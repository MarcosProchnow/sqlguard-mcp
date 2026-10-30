// Comando sqlguard-mcp: servidor MCP que dá acesso somente leitura a um banco SQL.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/MarcosProchnow/sqlguard-mcp/internal/mcp"
	"github.com/MarcosProchnow/sqlguard-mcp/internal/store"
)

const version = "0.1.0"

func main() {
	var (
		dsn     = flag.String("dsn", os.Getenv("SQLGUARD_DSN"), `banco: "sqlite:arquivo.db" ou "postgres://usuario:senha@host/banco" (ou variável SQLGUARD_DSN)`)
		maxRows = flag.Int("max-rows", 200, "máximo de linhas por consulta")
		timeout = flag.Duration("timeout", 5*time.Second, "tempo máximo por consulta")
		audit   = flag.String("audit", "", "arquivo do log de auditoria (JSON por linha); vazio escreve em stderr")
		seed    = flag.String("seed", "", "só SQLite: script SQL que cria o banco antes de abrir (uso em demonstração)")
	)
	flag.Parse()

	if err := run(*dsn, *seed, *audit, *maxRows, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "sqlguard-mcp:", err)
		os.Exit(1)
	}
}

func run(dsn, seed, auditPath string, maxRows int, timeout time.Duration) error {
	if dsn == "" {
		return fmt.Errorf("informe --dsn ou a variável SQLGUARD_DSN")
	}
	if seed != "" {
		path, ok := cutPrefix(dsn, "sqlite:")
		if !ok {
			return fmt.Errorf("--seed só funciona com SQLite")
		}
		script, err := os.ReadFile(seed)
		if err != nil {
			return err
		}
		if err := store.SeedSQLite(path, string(script)); err != nil {
			return fmt.Errorf("criando banco de demonstração: %w", err)
		}
	}

	st, err := store.Open(dsn, timeout)
	if err != nil {
		return err
	}
	defer st.Close()

	var logOut io.Writer = os.Stderr
	if auditPath != "" {
		f, err := os.OpenFile(auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		logOut = f
	}
	enc := json.NewEncoder(logOut)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	srv := mcp.New(st, maxRows, version, func(e mcp.Entry) { enc.Encode(e) })
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return s, false
}
