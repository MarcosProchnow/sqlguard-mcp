// Comando demo: sobe o servidor e conversa com ele como um cliente MCP faria
// (o Claude Code, por exemplo), mostrando consultas permitidas e bloqueadas.
//
//	go build -o bin/sqlguard-mcp ./cmd/sqlguard-mcp && go run ./cmd/demo bin/sqlguard-mcp
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type client struct {
	in  *json.Encoder
	out *bufio.Scanner
	id  int
}

func (c *client) call(method string, params any) map[string]any {
	c.id++
	c.in.Encode(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	if !c.out.Scan() {
		fmt.Println("servidor encerrou")
		os.Exit(1)
	}
	var m map[string]any
	json.Unmarshal(c.out.Bytes(), &m)
	return m
}

func (c *client) tool(name string, args map[string]any) (string, bool) {
	res := c.call("tools/call", map[string]any{"name": name, "arguments": args})["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	return text, res["isError"].(bool)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("uso: demo <caminho-do-sqlguard-mcp>")
		os.Exit(2)
	}
	os.MkdirAll("tmp", 0o755)
	cmd := exec.Command(os.Args[1], "--dsn", "sqlite:tmp/demo.db", "--seed", "testdata/loja.sql", "--audit", "tmp/audit.jsonl")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	os.Remove("tmp/audit.jsonl")
	if err := cmd.Start(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer cmd.Process.Kill()

	c := &client{in: json.NewEncoder(stdin), out: bufio.NewScanner(stdout)}
	c.out.Buffer(make([]byte, 0, 1<<16), 1<<22)

	init := c.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})["result"].(map[string]any)
	info := init["serverInfo"].(map[string]any)
	fmt.Printf("== initialize: %v %v\n", info["name"], info["version"])

	tools := c.call("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	names := []string{}
	for _, t := range tools {
		names = append(names, t.(map[string]any)["name"].(string))
	}
	fmt.Printf("== tools/list: %s\n\n", strings.Join(names, ", "))

	steps := []struct {
		titulo string
		tool   string
		args   map[string]any
	}{
		{"1. Quais tabelas existem?", "list_tables", map[string]any{}},
		{"2. Como é a tabela pedidos?", "describe_table", map[string]any{"table": "pedidos"}},
		{"3. Quem mais gastou? (permitida)", "query", map[string]any{"sql": `
			SELECT c.nome, ROUND(SUM(i.quantidade * i.preco_unit), 2) AS total
			FROM clientes c
			JOIN pedidos p       ON p.cliente_id = c.id AND p.status = 'pago'
			JOIN itens_pedido i  ON i.pedido_id = p.id
			GROUP BY c.nome ORDER BY total DESC LIMIT 3`}},
		{"4. Tentativa: apagar uma tabela (bloqueada)", "query", map[string]any{"sql": "DROP TABLE clientes"}},
		{"5. Tentativa: duas instruções (bloqueada)", "query", map[string]any{"sql": "SELECT 1; DELETE FROM pedidos"}},
		{"6. Tentativa: escrever dentro de uma CTE (bloqueada)", "query", map[string]any{"sql": "WITH x AS (DELETE FROM pedidos RETURNING *) SELECT * FROM x"}},
		{"7. Os dados continuam intactos?", "query", map[string]any{"sql": "SELECT COUNT(*) AS pedidos FROM pedidos"}},
	}
	for _, s := range steps {
		text, isErr := c.tool(s.tool, s.args)
		marca := "OK      "
		if isErr {
			marca = "BLOQUEIO"
		}
		fmt.Printf("%s\n  [%s] %s\n\n", s.titulo, marca, text)
	}

	fmt.Println("== Log de auditoria (tmp/audit.jsonl)")
	stdin.Close()
	cmd.Wait()
	if b, err := os.ReadFile("tmp/audit.jsonl"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var e map[string]any
			json.Unmarshal([]byte(line), &e)
			fmt.Printf("  %-14v permitida=%-5v linhas=%-3v %v\n", e["tool"], e["allowed"], e["rows"], e["reason"])
		}
	}
}
