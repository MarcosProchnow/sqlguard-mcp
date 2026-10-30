// Package mcp implementa o trecho do Model Context Protocol que este servidor usa:
// JSON-RPC 2.0 sobre stdio, com as chamadas initialize, ping, tools/list e tools/call.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/MarcosProchnow/sqlguard-mcp/internal/guard"
	"github.com/MarcosProchnow/sqlguard-mcp/internal/store"
)

const defaultProtocol = "2025-06-18"

// Backend é o que o servidor precisa do banco.
type Backend interface {
	Tables(ctx context.Context) (*store.Result, error)
	Describe(ctx context.Context, table string) (*store.Result, error)
	Query(ctx context.Context, sql string, maxRows int) (*store.Result, error)
}

// Entry é uma linha do log de auditoria: toda chamada de ferramenta gera uma.
type Entry struct {
	Time       time.Time `json:"time"`
	Tool       string    `json:"tool"`
	SQL        string    `json:"sql,omitempty"`
	Allowed    bool      `json:"allowed"`
	Reason     string    `json:"reason,omitempty"`
	Rows       int       `json:"rows"`
	DurationMs int64     `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
}

type Server struct {
	backend Backend
	maxRows int
	audit   func(Entry)
	version string
}

func New(b Backend, maxRows int, version string, audit func(Entry)) *Server {
	if audit == nil {
		audit = func(Entry) {}
	}
	return &Server{backend: b, maxRows: maxRows, audit: audit, version: version}
}

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve lê uma mensagem JSON por linha de r e escreve as respostas em w, até r acabar.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var mu sync.Mutex
	write := func(resp response) {
		resp.JSONRPC = "2.0"
		b, _ := json.Marshal(resp)
		mu.Lock()
		defer mu.Unlock()
		w.Write(append(b, '\n'))
	}

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			write(response{Error: &rpcError{-32700, "JSON inválido"}})
			continue
		}
		if len(req.ID) == 0 { // notificação: não tem resposta
			continue
		}
		result, rerr := s.handle(ctx, req)
		write(response{ID: req.ID, Result: result, Error: rerr})
	}
	return sc.Err()
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = defaultProtocol
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "sqlguard-mcp", "version": s.version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefs()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "parâmetros inválidos"}
		}
		text, isErr := s.callTool(ctx, p.Name, p.Arguments)
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		}, nil
	}
	return nil, &rpcError{-32601, "método não suportado: " + req.Method}
}

func toolDefs() []map[string]any {
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	return []map[string]any{
		{
			"name":        "list_tables",
			"description": "Lista as tabelas e views do banco.",
			"inputSchema": obj(map[string]any{}),
		},
		{
			"name":        "describe_table",
			"description": "Mostra as colunas de uma tabela: nome, tipo e se é obrigatória.",
			"inputSchema": obj(map[string]any{
				"table": map[string]any{"type": "string", "description": "Nome da tabela, com schema se for PostgreSQL (ex.: public.pedidos)."},
			}, "table"),
		},
		{
			"name":        "query",
			"description": "Executa um único SELECT (ou WITH ... SELECT) somente leitura, com limite de linhas e de tempo. Qualquer comando que escreva é recusado.",
			"inputSchema": obj(map[string]any{
				"sql":      map[string]any{"type": "string", "description": "A consulta SELECT."},
				"max_rows": map[string]any{"type": "integer", "description": "Máximo de linhas; não pode passar do limite do servidor."},
			}, "sql"),
		},
	}
}

func (s *Server) callTool(ctx context.Context, name string, raw json.RawMessage) (string, bool) {
	start := time.Now()
	e := Entry{Time: start.UTC(), Tool: name}
	finish := func(text string, isErr bool) (string, bool) {
		e.DurationMs = time.Since(start).Milliseconds()
		s.audit(e)
		return text, isErr
	}
	fail := func(err error) (string, bool) {
		e.Error = err.Error()
		return finish("erro: "+err.Error(), true)
	}
	ok := func(res *store.Result) (string, bool) {
		e.Allowed, e.Rows = true, res.RowCount
		b, _ := json.Marshal(res)
		return finish(string(b), false)
	}

	switch name {
	case "list_tables":
		e.Allowed = true
		res, err := s.backend.Tables(ctx)
		if err != nil {
			return fail(err)
		}
		return ok(res)

	case "describe_table":
		var a struct {
			Table string `json:"table"`
		}
		if err := json.Unmarshal(raw, &a); err != nil || a.Table == "" {
			return fail(fmt.Errorf("informe o argumento table"))
		}
		e.Allowed = true
		res, err := s.backend.Describe(ctx, a.Table)
		if err != nil {
			return fail(err)
		}
		return ok(res)

	case "query":
		var a struct {
			SQL     string `json:"sql"`
			MaxRows int    `json:"max_rows"`
		}
		if err := json.Unmarshal(raw, &a); err != nil || a.SQL == "" {
			return fail(fmt.Errorf("informe o argumento sql"))
		}
		e.SQL = a.SQL
		v := guard.Check(a.SQL)
		if !v.Allowed {
			e.Reason = v.Reason
			return finish("consulta bloqueada: "+v.Reason, true)
		}
		limit := s.maxRows
		if a.MaxRows > 0 && a.MaxRows < limit {
			limit = a.MaxRows
		}
		e.Allowed = true
		res, err := s.backend.Query(ctx, v.SQL, limit)
		if err != nil {
			return fail(err)
		}
		return ok(res)
	}
	return fail(fmt.Errorf("ferramenta desconhecida: %s", name))
}
