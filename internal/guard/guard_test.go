package guard

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		allowed bool
		reason  string // trecho esperado no motivo, quando bloqueada
	}{
		{"select simples", "SELECT * FROM clientes", true, ""},
		{"select com ponto e vírgula final", "SELECT 1;", true, ""},
		{"with cte", "WITH t AS (SELECT 1 AS n) SELECT n FROM t", true, ""},
		{"minúsculas", "select id from pedidos where status = 'pago'", true, ""},
		{"palavra proibida só dentro de texto", "SELECT * FROM logs WHERE msg = 'drop table x'", true, ""},
		{"palavra proibida em identificador entre aspas", `SELECT "update" FROM t`, true, ""},
		{"escape de aspa em texto", "SELECT 'it''s; drop table x'", true, ""},
		{"comentário removido do que vai ao banco", "SELECT 1 -- ; drop table x", true, ""},
		{"replace é função, não comando", "SELECT replace(nome, 'a', 'b') FROM clientes", true, ""},

		{"drop", "DROP TABLE clientes", false, "SELECT e WITH"},
		{"delete", "DELETE FROM clientes", false, "SELECT e WITH"},
		{"duas instruções", "SELECT 1; DROP TABLE clientes", false, "uma instrução"},
		{"cte que escreve", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", false, "DELETE"},
		{"select into cria tabela", "SELECT * INTO copia FROM clientes", false, "INTO"},
		{"for update trava linhas", "SELECT * FROM t FOR UPDATE", false, "UPDATE"},
		{"explain analyze executa", "EXPLAIN ANALYZE SELECT 1", false, "SELECT e WITH"},
		{"comando escondido atrás de comentário", "/* inofensivo */ DELETE FROM t", false, "DELETE"},
		{"comentário não esconde a segunda instrução", "SELECT 1; /* x */ DROP TABLE t", false, "uma instrução"},
		{"função que dorme", "SELECT pg_sleep(30)", false, "pg_sleep"},
		{"função que lê arquivo", "SELECT pg_read_file('/etc/passwd')", false, "pg_read_file"},
		{"altera configuração", "SELECT set_config('x', 'y', false)", false, "set_config"},
		{"texto entre $$ não é aceito", "SELECT $$;$$; DROP TABLE t", false, "não é suportado"},
		{"texto com tag $x$ não é aceito", "SELECT $x$a$x$", false, "não é suportado"},
		{"string E esconderia a segunda instrução", `SELECT E'\'' ; DELETE FROM t ; -- '`, false, "não é suportado"},
		{"barra invertida em texto comum é literal", `SELECT 'a\' FROM t`, true, ""},
		{"parâmetro posicional", "SELECT * FROM t WHERE id = $1", true, ""},
		{"vazia", "   ", false, "vazia"},
		{"só comentário", "-- nada", false, "vazia"},
		{"aspa aberta", "SELECT 'abc", false, "não terminado"},
		{"comentário aberto", "SELECT 1 /* abc", false, "não terminado"},
		{"pragma", "PRAGMA table_info(clientes)", false, "SELECT e WITH"},
		{"transação", "SELECT 1; COMMIT", false, "uma instrução"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Check(tc.sql)
			if v.Allowed != tc.allowed {
				t.Fatalf("Check(%q).Allowed = %v (motivo: %q), esperado %v", tc.sql, v.Allowed, v.Reason, tc.allowed)
			}
			if !tc.allowed && !strings.Contains(v.Reason, tc.reason) {
				t.Errorf("motivo = %q, esperado conter %q", v.Reason, tc.reason)
			}
		})
	}
}

func TestCheckRemoveComentarioEPontoEVirgula(t *testing.T) {
	v := Check("SELECT 1 /* x */ ; ")
	if !v.Allowed {
		t.Fatalf("deveria permitir: %s", v.Reason)
	}
	if strings.Contains(v.SQL, "/*") || strings.HasSuffix(v.SQL, ";") {
		t.Errorf("SQL normalizado ainda tem comentário ou ';': %q", v.SQL)
	}
}

func TestCheckPreservaTexto(t *testing.T) {
	v := Check("SELECT * FROM t WHERE a = 'x -- y'")
	if !v.Allowed || !strings.Contains(v.SQL, "'x -- y'") {
		t.Errorf("texto entre aspas deveria ficar intacto: %+v", v)
	}
}

// FuzzCheck confere duas propriedades para qualquer entrada que o guard aceite:
// o que vai ao banco não tem comando proibido nem segunda instrução, e passar de
// novo pelo guard dá o mesmo resultado.
func FuzzCheck(f *testing.F) {
	for _, seed := range []string{
		"SELECT 1", "SELECT 'a;b'", "WITH t AS (SELECT 1) SELECT * FROM t",
		"SELECT 1 -- c", "/* c */ SELECT 1", "DROP TABLE x", `SELECT "a" FROM t`,
		"SELECT 'it''s'", "SELECT 1; SELECT 2", "SELECT /* a /* b */ c */ 1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, q string) {
		v := Check(q)
		if !v.Allowed {
			return
		}
		scan, _, err := mask(v.SQL)
		if err != nil {
			t.Fatalf("SQL aceito não passa pelo mask: %q (%v)", v.SQL, err)
		}
		scan = trimStmt(scan)
		if strings.Contains(scan, ";") {
			t.Fatalf("SQL aceito tem ';' fora de texto: %q", v.SQL)
		}
		words := wordRe.FindAllString(strings.ToLower(scan), -1)
		if len(words) == 0 || (words[0] != "select" && words[0] != "with") {
			t.Fatalf("SQL aceito não começa com SELECT/WITH: %q", v.SQL)
		}
		for _, w := range words {
			if deniedWords[w] {
				t.Fatalf("SQL aceito contém %q: %q", w, v.SQL)
			}
		}
		if !Check(v.SQL).Allowed {
			t.Fatalf("guard não é idempotente para %q", v.SQL)
		}
	})
}
