# sqlguard-mcp

Servidor MCP em Go que dá ao Claude acesso **somente leitura** a um banco SQL (SQLite ou PostgreSQL).

## Regra de ouro

Este servidor nunca pode escrever no banco. Duas barreiras, e as duas precisam continuar de pé:

1. `internal/guard`: recusa a consulta antes de ela chegar ao banco.
2. `internal/store`: a conexão é somente leitura (SQLite `mode=ro` + `query_only`; PostgreSQL em transação `READ ONLY`).

Se uma mudança enfraquecer qualquer uma das duas, ela precisa de um teste novo que prove o contrário.

## Comandos

```bash
go test -race ./...        # testes
go vet ./... && gofmt -l . # análise estática
go build -o bin/sqlguard-mcp ./cmd/sqlguard-mcp
go run ./cmd/demo bin/sqlguard-mcp   # demonstração ponta a ponta
```

## Convenções

- Só biblioteca padrão, exceto os drivers de banco (`modernc.org/sqlite`, `pgx`). O protocolo MCP está implementado à mão em `internal/mcp`, de propósito.
- Mensagens ao usuário e comentários em português. Código e nomes de pacote em inglês, como no resto do ecossistema Go.
- Todo comando bloqueado ou permitido vira uma linha no log de auditoria (`mcp.Entry`).
- Dados de exemplo são fictícios (`testdata/loja.sql`). Nunca colocar dado real de cliente ou empresa neste repositório.

## Ao mexer no guard

Adicione o caso em `internal/guard/guard_test.go` primeiro, com o motivo esperado do bloqueio. Prefira recusar na dúvida: um falso bloqueio custa uma reformulação, um falso positivo de permissão custa um incidente.
