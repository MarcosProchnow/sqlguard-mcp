// Package store acessa o banco em modo somente leitura (SQLite ou PostgreSQL).
//
// É a segunda barreira: mesmo que uma consulta passasse pelo guard, a conexão
// não tem permissão de escrita.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Dialect string

const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// Result é o resultado de uma consulta, pronto para virar JSON.
type Result struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"row_count"`
	Truncated bool     `json:"truncated"`
}

type Store struct {
	db      *sql.DB
	dialect Dialect
	timeout time.Duration
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
		return &Store{db: db, dialect: SQLite, timeout: timeout}, nil
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, err
		}
		return &Store{db: db, dialect: Postgres, timeout: timeout}, nil
	}
	return nil, errors.New(`DSN inválido: use "sqlite:caminho.db" ou "postgres://usuario:senha@host/banco"`)
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
	return collect(ctx, qr, maxRows, q)
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
	return collect(ctx, s.db, 0, q)
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
		raw, err := collect(ctx, s.db, 0, fmt.Sprintf(`PRAGMA table_info("%s")`, table))
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
		raw, err := collectArgs(ctx, s.db, 0,
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

func collect(ctx context.Context, q queryer, maxRows int, query string) (*Result, error) {
	return collectArgs(ctx, q, maxRows, query)
}

func collectArgs(ctx context.Context, q queryer, maxRows int, query string, args ...any) (*Result, error) {
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
	for rows.Next() {
		if maxRows > 0 && len(res.Rows) >= maxRows {
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
			vals[i] = normalize(v)
		}
		res.Rows = append(res.Rows, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.RowCount = len(res.Rows)
	return res, nil
}

func normalize(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339)
	}
	return v
}
