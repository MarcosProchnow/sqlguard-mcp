package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
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
