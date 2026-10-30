#!/usr/bin/env bash
# Depois de cada edição em arquivo .go: formata e roda a análise estática.
# Se algo falhar, o Claude vê a mensagem e corrige na hora.
file=$(jq -r '.tool_input.file_path // empty' 2>/dev/null)
case "$file" in
  *.go)
    gofmt -w "$file" || exit 2
    go vet ./... >&2 || exit 2
    ;;
esac
