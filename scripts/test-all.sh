#!/usr/bin/env bash
# Roda a suíte completa num ambiente Debian/Ubuntu (ex.: Codespaces): instala e sobe um
# PostgreSQL local descartável, executa os testes com SQLite e PostgreSQL, o fuzzing do
# guard e a demonstração, e grava as saídas em docs/.
set -euo pipefail
cd "$(dirname "$0")/.."

pgbin() { ls -d /usr/lib/postgresql/*/bin 2>/dev/null | tail -1; }

if [ -n "${SQLGUARD_PG_DSN:-}" ]; then
  echo "== usando o PostgreSQL de SQLGUARD_PG_DSN"
else
  # Instala o PostgreSQL só se der para usar sudo sem senha.
  if [ -z "$(pgbin)" ] && sudo -n true 2>/dev/null; then
    # repositórios de terceiros da imagem (ex.: Yarn) às vezes têm chave vencida; não impedem a instalação
    sudo apt-get update -qq || true
    sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq postgresql >/dev/null
  fi
  if [ -n "$(pgbin)" ]; then
    # Cluster descartável do próprio usuário, em outra porta: não precisa de sudo nem de senha.
    PGBIN="$(pgbin)"
    PGDATA="${TMPDIR:-/tmp}/sqlguard-pg"
    [ -d "$PGDATA" ] || "$PGBIN/initdb" -D "$PGDATA" -U postgres --auth=trust >/dev/null
    "$PGBIN/pg_ctl" -D "$PGDATA" -o "-p 5433 -k $PGDATA" -l "$PGDATA/log" -w start >/dev/null || true
    "$PGBIN/createdb" -h localhost -p 5433 -U postgres sqlguard 2>/dev/null || true
    export SQLGUARD_PG_DSN="postgres://postgres@localhost:5433/sqlguard?sslmode=disable"
  else
    echo "== sem PostgreSQL: defina SQLGUARD_PG_DSN (os testes de PostgreSQL serão pulados)"
  fi
fi

echo "== formatação e análise estática"
test -z "$(gofmt -l .)"
go vet ./...

echo "== testes (SQLite e PostgreSQL)"
go test -race -v ./... 2>&1 | tee docs/test-output.txt | grep -E '^(ok|FAIL|---|\?)' | grep -vE '^    ---' | tail -20

echo "== fuzzing do guard (30 s)"
go test -run '^$' -fuzz FuzzCheck -fuzztime 30s ./internal/guard 2>&1 | tee docs/fuzz-output.txt | tail -8

echo "== demonstração"
go build -o bin/sqlguard-mcp ./cmd/sqlguard-mcp
go run ./cmd/demo bin/sqlguard-mcp > docs/demo-output.txt 2>&1
echo "demo: saída $?"
