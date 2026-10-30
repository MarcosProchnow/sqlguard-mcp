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
		{"texto com $ não esconde nada", "SELECT $$;$$; DROP TABLE t", false, "uma instrução"},
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
