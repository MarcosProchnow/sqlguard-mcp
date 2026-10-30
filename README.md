# sqlguard-mcp

Servidor [MCP](https://modelcontextprotocol.io) em Go que deixa o Claude consultar um banco SQL **sem poder alterar nada**.

Você aponta o servidor para um banco (SQLite ou PostgreSQL) e o Claude passa a listar tabelas, ver colunas e rodar `SELECT`. Qualquer outra coisa é recusada, com o motivo, e cada chamada fica registrada num log de auditoria.

![Fluxo de uma consulta: Claude, servidor MCP, guard de SQL, conexão somente leitura e banco](docs/arquitetura.svg)

## Por que existe

Dar a um agente de IA acesso direto a um banco é útil e arriscado. Um `DROP` ou um `DELETE` sem `WHERE` custa caro, e "pedir com educação no prompt" não é controle de segurança. Aqui o controle fica no código, em duas barreiras independentes:

| Barreira | Onde | O que faz |
|---|---|---|
| 1. Guard de SQL | [`internal/guard`](internal/guard/guard.go) | Recusa a consulta antes de ela chegar ao banco: só uma instrução, só `SELECT`/`WITH`, sem `INSERT`, `DROP`, `INTO`, `FOR UPDATE`, `pg_sleep`, `pg_read_file` etc. Ignora o que está dentro de textos e comentários, então `'drop table'` num filtro não dá falso bloqueio e um comando escondido atrás de comentário não passa. O que vai ao banco é a consulta já sem comentários. |
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
go test -race ./...                       # testes (os de PostgreSQL pedem SQLGUARD_PG_DSN)
bash scripts/test-all.sh                  # tudo: testes com SQLite e PostgreSQL, fuzzing e demo
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

## Testes

Verificado em um Codespace (saídas gravadas em [`docs/test-output.txt`](docs/test-output.txt) e [`docs/fuzz-output.txt`](docs/fuzz-output.txt)):

| O quê | Resultado |
|---|---|
| Guard de SQL: 32 casos, com os truques clássicos (comando atrás de comentário, escrita dentro de CTE, `SELECT ... INTO`, `FOR UPDATE`, `E'...'`, `$$`, várias instruções) | passam |
| Servidor MCP ponta a ponta: initialize, listagem, consulta permitida, consultas bloqueadas, auditoria | passam |
| SQLite: limite de linhas, tempo máximo, e o banco recusa `DELETE`, `UPDATE`, `DROP` e `INSERT` mesmo sem o guard | passam |
| **PostgreSQL 15 de verdade:** tabelas, colunas, limite de linhas, `statement_timeout`, e a transação `READ ONLY` recusa escrita (o erro cita `read-only`) | passam |
| Fuzzing do guard: ~537 mil entradas em 30 s, conferindo que nada aceito tem comando proibido, `;` solto ou deixa de ser idempotente | sem falhas |

Total: 57 verificações, nenhuma pulada, nenhuma falha. O [CI](.github/workflows/ci.yml) repete tudo a cada push, com um PostgreSQL como serviço.

## Decisões de segurança

- **Prefere recusar na dúvida.** Um falso bloqueio custa uma reformulação da consulta; uma consulta de escrita que passe custa um incidente.
- **Bancos leem o SQL de jeitos diferentes, e isso é brecha.** `E'...'` (escape do PostgreSQL) e `$$...$$` fazem cada banco enxergar os limites do texto de um jeito, e um atacante poderia esconder um `DELETE` onde o guard vê só um texto. Em vez de tentar acertar todos os dialetos, o guard **recusa essas duas formas**. Foi achado numa revisão do próprio código e virou caso de teste.
- **Duas barreiras, não uma.** O guard pode errar; a conexão somente leitura não depende dele.

## Como foi feito

- **Só biblioteca padrão**, fora os drivers de banco. O protocolo MCP (JSON-RPC 2.0 sobre stdio) está implementado à mão em [`internal/mcp`](internal/mcp/server.go), de propósito, para dar para ler e entender.
- **Feito com o Claude Code.** O [`CLAUDE.md`](CLAUDE.md) define a regra de ouro do projeto e um hook ([`.claude/hooks/post-edit.sh`](.claude/hooks/post-edit.sh)) roda `gofmt` e `go vet` depois de cada edição em Go.

## Limitações conhecidas

- O guard analisa o texto da consulta, não a árvore SQL completa. Pode bloquear uma consulta legítima que use uma palavra da lista (por exemplo, uma coluna chamada `set` sem aspas). Um parser de verdade é um próximo passo.
- Não aceita `E'...'` nem `$$...$$` (ver acima).
- Ainda não há controle de acesso por tabela ou coluna. Com PostgreSQL, use um usuário do banco que só tenha `SELECT` nas tabelas desejadas.

## Licença

[MIT](LICENSE)
