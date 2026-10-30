// Package guard decide se uma consulta SQL pode ser executada em modo somente leitura.
//
// É a primeira de duas barreiras: o guard recusa a consulta antes de ela chegar ao
// banco, e o banco ainda abre a conexão somente leitura (ver package store).
package guard

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Verdict é o resultado da análise de uma consulta.
type Verdict struct {
	Allowed bool
	Reason  string
	// SQL é a instrução pronta para executar: sem comentários e sem ';' final.
	SQL string
}

var deniedWords = toSet(
	"insert", "update", "delete", "drop", "alter", "create", "truncate",
	"grant", "revoke", "merge", "call", "exec", "execute", "copy",
	"attach", "detach", "pragma", "vacuum", "reindex", "analyze",
	"into", "lock", "set", "reset", "do", "listen", "notify",
	"begin", "commit", "rollback", "savepoint",
)

// Funções que leem arquivos, dormem, travam ou mudam estado, mesmo dentro de um SELECT.
var deniedPrefixes = []string{
	"pg_sleep", "pg_read", "pg_ls", "pg_advisory", "pg_terminate", "pg_cancel",
	"lo_", "dblink", "set_config", "load_extension", "readfile", "writefile",
}

var wordRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_$]*`)

// Check analisa a consulta e devolve o veredito.
func Check(query string) Verdict {
	scan, exec, err := mask(query)
	if err != nil {
		return deny(err.Error())
	}
	scan, exec = trimStmt(scan), trimStmt(exec)
	if scan == "" {
		return deny("consulta vazia")
	}
	if strings.Contains(scan, ";") {
		return deny("apenas uma instrução por consulta")
	}

	words := wordRe.FindAllString(strings.ToLower(scan), -1)
	if len(words) == 0 {
		return deny("consulta sem comando")
	}
	if first := words[0]; first != "select" && first != "with" {
		return deny(fmt.Sprintf("só SELECT e WITH são permitidos (recebido: %s)", strings.ToUpper(first)))
	}
	for _, w := range words {
		if deniedWords[w] {
			return deny(fmt.Sprintf("palavra proibida: %s", strings.ToUpper(w)))
		}
		for _, p := range deniedPrefixes {
			if strings.HasPrefix(w, p) {
				return deny(fmt.Sprintf("função proibida: %s", w))
			}
		}
	}
	return Verdict{Allowed: true, SQL: exec}
}

func deny(reason string) Verdict { return Verdict{Reason: reason} }

func trimStmt(s string) string {
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), "; \t\r\n"))
}

func toSet(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

// mask percorre a consulta uma vez e devolve duas versões:
//   - scan: sem comentários, e com strings e identificadores entre aspas esvaziados,
//     para procurar palavras sem cair em falso positivo ("drop" dentro de um texto);
//   - exec: sem comentários, com strings intactas, que é o que vai ao banco.
func mask(sql string) (scan, exec string, err error) {
	var s, e strings.Builder
	n := len(sql)
	for i := 0; i < n; {
		c := sql[i]
		switch {
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
			s.WriteByte(' ')
			e.WriteByte(' ')

		case c == '/' && i+1 < n && sql[i+1] == '*':
			depth := 1
			i += 2
			for i < n && depth > 0 {
				switch {
				case sql[i] == '/' && i+1 < n && sql[i+1] == '*':
					depth++
					i += 2
				case sql[i] == '*' && i+1 < n && sql[i+1] == '/':
					depth--
					i += 2
				default:
					i++
				}
			}
			if depth > 0 {
				return "", "", errors.New("comentário de bloco não terminado")
			}
			s.WriteByte(' ')
			e.WriteByte(' ')

		case c == '\'':
			// E'...' trata a barra invertida como escape no PostgreSQL e como texto comum no
			// SQLite. Cada banco leria os limites do texto de um jeito, então não aceitamos.
			if i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E') && (i < 2 || !isWord(sql[i-2])) {
				return "", "", errors.New("texto com prefixo E (escape) não é suportado")
			}
			j, ok := skipQuoted(sql, i, '\'')
			if !ok {
				return "", "", errors.New("texto entre aspas não terminado")
			}
			e.WriteString(sql[i:j])
			s.WriteString("''")
			i = j

		case c == '"' || c == '`':
			j, ok := skipQuoted(sql, i, c)
			if !ok {
				return "", "", errors.New("identificador entre aspas não terminado")
			}
			e.WriteString(sql[i:j])
			s.WriteString(`""`)
			i = j

		case c == '$' && (i == 0 || !isWord(sql[i-1])):
			// $$...$$ só existe no PostgreSQL; em outros bancos o conteúdo seria código.
			if dollarRe.MatchString(sql[i:]) {
				return "", "", errors.New("texto entre $$ não é suportado")
			}
			s.WriteByte(c)
			e.WriteByte(c)
			i++

		default:
			s.WriteByte(c)
			e.WriteByte(c)
			i++
		}
	}
	return s.String(), e.String(), nil
}

var dollarRe = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)?\$`)

func isWord(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// skipQuoted devolve o índice logo após o fechamento da aspa aberta em sql[i].
// A aspa dobrada (” ou "") é uma aspa literal; a barra invertida não é escape.
func skipQuoted(sql string, i int, q byte) (int, bool) {
	n := len(sql)
	for j := i + 1; j < n; j++ {
		if sql[j] != q {
			continue
		}
		if j+1 < n && sql[j+1] == q {
			j++
			continue
		}
		return j + 1, true
	}
	return 0, false
}
