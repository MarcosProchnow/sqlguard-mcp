package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MarcosProchnow/sqlguard-mcp/internal/store"
)

func newServer(t *testing.T) (*Server, *[]Entry) {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "..", "testdata", "loja.sql"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "demo.db")
	if err := store.SeedSQLite(path, string(script)); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open("sqlite:"+path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var log []Entry
	return New(st, 100, "test", func(e Entry) { log = append(log, e) }), &log
}

// rpc envia as mensagens (uma por linha) e devolve as respostas na mesma ordem.
func rpc(t *testing.T, s *Server, msgs ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(msgs, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("resposta inválida %q: %v", line, err)
		}
		resps = append(resps, m)
	}
	return resps
}

func toolText(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	res := resp["result"].(map[string]any)
	content := res["content"].([]any)[0].(map[string]any)
	return content["text"].(string), res["isError"].(bool)
}

func TestInitializeEListaFerramentas(t *testing.T) {
	s, _ := newServer(t)
	resps := rpc(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(resps) != 2 { // a notificação não gera resposta
		t.Fatalf("respostas = %d, esperado 2", len(resps))
	}
	info := resps[0]["result"].(map[string]any)["serverInfo"].(map[string]any)
	if info["name"] != "sqlguard-mcp" {
		t.Errorf("serverInfo = %v", info)
	}
	tools := resps[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 {
		t.Errorf("ferramentas = %d, esperado 3", len(tools))
	}
}

func TestQueryPermitidaEBloqueada(t *testing.T) {
	s, log := newServer(t)
	resps := rpc(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query","arguments":{"sql":"SELECT COUNT(*) AS n FROM pedidos"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"query","arguments":{"sql":"DROP TABLE pedidos"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"query","arguments":{"sql":"SELECT 1; DELETE FROM clientes"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"query","arguments":{"sql":"SELECT COUNT(*) AS n FROM pedidos"}}}`,
	)
	text, isErr := toolText(t, resps[0])
	if isErr || !strings.Contains(text, "[[12]]") {
		t.Errorf("consulta válida falhou: %q (erro=%v)", text, isErr)
	}
	for i, id := range []int{1, 2} {
		text, isErr = toolText(t, resps[id])
		if !isErr || !strings.HasPrefix(text, "consulta bloqueada") {
			t.Errorf("resposta %d deveria ser bloqueio: %q", i, text)
		}
	}
	// o banco continua intacto depois das tentativas
	text, _ = toolText(t, resps[3])
	if !strings.Contains(text, "[[12]]") {
		t.Errorf("dados alterados: %q", text)
	}

	if len(*log) != 4 || (*log)[1].Allowed || (*log)[1].Reason == "" {
		t.Errorf("auditoria incompleta: %+v", *log)
	}
}

func TestListTablesEDescribe(t *testing.T) {
	s, _ := newServer(t)
	resps := rpc(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_tables","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"describe_table","arguments":{"table":"clientes"}}}`,
	)
	text, isErr := toolText(t, resps[0])
	if isErr || !strings.Contains(text, "itens_pedido") {
		t.Errorf("list_tables: %q", text)
	}
	text, isErr = toolText(t, resps[1])
	if isErr || !strings.Contains(text, "nome") {
		t.Errorf("describe_table: %q", text)
	}
}

func TestMetodoDesconhecido(t *testing.T) {
	s, _ := newServer(t)
	resps := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"nao/existe"}`)
	if resps[0]["error"] == nil {
		t.Error("esperava erro JSON-RPC")
	}
}
