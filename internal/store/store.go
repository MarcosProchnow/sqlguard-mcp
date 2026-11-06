// Package store acessa o banco em modo somente leitura (SQLite ou PostgreSQL).
//
// É a segunda barreira: mesmo que uma consulta passasse pelo guard, a conexão
// não tem permissão de escrita.
package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Dialect string

const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// Limites padrão de tamanho: o número de linhas não basta, uma única célula pode ter
// dezenas de megabytes e estourar a memória do cliente ou o contexto do modelo.
const (
	DefaultMaxCellBytes   = 8 * 1024
	DefaultMaxResultBytes = 256 * 1024
)

// Result é o resultado de uma consulta, pronto para virar JSON.
type Result struct {
	Columns        []string `json:"columns"`
	Rows           [][]any  `json:"rows"`
	RowCount       int      `json:"row_count"`
	Truncated      bool     `json:"truncated"`
	TruncatedCells int      `json:"truncated_cells,omitempty"`
}

type Store struct {
	db       *sql.DB
	dialect  Dialect
	timeout  time.Duration
	maxCell  int
	maxBytes int
}

func newStore(db *sql.DB, d Dialect, timeout time.Duration) *Store {
	return &Store{db: db, dialect: d, timeout: timeout, maxCell: DefaultMaxCellBytes, maxBytes: DefaultMaxResultBytes}
}

// Open abre o banco indicado pelo DSN: "sqlite:caminho.db" ou "postgres://...".
func Open(dsn string, timeout time.Duration) (*Store, error) {
	switch {
	case strings.HasPrefix(dsn, "sqlite:"):
		path := strings.TrimPrefix(dsn, "sqlite:")
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("banco SQLite não encontrado: %w", err)
		}
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=query_only(1)")
		if err != nil {
			return nil, err
		}
		return newStore(db, SQLite, timeout), nil
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, err
		}
		return newStore(db, Postgres, timeout), nil
	}
	return nil, errors.New(`DSN inválido: use "sqlite:caminho.db" ou "postgres://usuario:senha@host/banco"`)
}

// SetLimits muda os limites de tamanho: bytes por célula e bytes por resultado.
// Zero desliga o limite correspondente.
func (s *Store) SetLimits(maxCellBytes, maxResultBytes int) {
	s.maxCell, s.maxBytes = maxCellBytes, maxResultBytes
}

// SeedSQLite cria (ou recria) um banco SQLite a partir de um script. Só serve para
// montar a base de demonstração; o servidor nunca escreve no banco.
func SeedSQLite(path, script string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(script)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Dialect() Dialect { return s.dialect }

// Warnings avisa quando a conexão tem mais poder do que um leitor precisa. No
// PostgreSQL, um usuário com privilégios roda funções administrativas mesmo dentro
// de um SELECT; o certo é um usuário só com SELECT.
func (s *Store) Warnings(ctx context.Context) ([]string, error) {
	if s.dialect != Postgres {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	var super, createRole, createDB, replication, bypassRLS bool
	err := s.db.QueryRowContext(ctx,
		`SELECT rolsuper, rolcreaterole, rolcreatedb, rolreplication, rolbypassrls
		 FROM pg_roles WHERE rolname = current_user`).Scan(&super, &createRole, &createDB, &replication, &bypassRLS)
	if err != nil {
		return nil, err
	}
	var warns []string
	switch {
	case super:
		warns = append(warns, "o usuário do banco é superusuário: crie um usuário só com SELECT")
	case createRole || createDB || replication || bypassRLS:
		warns = append(warns, "o usuário do banco tem atributos administrativos (CREATEROLE, CREATEDB, REPLICATION ou BYPASSRLS)")
	}
	if !super {
		var writable int
		err = s.db.QueryRowContext(ctx,
			`SELECT count(*) FROM information_schema.tables t
			 WHERE t.table_schema NOT IN ('pg_catalog', 'information_schema') AND t.table_type = 'BASE TABLE'
			   AND (has_table_privilege(quote_ident(t.table_schema) || '.' || quote_ident(t.table_name), 'INSERT')
			     OR has_table_privilege(quote_ident(t.table_schema) || '.' || quote_ident(t.table_name), 'UPDATE')
			     OR has_table_privilege(quote_ident(t.table_schema) || '.' || quote_ident(t.table_name), 'DELETE')
			     OR has_table_privilege(quote_ident(t.table_schema) || '.' || quote_ident(t.table_name), 'TRUNCATE'))`,
		).Scan(&writable)
		if err != nil {
			return nil, err
		}
		if writable > 0 {
			warns = append(warns, fmt.Sprintf("o usuário do banco pode escrever em %d tabela(s): conceda apenas SELECT", writable))
		}
	}
	return warns, nil
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Query executa uma consulta já aprovada pelo guard, devolvendo no máximo maxRows linhas.
func (s *Store) Query(ctx context.Context, q string, maxRows int) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	var qr queryer = s.db
	if s.dialect == Postgres {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", s.timeout.Milliseconds())); err != nil {
			return nil, err
		}
		qr = tx
	}
	return s.collect(ctx, qr, maxRows, q)
}

// Tables lista as tabelas e views visíveis.
func (s *Store) Tables(ctx context.Context) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	q := `SELECT name AS tabela FROM sqlite_master
	      WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`
	if s.dialect == Postgres {
		q = `SELECT table_schema || '.' || table_name AS tabela FROM information_schema.tables
		     WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1`
	}
	return s.collect(ctx, s.db, 0, q)
}

var tableRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// Describe devolve as colunas de uma tabela.
func (s *Store) Describe(ctx context.Context, table string) (*Result, error) {
	if !tableRe.MatchString(table) {
		return nil, fmt.Errorf("nome de tabela inválido: %q", table)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	var res *Result
	if s.dialect == SQLite {
		if strings.Contains(table, ".") {
			return nil, fmt.Errorf("nome de tabela inválido para SQLite: %q", table)
		}
		raw, err := s.collect(ctx, s.db, 0, fmt.Sprintf(`PRAGMA table_info("%s")`, table))
		if err != nil {
			return nil, err
		}
		res = &Result{Columns: []string{"coluna", "tipo", "obrigatoria", "chave_primaria"}, Rows: [][]any{}}
		for _, r := range raw.Rows { // cid, name, type, notnull, dflt_value, pk
			res.Rows = append(res.Rows, []any{r[1], r[2], r[3] == int64(1), r[5] != int64(0)})
		}
	} else {
		schema, name := "public", table
		if parts := strings.SplitN(table, ".", 2); len(parts) == 2 {
			schema, name = parts[0], parts[1]
		}
		raw, err := s.collect(ctx, s.db, 0,
			`SELECT column_name, data_type, is_nullable FROM information_schema.columns
			 WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position`, schema, name)
		if err != nil {
			return nil, err
		}
		res = &Result{Columns: []string{"coluna", "tipo", "obrigatoria"}, Rows: [][]any{}}
		for _, r := range raw.Rows {
			res.Rows = append(res.Rows, []any{r[0], r[1], r[2] == "NO"})
		}
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("tabela não encontrada: %s", table)
	}
	res.RowCount = len(res.Rows)
	return res, nil
}

// collect lê as linhas respeitando os três limites: linhas, bytes por célula e bytes
// por resultado. Passar de qualquer um deles marca o resultado como truncado.
func (s *Store) collect(ctx context.Context, q queryer, maxRows int, query string, args ...any) (*Result, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	res := &Result{Columns: cols, Rows: [][]any{}}
	size := 0
	for rows.Next() {
		if maxRows > 0 && len(res.Rows) >= maxRows {
			res.Truncated = true
			break
		}
		if s.maxBytes > 0 && size >= s.maxBytes {
			res.Truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			v = normalize(v)
			if str, ok := v.(string); ok && s.maxCell > 0 && len(str) > s.maxCell {
				v = fmt.Sprintf("%s…[cortado: %d bytes]", cutString(str, s.maxCell), len(str))
				res.TruncatedCells++
			}
			vals[i] = v
			size += cellSize(v)
		}
		res.Rows = append(res.Rows, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.RowCount = len(res.Rows)
	return res, nil
}

func cellSize(v any) int {
	if s, ok := v.(string); ok {
		return len(s)
	}
	return 8
}

// cutString corta em n bytes sem partir um caractere UTF-8 ao meio.
func cutString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// normalize deixa o valor serializável em JSON sem perder informação em silêncio:
// NaN e infinito (que o JSON não representa) viram texto, e bytes que não são UTF-8
// viram base64.
func normalize(v any) any {
	switch x := v.(type) {
	case []byte:
		if utf8.Valid(x) {
			return string(x)
		}
		return "base64:" + base64.StdEncoding.EncodeToString(x)
	case string:
		if !utf8.ValidString(x) {
			return "base64:" + base64.StdEncoding.EncodeToString([]byte(x))
		}
		return x
	case float64:
		return finite(x)
	case float32:
		return finite(float64(x))
	case time.Time:
		return x.Format(time.RFC3339)
	}
	return v
}

func finite(f float64) any {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	}
	return f
}
