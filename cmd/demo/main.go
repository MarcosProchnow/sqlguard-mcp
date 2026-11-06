// Comando demo: sobe o servidor e conversa com ele como um cliente MCP faria
// (o Claude Code, por exemplo), mostrando consultas permitidas e bloqueadas.
// Cada passo declara o que espera, e a demo sai com erro se o servidor responder outra
// coisa: ela serve também de teste ponta a ponta no CI.
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

type step struct {
	titulo   string
	tool     string
	args     map[string]any
	bloqueio bool   // espera que o servidor recuse
	contem   string // trecho que a resposta precisa ter
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
	falhas := 0
	if strings.Join(names, ",") != "list_tables,describe_table,query" {
		fmt.Println("FALHA: ferramentas inesperadas")
		falhas++
	}

	steps := []step{
		{"1. Quais tabelas existem?", "list_tables", map[string]any{}, false, `"itens_pedido"`},
		{"2. Como é a tabela pedidos?", "describe_table", map[string]any{"table": "pedidos"}, false, `"cliente_id"`},
		{"3. Quem mais gastou? (permitida)", "query", map[string]any{"sql": `
			SELECT c.nome, ROUND(SUM(i.quantidade * i.preco_unit), 2) AS total
			FROM clientes c
			JOIN pedidos p       ON p.cliente_id = c.id AND p.status = 'pago'
			JOIN itens_pedido i  ON i.pedido_id = p.id
			GROUP BY c.nome ORDER BY total DESC LIMIT 3`}, false, `["Ana Souza",1926.6]`},
		{"4. Tentativa: apagar uma tabela (bloqueada)", "query", map[string]any{"sql": "DROP TABLE clientes"}, true, "só SELECT e WITH"},
		{"5. Tentativa: duas instruções (bloqueada)", "query", map[string]any{"sql": "SELECT 1; DELETE FROM pedidos"}, true, "uma instrução"},
		{"6. Tentativa: escrever dentro de uma CTE (bloqueada)", "query", map[string]any{"sql": "WITH x AS (DELETE FROM pedidos RETURNING *) SELECT * FROM x"}, true, "DELETE"},
		{"7. Tentativa: esconder o DELETE atrás de [colchetes] (bloqueada)", "query", map[string]any{"sql": "SELECT id AS [a'] FROM clientes WHERE id = 1 ; DELETE FROM clientes ; --'"}, true, "uma instrução"},
		{"8. Tentativa: função entre aspas (bloqueada)", "query", map[string]any{"sql": `SELECT "load_extension"('x')`}, true, "load_extension"},
		{"9. Os dados continuam intactos?", "query", map[string]any{"sql": "SELECT COUNT(*) AS pedidos FROM pedidos"}, false, "[[12]]"},
	}
	for _, s := range steps {
		text, isErr := c.tool(s.tool, s.args)
		marca := "OK      "
		if isErr {
			marca = "BLOQUEIO"
		}
		status := ""
		if isErr != s.bloqueio || !strings.Contains(text, s.contem) {
			status = fmt.Sprintf("   <-- FALHA: esperava bloqueio=%v e %q", s.bloqueio, s.contem)
			falhas++
		}
		fmt.Printf("%s\n  [%s] %s%s\n\n", s.titulo, marca, text, status)
	}

	fmt.Println("== Log de auditoria (tmp/audit.jsonl)")
	stdin.Close()
	cmd.Wait()
	b, err := os.ReadFile("tmp/audit.jsonl")
	if err != nil {
		fmt.Println("FALHA: sem log de auditoria:", err)
		os.Exit(1)
	}
	linhas := strings.Split(strings.TrimSpace(string(b)), "\n")
	for _, line := range linhas {
		var e map[string]any
		json.Unmarshal([]byte(line), &e)
		fmt.Printf("  %-14v permitida=%-5v linhas=%-3v %v\n", e["tool"], e["allowed"], e["rows"], e["reason"])
	}
	if len(linhas) != len(steps) {
		fmt.Printf("FALHA: %d linhas de auditoria, esperava %d\n", len(linhas), len(steps))
		falhas++
	}

	if falhas > 0 {
		fmt.Printf("\n%d verificação(ões) da demo falharam\n", falhas)
		os.Exit(1)
	}
	fmt.Println("\nTodas as verificações da demo passaram")
}
