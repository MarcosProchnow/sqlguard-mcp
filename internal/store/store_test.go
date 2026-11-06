package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MarcosProchnow/sqlguard-mcp/internal/guard"
)

func openDemo(t *testing.T) *Store {
	t.Helper()
	return openDemoTimeout(t, 5*time.Second)
}

func openDemoTimeout(t *testing.T, timeout time.Duration) *Store {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "..", "testdata", "loja.sql"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "demo.db")
	if err := SeedSQLite(path, string(script)); err != nil {
		t.Fatal(err)
	}
	st, err := Open("sqlite:"+path, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestTablesEDescribe(t *testing.T) {
	st := openDemo(t)
	ctx := context.Background()

	tabs, err := st.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tabs.RowCount != 4 {
		t.Errorf("tabelas = %d, esperado 4", tabs.RowCount)
	}

	cols, err := st.Describe(ctx, "pedidos")
	if err != nil {
		t.Fatal(err)
	}
	if cols.RowCount != 4 || cols.Rows[0][0] != "id" {
		t.Errorf("colunas inesperadas: %+v", cols.Rows)
	}

	if _, err := st.Describe(ctx, "nao_existe"); err == nil {
		t.Error("esperava erro para tabela inexistente")
	}
	if _, err := st.Describe(ctx, `x"; DROP TABLE pedidos; --`); err == nil {
		t.Error("esperava erro para nome de tabela malicioso")
	}
}

func TestQueryLimitaLinhas(t *testing.T) {
	st := openDemo(t)
	res, err := st.Query(context.Background(), "SELECT id FROM pedidos ORDER BY id", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.RowCount != 5 || !res.Truncated {
		t.Errorf("RowCount=%d Truncated=%v, esperado 5 e true", res.RowCount, res.Truncated)
	}
}

// Segunda barreira: mesmo sem o guard, o banco recusa escrita.
func TestBancoRecusaEscrita(t *testing.T) {
	st := openDemo(t)
	for _, q := range []string{
		"DELETE FROM clientes",
		"UPDATE produtos SET preco = 0",
		"DROP TABLE pedidos",
		"INSERT INTO clientes VALUES (99, 'x', 'y', 'z')",
	} {
		if _, err := st.Query(context.Background(), q, 10); err == nil {
			t.Errorf("o banco deveria recusar %q", q)
		}
	}
	res, err := st.Query(context.Background(), "SELECT COUNT(*) FROM clientes", 1)
	if err != nil || res.Rows[0][0] != int64(6) {
		t.Errorf("dados deveriam estar intactos: %v %v", res, err)
	}
}

// Uma consulta que nunca termina precisa ser interrompida pelo tempo máximo.
func TestQueryTimeout(t *testing.T) {
	st := openDemoTimeout(t, 300*time.Millisecond)
	start := time.Now()
	_, err := st.Query(context.Background(),
		"WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r) SELECT COUNT(*) FROM r", 10)
	if err == nil {
		t.Fatal("esperava erro de tempo esgotado")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("levou %v para interromper", d)
	}
}

// O número de linhas não limita o tamanho: uma célula sozinha pode ter megabytes.
func TestLimitesDeTamanho(t *testing.T) {
	st := openDemo(t)
	st.SetLimits(100, 1000)
	ctx := context.Background()

	res, err := st.Query(ctx, "SELECT hex(zeroblob(5000)) AS h", 1)
	if err != nil {
		t.Fatal(err)
	}
	celula, _ := res.Rows[0][0].(string)
	if len(celula) > 200 || !strings.Contains(celula, "cortado") || res.TruncatedCells != 1 {
		t.Errorf("célula deveria ser cortada (tamanho %d, cortadas %d)", len(celula), res.TruncatedCells)
	}

	res, err = st.Query(ctx, "SELECT hex(zeroblob(45)) AS h FROM clientes, clientes, clientes", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.RowCount > 20 {
		t.Errorf("o resultado deveria parar por tamanho: RowCount=%d Truncated=%v", res.RowCount, res.Truncated)
	}
}

// Achado numa revisão: Inf e NaN fazem o json.Marshal falhar e a resposta saía vazia.
func TestInfinitoENaNViramTexto(t *testing.T) {
	st := openDemo(t)
	res, err := st.Query(context.Background(), "SELECT 1e999 AS mais, -1e999 AS menos", 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0][0] != "+Inf" || res.Rows[0][1] != "-Inf" {
		t.Errorf("linha = %v, esperado [+Inf -Inf]", res.Rows[0])
	}
}

func TestBytesQueNaoSaoUTF8(t *testing.T) {
	st := openDemo(t)
	res, err := st.Query(context.Background(), "SELECT x'ff00fe' AS b, x'6162' AS ok", 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0][0] != "base64:/wD+" || res.Rows[0][1] != "ab" {
		t.Errorf("linha = %v", res.Rows[0])
	}
}

func TestCutStringNaoPartePOrRune(t *testing.T) {
	got := cutString("aç", 2) // "ç" ocupa 2 bytes e começa no byte 1
	if got != "a" {
		t.Errorf("cutString = %q, esperado %q", got, "a")
	}
}

// snapshot guarda o conteúdo inteiro da base de demonstração (esquema e dados).
func snapshot(db *sql.DB) string {
	var b strings.Builder
	for _, q := range []string{
		"SELECT type, name, sql FROM sqlite_master ORDER BY name",
		"SELECT * FROM clientes ORDER BY id",
		"SELECT * FROM produtos ORDER BY id",
		"SELECT * FROM pedidos ORDER BY id",
		"SELECT * FROM itens_pedido ORDER BY pedido_id, produto_id",
	} {
		rows, err := db.Query(q)
		if err != nil {
			fmt.Fprintf(&b, "ERRO %s: %v\n", q, err) // tabela sumiu: o snapshot muda
			continue
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			fmt.Fprintln(&b, vals...)
		}
		rows.Close()
	}
	return b.String()
}

// FuzzGuardNaoAltera é o teste que pega o que o fuzzing do guard sozinho não pega:
// toda consulta que o guard aceitar roda num SQLite GRAVÁVEL (sem a segunda barreira)
// e o banco não pode mudar. Compara o guard com o que o banco de verdade faz.
func FuzzGuardNaoAltera(f *testing.F) {
	script, err := os.ReadFile(filepath.Join("..", "..", "testdata", "loja.sql"))
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{
		"SELECT * FROM clientes",
		"SELECT id AS [a'] FROM clientes WHERE id = 1 ; DELETE FROM clientes ; --'",
		"SELECT 1; DELETE FROM pedidos",
		"SELECT [id] FROM clientes",
		"SELECT 'x'; DROP TABLE clientes",
		"SELECT * FROM clientes -- ; DELETE FROM clientes",
		"WITH d AS (SELECT 1) SELECT * FROM d",
		"SELECT nome FROM clientes WHERE nome LIKE '%a%'",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, q string) {
		v := guard.Check(q, guard.SQLite)
		if !v.Allowed {
			return
		}
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1) // uma conexão só, senão cada uma teria o seu banco em memória
		if _, err := db.Exec(string(script)); err != nil {
			t.Fatal(err)
		}
		antes := snapshot(db)

		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if rows, err := db.QueryContext(ctx, v.SQL); err == nil {
			for rows.Next() {
			}
			rows.Close()
		}
		if depois := snapshot(db); depois != antes {
			t.Fatalf("o guard aceitou uma consulta que alterou o banco: %q", q)
		}
	})
}

// Achado numa revisão: o DSN era montado por concatenação e '#', '?' e '%' quebravam o caminho.
func TestCaminhosEspeciais(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "testdata", "loja.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, nome := range []string{"a b", "a#b", "a?b", "a%41b", "acentuação", "a&b=c"} {
		path := filepath.Join(t.TempDir(), nome, "demo.db")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := SeedSQLite(path, string(script)); err != nil {
			t.Errorf("%q: SeedSQLite: %v", nome, err)
			continue
		}
		st, err := Open("sqlite:"+path, 5*time.Second)
		if err != nil {
			t.Errorf("%q: Open: %v", nome, err)
			continue
		}
		res, err := st.Query(context.Background(), "SELECT COUNT(*) FROM clientes", 1)
		st.Close()
		if err != nil || res.Rows[0][0] != int64(6) {
			t.Errorf("%q: consulta falhou: %v %v", nome, res, err)
		}
	}
}

func TestSQLiteURI(t *testing.T) {
	for _, tc := range []struct{ in, query, want string }{
		{"/tmp/a b/x.db", "", "file:///tmp/a%20b/x.db"},
		{"/tmp/a#b/x.db", "mode=ro", "file:///tmp/a%23b/x.db?mode=ro"},
		{"C:/dados/x.db", "", "file:///C:/dados/x.db"},
	} {
		if got := sqliteURI(tc.in, tc.query); got != tc.want {
			t.Errorf("sqliteURI(%q) = %q, esperado %q", tc.in, got, tc.want)
		}
	}
}
