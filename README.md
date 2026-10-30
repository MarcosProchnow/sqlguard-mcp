# sqlguard-mcp

Servidor [MCP](https://modelcontextprotocol.io) em Go que deixa o Claude consultar um banco SQL **sem poder alterar nada**.

Você aponta o servidor para um banco (SQLite ou PostgreSQL) e o Claude passa a listar tabelas, ver colunas e rodar `SELECT`. Qualquer outra coisa é recusada, com o motivo, e cada chamada fica registrada num log de auditoria.

## Por que existe

Dar a um agente de IA acesso direto a um banco é útil e arriscado. Um `DROP` ou um `DELETE` sem `WHERE` custa caro, e "pedir com educação no prompt" não é controle de segurança. Aqui o controle fica no código, em duas barreiras independentes:

| Barreira | Onde | O que faz |
|---|---|---|
| 1. Guard de SQL | [`internal/guard`](internal/guard/guard.go) | Recusa a consulta antes de ela chegar ao banco: só uma instrução, só `SELECT`/`WITH`, sem `INSERT`, `DROP`, `INTO`, `FOR UPDATE`, `pg_sleep`, `pg_read_file` etc. Ignora o que está dentro de textos e comentários, então `'drop table'` num filtro não dá falso bloqueio e um comando escondido atrás de comentário não passa. |
| 2. Conexão somente leitura | [`internal/store`](internal/store/store.go) | SQLite abre com `mode=ro` e `query_only`; PostgreSQL roda em transação `READ ONLY` com `statement_timeout`. Mesmo que o guard falhasse, o banco recusaria a escrita. |

Mais: limite de linhas por consulta, tempo máximo e log de auditoria em JSON (uma linha por chamada, permitida ou não).

## Ferramentas

| Ferramenta | O que faz |
|---|---|
| `list_tables` | Lista tabelas e views |
| `describe_table` | Colunas de uma tabela: nome, tipo, se é obrigatória |
| `query` | Um único `SELECT` (ou `WITH ... SELECT`), com `max_rows` opcional |

## Demonstração

Saída real de `go run ./cmd/demo bin/sqlguard-mcp`, que sobe o servidor e fala com ele como um cliente MCP faria (loja fictícia em [`testdata/loja.sql`](testdata/loja.sql)):

```text
== initialize: sqlguard-mcp 0.1.0
== tools/list: list_tables, describe_table, query

3. Quem mais gastou? (permitida)
  [OK      ] {"columns":["nome","total"],"rows":[["Ana Souza",1926.6],["Carla Mendes",998],["Fábio Nunes",908.8]],...}

4. Tentativa: apagar uma tabela (bloqueada)
  [BLOQUEIO] consulta bloqueada: só SELECT e WITH são permitidos (recebido: DROP)

5. Tentativa: duas instruções (bloqueada)
  [BLOQUEIO] consulta bloqueada: apenas uma instrução por consulta

6. Tentativa: escrever dentro de uma CTE (bloqueada)
  [BLOQUEIO] consulta bloqueada: palavra proibida: DELETE

7. Os dados continuam intactos?
  [OK      ] {"columns":["pedidos"],"rows":[[12]],"row_count":1,"truncated":false}

== Log de auditoria
  list_tables    permitida=true  linhas=4
  describe_table permitida=true  linhas=4
  query          permitida=true  linhas=3
  query          permitida=false linhas=0   só SELECT e WITH são permitidos (recebido: DROP)
  query          permitida=false linhas=0   apenas uma instrução por consulta
  query          permitida=false linhas=0   palavra proibida: DELETE
  query          permitida=true  linhas=1
```

A saída completa está em [`docs/demo-output.txt`](docs/demo-output.txt) e a dos testes em [`docs/test-output.txt`](docs/test-output.txt).

## Como usar

Precisa de Go 1.23 ou mais novo. Dá para abrir o repositório num Codespace, que já vem pronto.

```bash
go build -o bin/sqlguard-mcp ./cmd/sqlguard-mcp
go run ./cmd/demo bin/sqlguard-mcp        # demonstração ponta a ponta
go test -race ./...                       # testes
```

Conectar ao Claude Code, com a base de demonstração:

```bash
bin/sqlguard-mcp --dsn sqlite:tmp/demo.db --seed testdata/loja.sql   # cria a base uma vez
claude mcp add sqlguard -- $(pwd)/bin/sqlguard-mcp --dsn sqlite:$(pwd)/tmp/demo.db
```

Com PostgreSQL, use `--dsn postgres://usuario:senha@host/banco` (ou a variável `SQLGUARD_DSN`) e, de preferência, um usuário do banco que só tenha `SELECT`.

| Opção | Padrão | |
|---|---|---|
| `--dsn` | `$SQLGUARD_DSN` | `sqlite:arquivo.db` ou `postgres://...` |
| `--max-rows` | 200 | máximo de linhas por consulta |
| `--timeout` | 5s | tempo máximo por consulta |
| `--audit` | stderr | arquivo do log de auditoria |

## Como foi feito

- **Só biblioteca padrão**, fora os drivers de banco. O protocolo MCP (JSON-RPC 2.0 sobre stdio) está implementado à mão em [`internal/mcp`](internal/mcp/server.go), de propósito, para dar para ler e entender.
- **Feito com o Claude Code.** O [`CLAUDE.md`](CLAUDE.md) define a regra de ouro do projeto e um hook ([`.claude/hooks/post-edit.sh`](.claude/hooks/post-edit.sh)) roda `gofmt` e `go vet` depois de cada edição em Go.
- **Testes de segurança primeiro:** 38 casos, entre eles os truques clássicos (comando atrás de comentário, escrita dentro de CTE, `SELECT ... INTO`, `FOR UPDATE`, texto com `$$`) e um teste que confirma que o banco recusa escrita mesmo sem o guard.

## Limitações conhecidas

- O guard analisa o texto da consulta, não a árvore SQL completa. Por isso prefere recusar na dúvida: pode bloquear uma consulta legítima que use uma palavra da lista, nunca o contrário. Um parser de verdade é um próximo passo.
- O suporte a PostgreSQL está escrito, mas os testes automáticos rodam só em SQLite. Falta um teste de integração com PostgreSQL no CI.
- Ainda não há controle de acesso por tabela ou coluna.

## Licença

[MIT](LICENSE)
