#!/usr/bin/env bash
# Roda a suíte completa num ambiente Debian/Ubuntu (ex.: Codespaces): instala e sobe um
# PostgreSQL local descartável, executa os testes com SQLite e PostgreSQL, o fuzzing do
# guard e a demonstração, e grava as saídas em docs/.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -n "${SQLGUARD_PG_DSN:-}" ]; then
  echo "== usando o PostgreSQL de SQLGUARD_PG_DSN"
elif sudo -n true 2>/dev/null; then
  if ! command -v pg_ctlcluster >/dev/null; then
    # repositórios de terceiros da imagem (ex.: Yarn) às vezes têm chave vencida; não impedem a instalação
    sudo apt-get update -qq || true
    sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq postgresql >/dev/null
  fi
  sudo service postgresql start
  sudo -u postgres psql -qc "ALTER USER postgres PASSWORD 'postgres'"
  sudo -u postgres psql -tc "SELECT 1 FROM pg_database WHERE datname='sqlguard'" | grep -q 1 \
    || sudo -u postgres psql -qc "CREATE DATABASE sqlguard"
  export SQLGUARD_PG_DSN="postgres://postgres:postgres@localhost:5432/sqlguard?sslmode=disable"
else
  echo "== sem PostgreSQL: defina SQLGUARD_PG_DSN ou rode com sudo sem senha (os testes de PostgreSQL serão pulados)"
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
