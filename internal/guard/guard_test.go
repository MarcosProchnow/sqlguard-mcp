package guard

import (
	"strings"
	"testing"
)

var dialetos = []struct {
	nome string
	d    Dialect
}{{"sqlite", SQLite}, {"postgres", Postgres}}

// Casos que valem igual para os dois dialetos.
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
		{"barra invertida em texto comum é literal", `SELECT 'a\' FROM t`, true, ""},
		{"parâmetro posicional", "SELECT * FROM t WHERE id = $1", true, ""},

		// palavras da lista usadas como nome de coluna, alias ou tabela
		{"coluna chamada call", "SELECT call, duracao FROM ligacoes", true, ""},
		{"coluna qualificada begin", "SELECT t.begin FROM turnos t", true, ""},
		{"coluna lock, copy, exec", "SELECT lock, copy, exec FROM portas", true, ""},
		{"coluna reset e notify", "SELECT reset, notify FROM canais", true, ""},
		{"alias com AS", "SELECT nome AS call FROM clientes", true, ""},
		{"ordenar por coluna lock", "SELECT id FROM portas ORDER BY lock", true, ""},
		{"filtrar por coluna set", "SELECT id FROM t WHERE set = 1 AND begin = 2", true, ""},
		{"coluna que começa como função proibida", "SELECT lo_total, pg_log_nivel FROM t", true, ""},
		{"EXPLAIN de um SELECT", "EXPLAIN SELECT * FROM clientes", true, ""},
		{"EXPLAIN QUERY PLAN", "EXPLAIN QUERY PLAN SELECT * FROM clientes", true, ""},
		{"VALUES", "VALUES (1), (2)", true, ""},
		{"TABLE", "TABLE clientes", true, ""},
		{"select entre parênteses", "(SELECT 1) UNION (SELECT 2)", true, ""},
		{"função comum com nome parecido", "SELECT pg_size_pretty(pg_database_size(current_database()))", true, ""},

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
		{"recarrega a configuração do servidor", "SELECT pg_reload_conf()", false, "pg_reload_conf"},
		{"zera estatísticas", "SELECT pg_stat_reset()", false, "pg_stat_reset"},
		{"manda notificação", "SELECT pg_notify('canal', 'x')", false, "pg_notify"},
		{"consome sequência", "SELECT nextval('s')", false, "nextval"},
		{"função entre aspas", `SELECT "pg_sleep"(10)`, false, "pg_sleep"},
		{"função entre aspas com schema", `SELECT pg_catalog."pg_reload_conf"()`, false, "pg_reload_conf"},
		{"função com schema", "SELECT pg_catalog.pg_sleep(10)", false, "pg_sleep"},
		{"função com espaço antes do parêntese", "SELECT pg_sleep (10)", false, "pg_sleep"},
		{"comando depois do CTE", "WITH x AS (SELECT 1) DELETE FROM clientes", false, "DELETE"},
		{"INSERT depois do CTE", "WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x", false, "INSERT"},
		{"CTE que escreve, com MATERIALIZED", "WITH x AS MATERIALIZED (UPDATE t SET a = 1 RETURNING *) SELECT * FROM x", false, "UPDATE"},
		{"FOR NO KEY UPDATE", "SELECT * FROM t FOR NO KEY UPDATE", false, "UPDATE"},
		{"comando entre parênteses", "(DELETE FROM clientes)", false, "SELECT e WITH"},
		{"EXPLAIN com opções", "EXPLAIN (ANALYZE) SELECT 1", false, "EXPLAIN"},
		{"EXPLAIN de um DELETE", "EXPLAIN DELETE FROM clientes", false, "EXPLAIN"},
		{"INTO depois da lista", "SELECT id INTO copia FROM clientes", false, "INTO"},
		{"texto entre $$ não é aceito", "SELECT $$;$$; DROP TABLE t", false, "não é suportado"},
		{"texto com tag $x$ não é aceito", "SELECT $x$a$x$", false, "não é suportado"},
		{"string E esconderia a segunda instrução", `SELECT E'\'' ; DELETE FROM t ; -- '`, false, "não é suportado"},
		{"vazia", "   ", false, "vazia"},
		{"só comentário", "-- nada", false, "vazia"},
		{"aspa aberta", "SELECT 'abc", false, "não terminado"},
		{"comentário aberto", "SELECT 1 /* abc", false, "não terminado"},
		{"pragma", "PRAGMA table_info(clientes)", false, "SELECT e WITH"},
		{"transação", "SELECT 1; COMMIT", false, "uma instrução"},
	}
	for _, dl := range dialetos {
		for _, tc := range cases {
			t.Run(dl.nome+"/"+tc.name, func(t *testing.T) {
				v := Check(tc.sql, dl.d)
				if v.Allowed != tc.allowed {
					t.Fatalf("Check(%q).Allowed = %v (motivo: %q), esperado %v", tc.sql, v.Allowed, v.Reason, tc.allowed)
				}
				if !tc.allowed && !strings.Contains(v.Reason, tc.reason) {
					t.Errorf("motivo = %q, esperado conter %q", v.Reason, tc.reason)
				}
			})
		}
	}
}

// No SQLite, [nome] é um identificador e um apóstrofo dentro dele não abre texto.
// Achado numa revisão: o guard aceitava esta consulta e o SQLite executava o DELETE.
func TestColchetesNoSQLite(t *testing.T) {
	ataque := "SELECT id AS [a'] FROM clientes WHERE id = 1 ; DELETE FROM clientes ; --'"
	if v := Check(ataque, SQLite); v.Allowed || !strings.Contains(v.Reason, "uma instrução") {
		t.Errorf("SQLite: o ataque deveria ser bloqueado por ter duas instruções: %+v", v)
	}
	for _, q := range []string{
		"SELECT [nome] FROM clientes",
		"SELECT id AS [meu apelido] FROM clientes",
		"SELECT [a;b] FROM t",
	} {
		if v := Check(q, SQLite); !v.Allowed {
			t.Errorf("SQLite deveria aceitar %q: %s", q, v.Reason)
		}
	}
	if v := Check("SELECT [load_extension]('x')", SQLite); v.Allowed || !strings.Contains(v.Reason, "load_extension") {
		t.Errorf("função entre colchetes deveria ser recusada: %+v", v)
	}
	if v := Check("SELECT [abc FROM t", SQLite); v.Allowed || !strings.Contains(v.Reason, "não terminado") {
		t.Errorf("colchete aberto deveria ser recusado: %+v", v)
	}
}

// No PostgreSQL os colchetes são índices de array e não delimitam identificador.
func TestColchetesNoPostgres(t *testing.T) {
	for _, q := range []string{
		"SELECT arr[1] FROM t",
		"SELECT dados['chave'] FROM t",
		"SELECT ARRAY[1, 2, 3]",
	} {
		if v := Check(q, Postgres); !v.Allowed {
			t.Errorf("PostgreSQL deveria aceitar %q: %s", q, v.Reason)
		}
	}
	if v := Check("SELECT arr['x; DROP TABLE t'] FROM t", Postgres); !v.Allowed {
		t.Errorf("texto com ; dentro de colchetes do PostgreSQL é só texto: %+v", v)
	}
}

func TestDialetoDesconhecido(t *testing.T) {
	if v := Check("SELECT 1", 0); v.Allowed {
		t.Error("sem dialeto o guard deve recusar")
	}
}

func TestCheckRemoveComentarioEPontoEVirgula(t *testing.T) {
	v := Check("SELECT 1 /* x */ ; ", SQLite)
	if !v.Allowed {
		t.Fatalf("deveria permitir: %s", v.Reason)
	}
	if strings.Contains(v.SQL, "/*") || strings.HasSuffix(v.SQL, ";") {
		t.Errorf("SQL normalizado ainda tem comentário ou ';': %q", v.SQL)
	}
}

func TestCheckPreservaTexto(t *testing.T) {
	v := Check("SELECT * FROM t WHERE a = 'x -- y'", Postgres)
	if !v.Allowed || !strings.Contains(v.SQL, "'x -- y'") {
		t.Errorf("texto entre aspas deveria ficar intacto: %+v", v)
	}
}

// FuzzCheck confere, para qualquer entrada que o guard aceite, que o que vai ao banco
// passa de novo pelo guard com o mesmo resultado, sem segunda instrução e sem violação.
func FuzzCheck(f *testing.F) {
	for _, seed := range []string{
		"SELECT 1", "SELECT 'a;b'", "WITH t AS (SELECT 1) SELECT * FROM t",
		"SELECT 1 -- c", "/* c */ SELECT 1", "DROP TABLE x", `SELECT "a" FROM t`,
		"SELECT 'it''s'", "SELECT 1; SELECT 2", "SELECT /* a /* b */ c */ 1",
		"SELECT [a'] FROM t", "SELECT arr['k'] FROM t", `SELECT "pg_sleep"(1)`,
		"SELECT call FROM t", "EXPLAIN SELECT 1", "WITH x AS (SELECT 1) DELETE FROM t",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, q string) {
		for _, dl := range dialetos {
			v := Check(q, dl.d)
			if !v.Allowed {
				continue
			}
			toks, _, err := mask(v.SQL, dl.d)
			if err != nil {
				t.Fatalf("[%s] SQL aceito não passa pelo mask: %q (%v)", dl.nome, v.SQL, err)
			}
			toks = trimSemis(toks)
			for _, tk := range toks {
				if tk.is(tPunct, ";") {
					t.Fatalf("[%s] SQL aceito tem ';' fora de texto: %q", dl.nome, v.SQL)
				}
			}
			if reason := violation(toks); reason != "" {
				t.Fatalf("[%s] SQL aceito tem violação (%s): %q", dl.nome, reason, v.SQL)
			}
			if !Check(v.SQL, dl.d).Allowed {
				t.Fatalf("[%s] guard não é idempotente para %q", dl.nome, v.SQL)
			}
		}
	})
}
