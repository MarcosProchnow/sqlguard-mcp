package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Estes testes usam um PostgreSQL de verdade e só rodam com SQLGUARD_PG_DSN definido,
// por exemplo: postgres://postgres:postgres@localhost:5432/sqlguard?sslmode=disable
// O schema public do banco é apagado e recriado a cada teste: use um banco descartável.

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("SQLGUARD_PG_DSN")
	if dsn == "" {
		t.Skip("defina SQLGUARD_PG_DSN para rodar os testes com PostgreSQL")
	}
	return dsn
}

func openPG(t *testing.T, timeout time.Duration) *Store {
	t.Helper()
	dsn := pgDSN(t)
	script, err := os.ReadFile(filepath.Join("..", "..", "testdata", "loja.sql"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(string(script)); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dsn, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestPGTablesEDescribe(t *testing.T) {
	st := openPG(t, 5*time.Second)
	ctx := context.Background()

	tabs, err := st.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tabs.RowCount != 4 || tabs.Rows[0][0] != "public.clientes" {
		t.Errorf("tabelas inesperadas: %+v", tabs.Rows)
	}

	for _, nome := range []string{"pedidos", "public.pedidos"} {
		cols, err := st.Describe(ctx, nome)
		if err != nil {
			t.Fatalf("Describe(%q): %v", nome, err)
		}
		if cols.RowCount != 4 || cols.Rows[0][0] != "id" {
			t.Errorf("colunas de %q inesperadas: %+v", nome, cols.Rows)
		}
	}
	if _, err := st.Describe(ctx, "nao_existe"); err == nil {
		t.Error("esperava erro para tabela inexistente")
	}
}

// A transação READ ONLY do PostgreSQL é a segunda barreira, independente do guard.
func TestPGRecusaEscrita(t *testing.T) {
	st := openPG(t, 5*time.Second)
	ctx := context.Background()
	for _, q := range []string{
		"DELETE FROM clientes",
		"UPDATE produtos SET preco = 0",
		"DROP TABLE pedidos",
		"INSERT INTO clientes VALUES (99, 'x', 'y', 'z')",
		"TRUNCATE itens_pedido",
	} {
		_, err := st.Query(ctx, q, 10)
		if err == nil {
			t.Errorf("o banco deveria recusar %q", q)
			continue
		}
		if !strings.Contains(err.Error(), "read-only") {
			t.Errorf("erro de %q deveria citar read-only: %v", q, err)
		}
	}
	res, err := st.Query(ctx, "SELECT COUNT(*) FROM clientes", 1)
	if err != nil || res.Rows[0][0] != int64(6) {
		t.Errorf("dados deveriam estar intactos: %v %v", res, err)
	}
}

func TestPGLimiteETimeout(t *testing.T) {
	st := openPG(t, 5*time.Second)
	res, err := st.Query(context.Background(), "SELECT generate_series(1, 1000)", 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.RowCount != 10 || !res.Truncated {
		t.Errorf("RowCount=%d Truncated=%v, esperado 10 e true", res.RowCount, res.Truncated)
	}

	curto := openPG(t, 300*time.Millisecond)
	start := time.Now()
	if _, err := curto.Query(context.Background(), "SELECT pg_sleep(10)", 1); err == nil {
		t.Fatal("esperava erro de tempo esgotado")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("levou %v para interromper", d)
	}
}
