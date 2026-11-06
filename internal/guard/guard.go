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

// Dialect diz qual SQL o banco fala. O guard precisa saber, porque cada banco lê o
// texto de um jeito: no SQLite, [nome] é um identificador; no PostgreSQL, colchetes
// são índices de array.
type Dialect int

const (
	SQLite Dialect = iota + 1
	Postgres
)

// Verdict é o resultado da análise de uma consulta.
type Verdict struct {
	Allowed bool
	Reason  string
	// SQL é a instrução pronta para executar: sem comentários e sem ';' final.
	SQL string
}

// Comandos que escrevem, travam ou mudam o estado da sessão. Uma palavra desta lista só
// passa quando está em posição de identificador (ver identifierContext).
var deniedWords = toSet(
	"insert", "update", "delete", "drop", "alter", "create", "truncate",
	"grant", "revoke", "merge", "call", "exec", "execute", "copy",
	"attach", "detach", "pragma", "vacuum", "reindex", "analyze",
	"into", "lock", "set", "reset", "do", "listen", "notify",
	"begin", "commit", "rollback", "savepoint",
)

// Funções que leem arquivos, dormem, travam ou mudam estado, mesmo dentro de um SELECT.
// Só valem quando o nome é seguido de '(', e valem também para nomes entre aspas.
var deniedPrefixes = []string{
	"pg_sleep", "pg_read", "pg_ls", "pg_advisory", "pg_try_advisory", "pg_terminate", "pg_cancel",
	"lo_", "dblink", "set_config", "load_extension", "readfile", "writefile",
	// funções administrativas que rodam dentro de um SELECT, mesmo numa transação READ ONLY
	"pg_reload", "pg_notify", "pg_stat_reset", "pg_stat_statements_reset", "pg_rotate",
	"pg_switch", "pg_create", "pg_drop", "pg_promote", "pg_wal_replay", "pg_backup",
	"pg_replication", "pg_logical", "pg_import", "pg_export", "pg_file", "pg_log",
	"nextval", "setval",
}

// Primeiras palavras aceitas. VALUES e TABLE só devolvem linhas.
var startWords = toSet("select", "with", "values", "table")

// identifierContext: o que vem antes de uma palavra proibida para ela ser só um nome de
// coluna, alias ou tabela (ex.: SELECT call FROM ligacoes, t.begin, ORDER BY lock).
// Nenhum comando começa depois dessas palavras; um comando só aparece no início, depois
// de '(' ou ')' (corpo de CTE) ou depois de FOR/KEY (cláusulas de trava).
var identifierContext = toSet(
	".", ",", "as", "select", "distinct", "by", "where", "and", "or", "not",
	"on", "from", "join", "when", "then", "else", "case",
)

type tokKind int

const (
	tWord   tokKind = iota + 1 // palavra sem aspas, em minúsculas
	tQuoted                    // identificador entre aspas ("x", `x`, [x]); text é o conteúdo
	tString                    // texto entre apóstrofos; o conteúdo não importa
	tNumber
	tPunct // um caractere de pontuação
)

type token struct {
	kind tokKind
	text string
}

// context é o que o token vale como "palavra anterior" para identifierContext.
func (t token) context() string {
	switch t.kind {
	case tWord, tPunct:
		return t.text
	}
	return "\x00" // texto, número e identificador entre aspas não liberam nada
}

func (t token) is(kind tokKind, text string) bool { return t.kind == kind && t.text == text }

// Check analisa a consulta, lida como SQL do dialeto d, e devolve o veredito.
func Check(query string, d Dialect) Verdict {
	if d != SQLite && d != Postgres {
		return deny("dialeto desconhecido")
	}
	toks, exec, err := mask(query, d)
	if err != nil {
		return deny(err.Error())
	}
	toks, exec = trimSemis(toks), trimStmt(exec)
	if len(toks) == 0 {
		return deny("consulta vazia")
	}
	for _, t := range toks {
		if t.is(tPunct, ";") {
			return deny("apenas uma instrução por consulta")
		}
	}
	if reason := violation(toks); reason != "" {
		return deny(reason)
	}
	return Verdict{Allowed: true, SQL: exec}
}

// violation devolve o motivo de recusa de uma lista de tokens sem ';', ou "" se estiver ok.
func violation(toks []token) string {
	// o primeiro comando: pula parênteses de abertura, como em (SELECT 1) UNION (SELECT 2)
	i := 0
	for i < len(toks) && toks[i].is(tPunct, "(") {
		i++
	}
	if i == len(toks) || toks[i].kind != tWord {
		return "consulta sem comando"
	}
	first := toks[i].text
	switch {
	case first == "explain":
		// EXPLAIN não executa a consulta; EXPLAIN ANALYZE executa, e (opções) não é aceito.
		if !explainOK(toks[i+1:]) {
			return "só SELECT e WITH são permitidos (recebido: EXPLAIN, aceito só EXPLAIN [QUERY PLAN] SELECT)"
		}
	case !startWords[first]:
		return fmt.Sprintf("só SELECT e WITH são permitidos (recebido: %s)", strings.ToUpper(first))
	}

	for k, t := range toks {
		prev := ""
		if k > 0 {
			prev = toks[k-1].context()
		}
		call := k+1 < len(toks) && toks[k+1].is(tPunct, "(")
		switch t.kind {
		case tWord:
			if deniedWords[t.text] && !identifierContext[prev] {
				return fmt.Sprintf("palavra proibida: %s", strings.ToUpper(t.text))
			}
			if call && deniedFunction(t.text) {
				return fmt.Sprintf("função proibida: %s", t.text)
			}
		case tQuoted:
			// "pg_sleep"(10) é a mesma chamada de pg_sleep(10)
			if call && deniedFunction(t.text) {
				return fmt.Sprintf("função proibida: %s", t.text)
			}
		}
	}
	return ""
}

func explainOK(rest []token) bool {
	if len(rest) >= 2 && rest[0].is(tWord, "query") && rest[1].is(tWord, "plan") {
		rest = rest[2:]
	}
	return len(rest) > 0 && rest[0].kind == tWord && startWords[rest[0].text]
}

func deniedFunction(name string) bool {
	for _, p := range deniedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func deny(reason string) Verdict { return Verdict{Reason: reason} }

func trimSemis(toks []token) []token {
	for len(toks) > 0 && toks[len(toks)-1].is(tPunct, ";") {
		toks = toks[:len(toks)-1]
	}
	return toks
}

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

// mask percorre a consulta uma vez e devolve:
//   - os tokens, sem comentários e com o conteúdo dos textos descartado, para o guard
//     decidir sem cair em falso positivo ("drop" dentro de um texto);
//   - exec: a consulta sem comentários e com os textos intactos, que é o que vai ao banco.
//
// O que vai ao banco é exatamente o que foi analisado: os comentários, que cada banco
// lê de um jeito, nunca chegam a ele.
func mask(sql string, d Dialect) (toks []token, exec string, err error) {
	var e strings.Builder
	n := len(sql)
	for i := 0; i < n; {
		c := sql[i]
		switch {
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
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
				return nil, "", errors.New("comentário de bloco não terminado")
			}
			e.WriteByte(' ')

		case c == '\'':
			// E'...' trata a barra invertida como escape no PostgreSQL e como texto comum no
			// SQLite. Cada banco leria os limites do texto de um jeito, então não aceitamos.
			if i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E') && (i < 2 || !isWord(sql[i-2])) {
				return nil, "", errors.New("texto com prefixo E (escape) não é suportado")
			}
			j, ok := skipQuoted(sql, i, '\'')
			if !ok {
				return nil, "", errors.New("texto entre aspas não terminado")
			}
			e.WriteString(sql[i:j])
			toks = append(toks, token{tString, ""})
			i = j

		case c == '"' || c == '`':
			j, ok := skipQuoted(sql, i, c)
			if !ok {
				return nil, "", errors.New("identificador entre aspas não terminado")
			}
			e.WriteString(sql[i:j])
			name := strings.ReplaceAll(sql[i+1:j-1], string(c)+string(c), string(c))
			toks = append(toks, token{tQuoted, strings.ToLower(name)})
			i = j

		case c == '[' && d == SQLite:
			// No SQLite, [nome] é um identificador sem escape. Sem tratar isso, um apóstrofo
			// dentro dos colchetes faz o guard ver um texto onde o banco vê código.
			j := strings.IndexByte(sql[i:], ']')
			if j < 0 {
				return nil, "", errors.New("identificador entre colchetes não terminado")
			}
			e.WriteString(sql[i : i+j+1])
			toks = append(toks, token{tQuoted, strings.ToLower(sql[i+1 : i+j])})
			i += j + 1

		case c == '$' && (i == 0 || !isWord(sql[i-1])) && dollarRe.MatchString(sql[i:]):
			// $$...$$ só existe no PostgreSQL; em outros bancos o conteúdo seria código.
			return nil, "", errors.New("texto entre $$ não é suportado")

		case isWordStart(c):
			j := i + 1
			for j < n && (isWord(sql[j]) || sql[j] == '$') {
				j++
			}
			e.WriteString(sql[i:j])
			toks = append(toks, token{tWord, strings.ToLower(sql[i:j])})
			i = j

		case c >= '0' && c <= '9':
			j := i + 1
			for j < n && isWord(sql[j]) {
				j++
			}
			e.WriteString(sql[i:j])
			toks = append(toks, token{tNumber, sql[i:j]})
			i = j

		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			e.WriteByte(c)
			i++

		default:
			e.WriteByte(c)
			toks = append(toks, token{tPunct, string(c)})
			i++
		}
	}
	return toks, e.String(), nil
}

var dollarRe = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)?\$`)

func isWordStart(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func isWord(b byte) bool {
	return isWordStart(b) || b >= '0' && b <= '9'
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
