package sqltok

import "testing"

func inspect(dialect, sql string) Info {
	r := Split(dialect, sql)
	if len(r) != 1 {
		return Inspect(dialect, Tokenize(dialect, sql).Tokens)
	}
	return Inspect(dialect, r[0].Tokens)
}

func TestInspect(t *testing.T) {
	cases := []struct {
		dialect, sql  string
		writes        bool
		table         string
		noWhere       bool
		schema, tname string
	}{
		{SQLite, "SELECT * FROM sales_order o JOIN customer c ON c.id = o.customer_id", false, "sales_order", false, "", ""},
		{SQLite, "select 1", false, "", false, "", ""},
		{Postgres, `SELECT * FROM "Sales"."OrderLine" WHERE x = 1`, false, "OrderLine", false, "", ""},
		{Postgres, "SELECT * FROM (SELECT * FROM inner_t) x", false, "inner_t", false, "", ""},
		{Postgres, "SELECT * FROM generate_series(1, 3) g JOIN items i ON true", false, "items", false, "", ""},
		{Postgres, "SELECT EXTRACT(YEAR FROM created) FROM orders", false, "orders", false, "", ""},
		{Postgres, "WITH x AS (SELECT * FROM sales) SELECT * FROM x", false, "sales", false, "", ""},
		{Postgres, "SELECT * FROM t FOR UPDATE", false, "t", false, "", ""},
		{Postgres, "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", true, "t", false, "", ""},
		{Postgres, "EXPLAIN ANALYZE DELETE FROM t", true, "t", false, "", ""},
		{Postgres, "EXPLAIN SELECT * FROM t", false, "t", false, "", ""},
		{MySQL, "SHOW CREATE TABLE `orders`", false, "orders", false, "", ""},
		{MySQL, "SHOW TABLES", false, "", false, "", ""},
		{SQLite, "INSERT INTO store (name) VALUES ('x')", true, "store", false, "", ""},
		{SQLite, "CREATE TABLE IF NOT EXISTS t2 (id int)", true, "t2", false, "", ""},
		{SQLite, "DROP TABLE t2", true, "t2", false, "", ""},
		{SQLite, "TRUNCATE t2", true, "t2", false, "", ""},
		{SQLite, "BEGIN", false, "", false, "", ""},
		{MySQL, "SET @a = 1", false, "", false, "", ""},
		{MySQL, "USE shop", false, "", false, "", ""},
		// UPDATE / DELETE without WHERE
		{SQLite, "UPDATE store SET name = name WHERE 1 = 0", true, "store", false, "", ""},
		{SQLite, "UPDATE store SET name = name", true, "store", true, "", "store"},
		{SQLite, "UPDATE OR REPLACE store AS s SET name = name", true, "store", true, "", "store"},
		{SQLite, "DELETE FROM store", true, "store", true, "", "store"},
		{SQLite, "DELETE FROM main.store", true, "store", true, "main", "store"},
		{Postgres, `DELETE FROM Shop."Store" s RETURNING *`, true, "Store", true, "shop", "Store"},
		{Postgres, "DELETE FROM ONLY store", true, "store", true, "", ""},
		{Postgres, "DELETE FROM store USING other", true, "store", true, "", ""},
		{Postgres, "UPDATE store SET a = o.a FROM other o", true, "store", true, "", ""},
		{Postgres, "UPDATE store SET a = (SELECT max(a) FROM other WHERE x = 1)", true, "store", true, "", "store"},
		{Postgres, "WITH x AS (SELECT 1) DELETE FROM store", true, "store", true, "", ""},
		{MySQL, "DELETE FROM store LIMIT 10", true, "store", false, "", ""},
		{MySQL, "UPDATE a JOIN b ON a.id = b.id SET a.x = b.x", true, "a", false, "", ""},
		{MySQL, "UPDATE a, b SET a.x = b.x", true, "a", true, "", ""},
		{MySQL, "DELETE a FROM a JOIN b ON a.id = b.id", true, "a", false, "", ""},
		{MySQL, "DELETE LOW_PRIORITY FROM `Store`", true, "Store", true, "", "Store"},
		{MySQL, "UPDATE t SET `where` = 1", true, "t", true, "", "t"},
		// a JOIN in FROM / USING is cross joined with the target: still every row
		{Postgres, "UPDATE store SET a = o.a FROM other o JOIN x ON x.id = o.id", true, "store", true, "", ""},
		{Postgres, "DELETE FROM store USING other o JOIN x ON x.id = o.id", true, "store", true, "", ""},
		{SQLite, "UPDATE store SET a = 1 FROM a JOIN b ON a.id = b.id", true, "store", true, "", ""},
		{Postgres, "UPDATE store SET a = o.a FROM other o JOIN x ON x.id = o.id WHERE o.id = store.id", true, "store", false, "", ""},
		// MySQL: an outer / cross join of the target does not narrow it
		{MySQL, "UPDATE a LEFT JOIN b ON a.id = b.id SET a.x = 1", true, "a", true, "", ""},
		{MySQL, "DELETE a FROM a LEFT OUTER JOIN b ON a.id = b.id", true, "a", true, "", ""},
		{MySQL, "UPDATE a CROSS JOIN b SET a.x = b.x", true, "a", true, "", ""},
		{MySQL, "UPDATE a INNER JOIN b ON a.id = b.id SET a.x = b.x", true, "a", false, "", ""},
		// writes hidden in reads, maintenance, pragmas
		{Postgres, "SELECT setval('seq', 1)", true, "", false, "", ""},
		{Postgres, "SELECT dblink_exec('dbname=x', 'DELETE FROM t')", true, "", false, "", ""},
		{Postgres, "SELECT count(*) FROM t", false, "t", false, "", ""},
		{Postgres, "EXPLAIN ANALYZE SELECT setval('seq', 1)", true, "", false, "", ""},
		{Postgres, "EXPLAIN SELECT setval('seq', 1)", false, "", false, "", ""},
		{Postgres, "WITH x AS (SELECT 1) SELECT * INTO t2 FROM x", true, "t2", false, "", ""},
		{Postgres, "VACUUM FULL t", true, "", false, "", ""},
		{Postgres, "REINDEX TABLE t", true, "t", false, "", ""},
		{MySQL, "OPTIMIZE TABLE t", true, "t", false, "", ""},
		{SQLite, "PRAGMA user_version = 3", true, "", false, "", ""},
		{SQLite, "PRAGMA table_info(t)", false, "", false, "", ""},
	}
	for _, c := range cases {
		in := inspect(c.dialect, c.sql)
		if in.Writes != c.writes || in.Table != c.table || in.NoWhere != c.noWhere || in.TargetSchema != c.schema || in.TargetTable != c.tname {
			t.Errorf("%s %q: got writes=%v table=%q noWhere=%v target=%q.%q, want %v %q %v %q.%q",
				c.dialect, c.sql, in.Writes, in.Table, in.NoWhere, in.TargetSchema, in.TargetTable, c.writes, c.table, c.noWhere, c.schema, c.tname)
		}
	}
}
