package sqltok

import (
	"strings"
	"testing"
)

func stmts(dialect, sql string) []string {
	var out []string
	for _, r := range Split(dialect, sql) {
		out = append(out, sql[r.Start:r.End])
	}
	return out
}

func TestSplit(t *testing.T) {
	cases := []struct {
		dialect, sql string
		want         []string
	}{
		{MySQL, "SELECT 5--1 AS a; SELECT 2 AS b", []string{"SELECT 5--1 AS a", "SELECT 2 AS b"}},
		{MySQL, "SELECT 1 -- c; x\n; SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{MySQL, "SELECT 'a;b\\';c'; SELECT `x;y` FROM t", []string{"SELECT 'a;b\\';c'", "SELECT `x;y` FROM t"}},
		{MySQL, "SELECT 1 # c;\n; SELECT \"q;\"", []string{"SELECT 1", "SELECT \"q;\""}},
		{Postgres, `SELECT E'it\'s; fine' AS s; SELECT 2`, []string{`SELECT E'it\'s; fine' AS s`, "SELECT 2"}},
		{Postgres, "SELECT $$a;b$$; SELECT $f$x;$$;y$f$", []string{"SELECT $$a;b$$", "SELECT $f$x;$$;y$f$"}},
		{Postgres, "SELECT 'it''s;' /* a; /* nested; */ b; */ ; SELECT 1", []string{"SELECT 'it''s;'", "SELECT 1"}},
		{Postgres, "SELECT 'a\\'; SELECT 2", []string{"SELECT 'a\\'", "SELECT 2"}},
		{SQLite, "CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE b SET x = 1; DELETE FROM c; END; SELECT 1",
			[]string{"CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE b SET x = 1; DELETE FROM c; END", "SELECT 1"}},
		{SQLite, "-- only a comment\n;  ; SELECT [a;b] FROM t", []string{"SELECT [a;b] FROM t"}},
	}
	for _, c := range cases {
		got := stmts(c.dialect, c.sql)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s %q:\n got  %q\n want %q", c.dialect, c.sql, got, c.want)
		}
	}
}

func classify(dialect, sql string) Class {
	r := Split(dialect, sql)
	if len(r) != 1 {
		return Classify(dialect, Tokenize(dialect, sql).Tokens)
	}
	return Classify(dialect, r[0].Tokens)
}

func TestReadOnlyClassifier(t *testing.T) {
	allowed := map[string][]string{
		Postgres: {
			"SELECT * FROM sales_order WHERE status = 'update' -- delete",
			"select \"comment\" from t",
			"WITH x AS (SELECT 1) SELECT * FROM x",
			"(SELECT 1) UNION (SELECT 2)",
			"EXPLAIN (FORMAT JSON) SELECT 1",
			"SHOW search_path",
			"VALUES (1), (2)",
			"TABLE store",
			"SELECT replace(name, 'a', 'b') FROM t",
			"SELECT CAST(x AS varchar) FROM t",
			"SELECT $$delete from x$$",
		},
		MySQL: {
			"SELECT * FROM t WHERE a = 'set_config('",
			"SHOW CREATE TABLE sales_order",
			"SHOW GLOBAL STATUS",
			"DESCRIBE sales_order",
			"SELECT CONVERT(x USING utf8mb4), CAST(y AS CHAR CHARACTER SET utf8mb4) FROM t",
			"SELECT `update` FROM t",
			"SELECT start, load FROM t",
		},
		SQLite: {
			"PRAGMA table_info(store)",
			"PRAGMA main.index_list('store')",
			"PRAGMA user_version",
			"SELECT [delete] FROM t",
		},
	}
	refused := map[string][]string{
		Postgres: {
			"BEGIN READ  WRITE",
			"START TRANSACTION ISOLATION LEVEL READ COMMITTED, READ\tWRITE",
			"WITH d AS (DELETE FROM t WHERE id = 1 RETURNING id) SELECT * FROM d",
			"WITH x AS (SELECT 1) DELETE FROM t",
			"SELECT set_config/**/('default_transaction_read_only','off',false)",
			`SELECT "set_config"('default_transaction_read_only','off',false)`,
			"SELECT pg_catalog.set_config('a','b',false)",
			`SELECT "pg_cancel_backend"(0)`,
			"SELECT pg_terminate_backend(123)",
			"SELECT lo_import('/etc/passwd')",
			"SELECT dblink_exec('x','y')",
			"SELECT pg_advisory_lock(1)",
			"SELECT nextval('s')",
			"SELECT 1 AS x INTO audit_ro2",
			"SELECT query_to_xml('delete from t', true, false, '')",
			"RESET ALL",
			"DISCARD ALL -- temp",
			"SET search_path TO x",
			"EXPLAIN ANALYZE DELETE FROM t",
			"EXPLAIN ANALYZE SELECT 1",
			"SELECT * FROM t FOR UPDATE",
			"COPY t TO '/tmp/x'",
			"DO $$ BEGIN END $$",
			"SELECT 1; DELETE FROM t",
			"CALL p()",
			"UPDATE t SET a = 1",
			"LOCK TABLE t",
		},
		MySQL: {
			"SET @a = 1, SESSION transaction_read_only = 0",
			"START TRANSACTION READ  WRITE",
			"WITH x AS (SELECT 1) DELETE FROM t WHERE id = 2",
			"/*!80000 UPDATE t SET v = 'hacked' WHERE id IN ( */ SELECT 3 /*! ) */",
			"SELECT 1 /*!50000 INTO OUTFILE '/tmp/x' */",
			"SELECT * FROM t ORDER BY id LIMIT 3 INTO/**/OUTFILE '/tmp/x'",
			"RESET PERSIST /* temp */",
			"SELECT /*+ SET_VAR(sort_buffer_size = 16M) */ 1",
			"SELECT * FROM t LOCK IN SHARE MODE",
			"SELECT GET_LOCK('x', 1)",
			"SELECT LOAD_FILE('/etc/passwd')",
			"HANDLER t OPEN",
			"LOAD DATA INFILE 'x' INTO TABLE t",
			"REPLACE INTO t VALUES (1)",
			"KILL 12",
			"FLUSH TABLES",
			"USE other",
		},
		SQLite: {
			"PRAGMA query_only/**/=0",
			"PRAGMA journal_mode/**/=DELETE",
			"PRAGMA user_version(7)",
			"PRAGMA writable_schema = 1",
			"ATTACH 'x.db' AS x",
			"WITH x AS (SELECT 1) INSERT INTO store (code) SELECT 'zz' FROM x",
			"SELECT load_extension('x')",
			"VACUUM",
		},
	}
	for d, list := range allowed {
		for _, sql := range list {
			if c := classify(d, sql); c.ReadOnly != "" {
				t.Errorf("%s: %q refused: %s", d, sql, c.ReadOnly)
			}
		}
	}
	for d, list := range refused {
		for _, sql := range list {
			if c := classify(d, sql); c.ReadOnly == "" {
				t.Errorf("%s: %q allowed", d, sql)
			}
		}
	}
}

func TestReturnsRows(t *testing.T) {
	cases := map[string]bool{
		"UPDATE t SET v = 'returning customer'":         false,
		"UPDATE t SET v = 1 RETURNING id":               true,
		"WITH x AS (SELECT 1) UPDATE t SET v = 1":       false,
		"WITH x AS (SELECT 1) SELECT * FROM x":          true,
		"ANALYZE TABLE t":                               true,
		"FETCH ALL FROM cur":                            true,
		"/* c */ SELECT 1":                              true,
		"(SELECT 1)":                                    true,
		"INSERT INTO t VALUES (1)":                      false,
		"CHECK TABLE t":                                 true,
		"EXECUTE stmt":                                  true,
		"-- x\nSHOW TABLES":                             true,
		"DELETE FROM t WHERE note = 'select returning'": false,
	}
	for sql, want := range cases {
		if got := classify(MySQL, sql).ReturnsRows; got != want {
			t.Errorf("%q: returnsRows %v, want %v", sql, got, want)
		}
	}
}

func TestFragment(t *testing.T) {
	bad := []string{
		"(SELECT set_config/**/('default_transaction_read_only','off',false)) IS NOT NULL",
		"id LIMIT 3 INTO/**/OUTFILE '/x' -- ",
		"nextval('s') > 0",
		"1=1; DELETE FROM t",
		"id = 1 /* unterminated",
	}
	for _, f := range bad {
		if CheckFragment(Postgres, f) == "" {
			t.Errorf("fragment %q allowed", f)
		}
	}
	for _, f := range []string{"status = 'delete' AND id > 3", "created_at DESC, id", "name LIKE '%;%'"} {
		if r := CheckFragment(Postgres, f); r != "" {
			t.Errorf("fragment %q refused: %s", f, r)
		}
	}
}
