# sqlguard-mcp

Servidor [MCP](https://modelcontextprotocol.io) em Go que deixa o Claude consultar um banco SQL **sem poder alterar nada**.

Você aponta o servidor para um banco (SQLite ou PostgreSQL) e o Claude passa a listar tabelas, ver colunas e rodar `SELECT`. Qualquer outra coisa é recusada, com o motivo, e cada chamada fica registrada num log de auditoria.

![Fluxo de uma consulta: Claude, servidor MCP, guard de SQL, conexão somente leitura e banco](docs/arquitetura.svg)

## Por que existe

Dar a um agente de IA acesso direto a um banco é útil e arriscado. Um `DROP` ou um `DELETE` sem `WHERE` custa caro, e "pedir com educação no prompt" não é controle de segurança. Aqui o controle fica no código, em duas barreiras independentes:

| Barreira | Onde | O que faz |
|---|---|---|
| 1. Guard de SQL | [`internal/guard`](internal/guard/guard.go) | Recusa a consulta antes de ela chegar ao banco: só uma instrução, só `SELECT`/`WITH`, sem `INSERT`, `DROP`, `INTO`, `FOR UPDATE`, `pg_sleep`, `pg_read_file` etc. Ignora o que está dentro de textos e comentários, então `'drop table'` num filtro não dá falso bloqueio e um comando escondido atrás de comentário não passa. O guard lê o SQL **do dialeto do banco conectado** (SQLite ou PostgreSQL) e o que vai ao banco é a consulta já sem comentários. |
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

7. Tentativa: esconder o DELETE atrás de [colchetes] (bloqueada)
  [BLOQUEIO] consulta bloqueada: apenas uma instrução por consulta

8. Tentativa: função entre aspas (bloqueada)
  [BLOQUEIO] consulta bloqueada: função proibida: load_extension

9. Os dados continuam intactos?
  [OK      ] {"columns":["pedidos"],"rows":[[12]],"row_count":1,"truncated":false}

Todas as verificações da demo passaram
```

Cada passo declara o que espera, e a demo sai com erro se a resposta for outra: ela é também um teste ponta a ponta no CI.

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
| `--max-cell-bytes` | 8192 | máximo de bytes por célula; o excedente é cortado e contado em `truncated_cells` |
| `--max-bytes` | 262144 | máximo de bytes por resultado; o excedente trunca o resultado |
| `--strict` | desligado | recusa iniciar se o usuário do PostgreSQL tiver mais poder que um leitor (sem isso, só avisa) |

## Testes

Verificado em um Codespace (saídas gravadas em [`docs/test-output.txt`](docs/test-output.txt) e [`docs/fuzz-output.txt`](docs/fuzz-output.txt)):

| O quê | Resultado |
|---|---|
| Guard de SQL: 58 casos, rodados nos dois dialetos, com os truques clássicos (comando atrás de comentário, escrita dentro de CTE ou depois dela, `SELECT ... INTO`, `FOR UPDATE`, função entre aspas, `E'...'`, `$$`, `[colchetes]` do SQLite, várias instruções) e os nomes de coluna que não podem ser bloqueados (`call`, `begin`, `lock`) | passam |
| Servidor MCP ponta a ponta: initialize, negociação de versão, `id` nulo, listagem, consultas permitidas e bloqueadas, auditoria, resposta que o JSON não representa, encerramento por Ctrl+C | passam |
| SQLite: limites de linhas e de bytes, tempo máximo, `Inf`/`NaN`, bytes que não são UTF-8, caminhos com `#`, `?`, `%` e acento, e o banco recusa escrita mesmo sem o guard | passam |
| **PostgreSQL 15 de verdade:** tabelas, colunas, limites, `statement_timeout`, transação `READ ONLY` recusando escrita, e o aviso de privilégios (superusuário e usuário só com `SELECT`) | passam |
| Fuzzing do guard: ~79 mil entradas em 30 s, conferindo que o que foi aceito não tem segunda instrução nem violação, e passa de novo pelo guard | sem falhas |
| **Fuzzing diferencial:** ~34 mil entradas em 30 s; toda consulta que o guard aceita roda num SQLite **gravável** (sem a segunda barreira) e o banco não pode mudar | sem falhas |
| Demo ponta a ponta com o binário real, conferindo o resultado de cada passo | passa |

Total: 180 verificações, nenhuma pulada, nenhuma falha. O [CI](.github/workflows/ci.yml) repete tudo a cada push, com um PostgreSQL como serviço.

O fuzzing diferencial existe por causa de um achado: o fuzzing do guard sozinho só confere a coerência do próprio guard e não pegou o problema dos colchetes (veja abaixo). Comparar com o que o banco de verdade faz é o que fecha essa lacuna.

## Decisões de segurança

- **Prefere recusar na dúvida.** Um falso bloqueio custa uma reformulação da consulta; uma consulta de escrita que passe custa um incidente.
- **Bancos leem o SQL de jeitos diferentes, e isso é brecha.** `E'...'` (escape do PostgreSQL), `$$...$$` e os `[colchetes]` do SQLite fazem cada banco enxergar os limites de um texto de um jeito, e um atacante poderia esconder um `DELETE` onde o guard vê só um texto. O guard recusa `E'...'` e `$$`, e conhece o dialeto para tratar os colchetes.
- **Palavra proibida depende da posição.** `SELECT call FROM ligacoes` é uma coluna; `WITH x AS (SELECT 1) DELETE FROM t` é um comando. O guard trabalha com tokens e só libera a palavra onde um comando não pode começar (depois de `SELECT`, `,`, `.`, `AS`, `WHERE`, `ORDER BY` e afins). Na dúvida, recusa.
- **Função é bloqueada pelo nome, com ou sem aspas.** `pg_sleep(10)`, `pg_catalog.pg_sleep(10)` e `"pg_sleep"(10)` são a mesma chamada. Uma coluna chamada `lo_total` não é chamada de função e passa.
- **Duas barreiras, não uma.** O guard pode errar; a conexão somente leitura não depende dele.
- **Resposta limitada em bytes, não só em linhas.** Uma célula pode ter megabytes; por isso há limite por célula e por resultado.
- **Erro nunca vira resposta vazia.** `Inf` e `NaN` viram texto, bytes que não são UTF-8 viram base64, e o que não puder ser serializado vira erro explícito.
- **Usuário do banco com o mínimo de poder.** No PostgreSQL, funções administrativas rodam dentro de um `SELECT`, mesmo numa transação `READ ONLY`. O guard bloqueia as conhecidas, mas a defesa de verdade é um usuário só com `SELECT`: o servidor avisa ao iniciar se o usuário tiver mais poder (`--strict` recusa).

### Revisão de segurança e o que ela achou

Uma revisão do próprio código, com sondas rodando em SQLite e PostgreSQL reais, achou estes problemas, todos corrigidos e cobertos por teste:

1. **O guard não entendia `[identificador]` do SQLite.** Um apóstrofo dentro dos colchetes escondia um `DELETE`; num banco gravável, a tabela foi de 6 para 0 linhas. A conexão somente leitura barrou o ataque, e foi essa segunda barreira que o provou necessária.
2. **Erro de JSON engolido:** `Inf`/`NaN` davam resposta vazia como se fosse sucesso.
3. **Sem limite de bytes:** uma consulta devolveu 40 MB de uma vez.
4. **PostgreSQL:** `pg_reload_conf()`, `pg_stat_reset()` e `pg_notify()` rodavam num `SELECT` com um usuário privilegiado.
5. **Função entre aspas:** `"pg_sleep"(10)` escapava da lista de funções bloqueadas, porque o guard ignorava o conteúdo das aspas. Foi isso que levou a reescrever o guard com tokens.

Também foram corrigidos: colunas legítimas recusadas, caminhos de SQLite com `#`, `?` e `%`, versão de protocolo desconhecida ecoada ao cliente, `id` nulo aceito, Ctrl+C que não encerrava e uma demo que não conferia nada.

## Como foi feito

- **Só biblioteca padrão**, fora os drivers de banco. O protocolo MCP (JSON-RPC 2.0 sobre stdio) está implementado à mão em [`internal/mcp`](internal/mcp/server.go), de propósito, para dar para ler e entender.
- **Feito com o Claude Code.** O [`CLAUDE.md`](CLAUDE.md) define a regra de ouro do projeto e um hook ([`.claude/hooks/post-edit.sh`](.claude/hooks/post-edit.sh)) roda `gofmt` e `go vet` depois de cada edição em Go.

## Limitações conhecidas

- O guard analisa tokens, não a árvore SQL completa. Pode recusar uma consulta legítima em que uma palavra da lista apareça fora das posições conhecidas (por exemplo, um alias sem `AS`, como `SELECT nome call`). Também recusa `EXPLAIN` com opções. Um parser de verdade é o próximo passo.
- A lista de funções bloqueadas é uma lista: uma função administrativa que não esteja nela passa pelo guard. Por isso a defesa principal no PostgreSQL é um usuário só com `SELECT`, e o servidor avisa quando não é o caso.
- O fuzzing diferencial roda só contra o SQLite. No PostgreSQL, o que protege contra escrita é a transação `READ ONLY`, verificada pelos testes.
- Não aceita `E'...'` nem `$$...$$` (ver acima).
- Ainda não há controle de acesso por tabela ou coluna. Com PostgreSQL, use um usuário do banco que só tenha `SELECT` nas tabelas desejadas.

## Licença

[MIT](LICENSE)
