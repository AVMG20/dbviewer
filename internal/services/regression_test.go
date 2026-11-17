package services

// Regression tests for the backend audit (audit/backend.md). Ported from the
// auditor's probes, with assertions. They only touch scratch tables named
// regr_* in the dummy databases (dropped afterwards) and temporary SQLite copies.
// No server-global statements (SET GLOBAL, PERSIST, RESET PERSIST, FLUSH,
// KILL of other sessions, CREATE/DROP USER).

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dbviewer/internal/envimport"
	"dbviewer/internal/model"
	"dbviewer/internal/query"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func engineByName(t *testing.T, name string) engine {
	for _, e := range engines(t) {
		if e.name == name {
			if e.cfg.Socket != "" {
				if _, err := os.Stat(e.cfg.Socket); err != nil {
					t.Skip("mysql socket not available")
				}
			}
			return e
		}
	}
	t.Fatal("no engine " + name)
	return engine{}
}

var qseq atomic.Int64

func (f *fixture) run(connID, session, sql string) []model.QueryResult {
	f.t.Helper()
	r, err := f.qs.RunQuery(f.ctx, model.QueryRequest{ConnID: connID, QueryID: "rq" + strconv.FormatInt(qseq.Add(1), 10), SQL: sql, SessionID: session})
	if err != nil {
		f.t.Fatalf("RunQuery %q: %v", sql, err)
	}
	return r
}

func (f *fixture) scalar(connID, sql string) string {
	f.t.Helper()
	p, err := f.core.Conns.Get(f.ctx, connID)
	if err != nil {
		f.t.Fatal(err)
	}
	var v any
	if err := p.DB.QueryRowContext(f.ctx, sql).Scan(&v); err != nil {
		return "ERR " + err.Error()
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}

func lastErr(rs []model.QueryResult) string {
	if len(rs) == 0 {
		return ""
	}
	return rs[len(rs)-1].Error
}

// ---------------------------------------------------------------- #1 read-only bypasses

func TestRegrReadOnlyPostgres(t *testing.T) {
	e := engineByName(t, "postgres")
	f := newFixture(t)
	rw := f.save(e.cfg)
	f.exec(rw.ID, "DROP TABLE IF EXISTS regr_ro")
	f.exec(rw.ID, "CREATE TABLE regr_ro (id int PRIMARY KEY, v text)")
	f.exec(rw.ID, "DROP SEQUENCE IF EXISTS regr_seq")
	f.exec(rw.ID, "CREATE SEQUENCE regr_seq")
	t.Cleanup(func() {
		f.exec(rw.ID, "DROP TABLE IF EXISTS regr_ro, regr_ro2, regr_ro3")
		f.exec(rw.ID, "DROP SEQUENCE IF EXISTS regr_seq")
	})
	f.exec(rw.ID, "INSERT INTO regr_ro SELECT g, 'x' FROM generate_series(1,10) g")
	cfg := e.cfg
	cfg.Name, cfg.ReadOnly = "pg-ro", true
	ro := f.save(cfg)

	for i, sql := range []string{
		"BEGIN READ  WRITE; WITH d AS (DELETE FROM regr_ro WHERE id = 1 RETURNING id) SELECT * FROM d; COMMIT",
		"SELECT set_config/**/('default_transaction_read_only','off',false); WITH d AS (DELETE FROM regr_ro WHERE id = 2 RETURNING id) SELECT * FROM d",
		`SELECT "set_config"('default_transaction_read_only','off',false); SELECT 1 AS x INTO regr_ro2`,
		"START TRANSACTION ISOLATION LEVEL READ COMMITTED, READ\tWRITE; SELECT 1 INTO regr_ro3; COMMIT",
		`SELECT "pg_cancel_backend"(0)`,
		"RESET ALL /* search_path */",
		"DISCARD ALL -- temp",
		"EXPLAIN ANALYZE DELETE FROM regr_ro",
		"SELECT query_to_xml('delete from regr_ro', true, false, '')",
	} {
		rs := f.run(ro.ID, fmt.Sprintf("ro-%d", i), sql)
		if len(rs) != 1 || !strings.HasPrefix(rs[0].Error, model.ErrReadOnly) {
			t.Errorf("%q: expected read_only refusal, got %+v", sql, rs)
		}
	}
	if n := f.scalar(rw.ID, "SELECT count(*) FROM regr_ro"); n != "10" {
		t.Fatalf("rows deleted through the read-only connection: %s left", n)
	}
	if v := f.scalar(rw.ID, "SELECT coalesce(to_regclass('regr_ro2')::text, '') || coalesce(to_regclass('regr_ro3')::text, '')"); v != "" {
		t.Fatalf("tables created through the read-only connection: %q", v)
	}
	// WHERE fragments
	if _, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: ro.ID, Table: "regr_ro", Where: "nextval('regr_seq') > 0"}); err == nil {
		t.Error("nextval in WHERE accepted")
	}
	if _, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: ro.ID, Table: "regr_ro",
		Where: "(SELECT set_config/**/('default_transaction_read_only','off',false)) IS NOT NULL"}); err == nil {
		t.Error("set_config in WHERE accepted")
	}
	// even a read that slips through runs in a read-only transaction: the pool stays read-only
	out := filepath.Join(t.TempDir(), "x.csv")
	if _, err := f.ds.Export(f.ctx, model.ExportRequest{ConnID: ro.ID, Format: "csv", Path: out,
		SQL: "WITH d AS (DELETE FROM regr_ro WHERE id = 3 RETURNING id) SELECT * FROM d"}); err == nil {
		t.Error("data-modifying CTE export accepted")
	}
	p, _ := f.core.Conns.Get(f.ctx, ro.ID)
	if _, err := p.DB.ExecContext(f.ctx, "DELETE FROM regr_ro WHERE id = 4"); err == nil {
		t.Error("pooled session is not read-only")
	}
	if n := f.scalar(rw.ID, "SELECT count(*) FROM regr_ro"); n != "10" {
		t.Fatalf("rows deleted: %s left", n)
	}
	// reads still work, also several per run
	rs := f.run(ro.ID, "ok", "SELECT count(*) FROM regr_ro; SHOW search_path; EXPLAIN SELECT 1")
	if len(rs) != 3 || lastErr(rs) != "" {
		t.Fatalf("reads on ro: %+v", rs)
	}
}

func TestRegrReadOnlyMySQL(t *testing.T) {
	e := engineByName(t, "mysql")
	f := newFixture(t)
	rw := f.save(e.cfg)
	f.exec(rw.ID, "DROP TABLE IF EXISTS regr_ro")
	f.exec(rw.ID, "CREATE TABLE regr_ro (id int PRIMARY KEY, v varchar(20))")
	t.Cleanup(func() { f.exec(rw.ID, "DROP TABLE IF EXISTS regr_ro") })
	f.exec(rw.ID, "INSERT INTO regr_ro VALUES (1,'x'),(2,'x'),(3,'x'),(4,'x')")
	cfg := e.cfg
	cfg.Name, cfg.ReadOnly = "my-ro", true
	ro := f.save(cfg)
	for i, sql := range []string{
		"SET @a = 1, SESSION transaction_read_only = 0; WITH x AS (SELECT 1) DELETE FROM regr_ro WHERE id = 1",
		"START TRANSACTION READ  WRITE; WITH x AS (SELECT 1) DELETE FROM regr_ro WHERE id = 2; COMMIT",
		"SET sql_mode = '', @@session.transaction_read_only = 0; /*!80000 UPDATE regr_ro SET v = 'hacked' WHERE id IN ( */ SELECT 3 /*! ) */",
		"/*!80000 UPDATE regr_ro SET v = 'hacked' WHERE id IN ( */ SELECT 3 /*! ) */",
		"EXPLAIN ANALYZE DELETE a FROM regr_ro a JOIN regr_ro b ON a.id = b.id",
		"SELECT * FROM regr_ro LOCK IN SHARE MODE",
	} {
		rs := f.run(ro.ID, fmt.Sprintf("ro-%d", i), sql)
		if len(rs) != 1 || !strings.HasPrefix(rs[0].Error, model.ErrReadOnly) {
			t.Errorf("%q: expected read_only refusal, got %+v", sql, rs)
		}
	}
	target := filepath.Join(t.TempDir(), "outfile.txt")
	if _, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: ro.ID, Table: "regr_ro", OrderBy: "id LIMIT 3 INTO/**/OUTFILE '" + target + "' -- "}); err == nil {
		t.Error("INTO OUTFILE in ORDER BY accepted")
	}
	if n, v := f.scalar(rw.ID, "SELECT count(*) FROM regr_ro"), f.scalar(rw.ID, "SELECT v FROM regr_ro WHERE id = 3"); n != "4" || v != "x" {
		t.Fatalf("writes through the read-only connection: count=%s v=%s", n, v)
	}
	if p, err := f.core.Conns.Get(f.ctx, ro.ID); err == nil {
		if _, err := p.DB.ExecContext(f.ctx, "DELETE FROM regr_ro WHERE id = 4"); err == nil {
			t.Error("pooled MySQL session is not read-only")
		}
	}
	if rs := f.run(ro.ID, "ok", "SELECT COUNT(*) FROM regr_ro; SHOW CREATE TABLE regr_ro; DESCRIBE regr_ro"); len(rs) != 3 || lastErr(rs) != "" {
		t.Fatalf("reads on ro: %+v", rs)
	}
}

func TestRegrReadOnlySQLiteAndPaths(t *testing.T) {
	e := engineByName(t, "sqlite")
	f := newFixture(t)
	cfg := e.cfg
	cfg.ReadOnly = true
	ro := f.save(cfg)
	for i, sql := range []string{
		"PRAGMA query_only/**/=0",
		"WITH x AS (SELECT 1) INSERT INTO store (code, name) SELECT 'zz','zz' FROM x",
		"PRAGMA journal_mode/**/=DELETE",
		"PRAGMA user_version(7)",
	} {
		rs := f.run(ro.ID, fmt.Sprintf("s%d", i), sql)
		if len(rs) != 1 || !strings.HasPrefix(rs[0].Error, model.ErrReadOnly) {
			t.Errorf("%q: expected refusal, got %+v", sql, rs)
		}
	}
	if rs := f.run(ro.ID, "ok", "PRAGMA table_info(store); SELECT count(*) FROM store"); lastErr(rs) != "" {
		t.Fatalf("reads: %+v", rs)
	}
	// #10: URI special characters in the path; read-only must hold
	dir := t.TempDir()
	for _, name := range []string{"my#db.sqlite", "what?.sqlite", "100%41.sqlite"} {
		p := filepath.Join(dir, name)
		copyFile(t, e.cfg.FilePath, p)
		c := f.save(model.ConnectionConfig{Name: name, Driver: model.DriverSQLite, FilePath: p, ReadOnly: true})
		rs := f.run(c.ID, "", "SELECT count(*) FROM store")
		if lastErr(rs) != "" || len(rs[0].Rows) != 1 || rs[0].Rows[0][0] != int64(4) {
			t.Errorf("%s: %+v", name, rs)
		}
		pool, err := f.core.Conns.Get(f.ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.DB.ExecContext(f.ctx, "DELETE FROM store"); err == nil {
			t.Errorf("%s: opened writable", name)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 3 {
		var names []string
		for _, en := range ents {
			names = append(names, en.Name())
		}
		t.Errorf("stray files created: %v", names)
	}
}

// ---------------------------------------------------------------- #4 export never writes

func TestRegrExportReadOnlyByNature(t *testing.T) {
	for _, name := range []string{"mysql", "postgres", "sqlite"} {
		t.Run(name, func(t *testing.T) {
			e := engineByName(t, name)
			f := newFixture(t)
			c := f.save(e.cfg)
			f.exec(c.ID, "DROP TABLE IF EXISTS regr_exp")
			f.exec(c.ID, "CREATE TABLE regr_exp (id int PRIMARY KEY, v varchar(20))")
			t.Cleanup(func() { f.exec(c.ID, "DROP TABLE IF EXISTS regr_exp") })
			f.exec(c.ID, "INSERT INTO regr_exp VALUES (1,'x'),(2,'x'),(3,'x')")
			out := filepath.Join(t.TempDir(), "x.csv")
			for _, sql := range []string{
				"DELETE FROM regr_exp WHERE id < 3",
				"UPDATE regr_exp SET v = 'changed' RETURNING *",
				"WITH d AS (DELETE FROM regr_exp RETURNING id) SELECT * FROM d",
				"INSERT INTO regr_exp VALUES (9, 'y') RETURNING *",
			} {
				if _, err := f.ds.Export(f.ctx, model.ExportRequest{ConnID: c.ID, Format: "csv", Path: out, SQL: sql}); err == nil {
					t.Errorf("export accepted %q", sql)
				}
			}
			if n, v := f.scalar(c.ID, "SELECT count(*) FROM regr_exp"), f.scalar(c.ID, "SELECT min(v) FROM regr_exp"); n != "3" || v != "x" {
				t.Fatalf("export modified data: count=%s min(v)=%s", n, v)
			}
			if r, err := f.ds.Export(f.ctx, model.ExportRequest{ConnID: c.ID, Format: "csv", Path: out, SQL: "SELECT * FROM regr_exp"}); err != nil || r.Rows != 3 {
				t.Fatalf("select export: %+v %v", r, err)
			}
		})
	}
}

// ---------------------------------------------------------------- #2 #17 postgres edits / index drop

func TestRegrPostgresTruncationAndIndexDrop(t *testing.T) {
	e := engineByName(t, "postgres")
	f := newFixture(t)
	c := f.save(e.cfg)
	f.exec(c.ID, "DROP TABLE IF EXISTS regr_trunc")
	f.exec(c.ID, "CREATE TABLE regr_trunc (id int PRIMARY KEY, s varchar(5), ch char(3), b bit(8), n numeric(6,2), tags varchar(3)[], CONSTRAINT regr_uq CHECK (id > 0))")
	t.Cleanup(func() { f.exec(c.ID, "DROP TABLE IF EXISTS regr_trunc") })
	f.exec(c.ID, "INSERT INTO regr_trunc VALUES (1,'abc','x',B'10101010',1.5,'{a}')")
	for col, val := range map[string]any{"s": "abcdefghij", "ch": "wxyz", "b": "1", "tags": "{abcdef}"} {
		r, err := f.ds.ApplyChanges(f.ctx, model.ChangeSet{ConnID: c.ID, Table: "regr_trunc",
			Updates: []model.RowUpdate{{PK: map[string]any{"id": float64(1)}, Set: map[string]any{col: val}}}})
		if err != nil || r.OK {
			t.Errorf("%s=%v: expected a database error, got ok=%v err=%v", col, val, r.OK, err)
		}
	}
	r, err := f.ds.ApplyChanges(f.ctx, model.ChangeSet{ConnID: c.ID, Table: "regr_trunc",
		Updates: []model.RowUpdate{{PK: map[string]any{"id": float64(1)}, Set: map[string]any{"s": "abcde", "ch": "xyz", "b": "11110000", "n": "1234.56"}}}})
	if err != nil || !r.OK {
		t.Fatalf("valid update: %+v %v", r, err)
	}
	if got := f.scalar(c.ID, "SELECT s || '|' || ch || '|' || b::text || '|' || n::text FROM regr_trunc"); got != "abcde|xyz|11110000|1234.56" {
		t.Fatalf("stored: %s", got)
	}
	// unique index sharing its name with a CHECK constraint: only the index goes
	f.exec(c.ID, "CREATE UNIQUE INDEX regr_uq ON regr_trunc (n)")
	ch := model.StructureChange{ConnID: c.ID, Table: "regr_trunc", DropIndexes: []string{"regr_uq"}}
	prev, err := f.ss.PreviewStructureChanges(f.ctx, ch)
	if err != nil || len(prev) != 1 || !strings.HasPrefix(prev[0], "DROP INDEX") {
		t.Fatalf("preview: %v %v", prev, err)
	}
	ch.ExpectedStatements = prev
	if ar, err := f.ss.ApplyStructureChanges(f.ctx, ch); err != nil || !ar.OK {
		t.Fatalf("apply: %+v %v", ar, err)
	}
	if n := f.scalar(c.ID, "SELECT count(*) FROM pg_constraint WHERE conrelid = 'regr_trunc'::regclass AND contype = 'c'"); n != "1" {
		t.Fatalf("check constraint dropped too (%s left)", n)
	}
	// a unique constraint is dropped as a constraint
	f.exec(c.ID, "ALTER TABLE regr_trunc ADD CONSTRAINT regr_uq2 UNIQUE (s)")
	prev, _ = f.ss.PreviewStructureChanges(f.ctx, model.StructureChange{ConnID: c.ID, Table: "regr_trunc", DropIndexes: []string{"regr_uq2"}})
	if len(prev) != 1 || !strings.Contains(prev[0], "DROP CONSTRAINT") {
		t.Fatalf("constraint index preview: %v", prev)
	}
	// #32: a stale preview is refused
	stale := model.StructureChange{ConnID: c.ID, Table: "regr_trunc", DropColumns: []string{"tags"}, ExpectedStatements: []string{"ALTER TABLE x DROP COLUMN y;"}}
	if _, err := f.ss.ApplyStructureChanges(f.ctx, stale); err == nil {
		t.Fatal("stale structure preview accepted")
	}
}

// ---------------------------------------------------------------- #3 cancel never hides a committed write

func TestRegrCancelDoesNotHideCommits(t *testing.T) {
	for _, name := range []string{"postgres", "mysql"} {
		t.Run(name, func(t *testing.T) {
			e := engineByName(t, name)
			f := newFixture(t)
			c := f.save(e.cfg)
			f.exec(c.ID, "DROP TABLE IF EXISTS regr_cancel")
			f.exec(c.ID, "CREATE TABLE regr_cancel (id int PRIMARY KEY, n int)")
			t.Cleanup(func() { f.exec(c.ID, "DROP TABLE IF EXISTS regr_cancel") })
			f.exec(c.ID, "INSERT INTO regr_cancel VALUES (1, 0)")
			ok := 0
			for i := 0; i < 150; i++ {
				qid := fmt.Sprintf("c-%s-%d", name, i)
				stop := make(chan struct{})
				fired := make(chan struct{})
				go func() {
					defer close(fired)
					time.Sleep(time.Duration(i%40) * 50 * time.Microsecond)
					for {
						select {
						case <-stop:
							return
						default:
							if f.qs.CancelQuery(qid) {
								return
							}
						}
					}
				}()
				rs, _ := f.qs.RunQuery(context.Background(), model.QueryRequest{ConnID: c.ID, QueryID: qid, SessionID: "cancel",
					SQL: "UPDATE regr_cancel SET n = n + 1 WHERE id = 1"})
				close(stop)
				<-fired
				if len(rs) == 1 && rs[0].Error == "" {
					ok++
				}
			}
			if n := f.scalar(c.ID, "SELECT n FROM regr_cancel"); n != strconv.Itoa(ok) {
				t.Fatalf("%s committed updates, %d reported as success", n, ok)
			}
		})
	}
}

// ---------------------------------------------------------------- #5 #9 console performance

func TestRegrConsoleBigResultStops(t *testing.T) {
	for _, name := range []string{"mysql", "postgres"} {
		t.Run(name, func(t *testing.T) {
			e := engineByName(t, name)
			f := newFixture(t)
			c := f.save(e.cfg)
			for _, session := range []string{"big", ""} {
				start := time.Now()
				rs, err := f.qs.RunQuery(f.ctx, model.QueryRequest{ConnID: c.ID, QueryID: "big" + session, MaxRows: 10, SessionID: session,
					SQL: "SELECT a.* FROM sales_order_item a CROSS JOIN sales_order_item b"})
				if err != nil || len(rs) != 1 || rs[0].Error != "" || len(rs[0].Rows) != 10 || !rs[0].HasMore {
					t.Fatalf("big select: %v %+v", err, rs)
				}
				if el := time.Since(start); el > 2*time.Second {
					t.Fatalf("session %q: maxRows=10 on a 24M-row result took %s", session, el)
				}
			}
			if rs := f.run(c.ID, "big", "SELECT 1"); lastErr(rs) != "" {
				t.Fatalf("session unusable after the stop: %+v", rs)
			}
		})
	}
}

func TestRegrDisconnectCancelsRunningQuery(t *testing.T) {
	e := engineByName(t, "postgres")
	f := newFixture(t)
	c := f.save(e.cfg)
	done := make(chan struct{})
	go func() {
		f.qs.RunQuery(context.Background(), model.QueryRequest{ConnID: c.ID, QueryID: "slow", SessionID: "slow", SQL: "SELECT pg_sleep(6)"})
		close(done)
	}()
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	f.cs.Disconnect(c.ID)
	<-done
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Disconnect took %s while a console query ran", el)
	}
}

// ---------------------------------------------------------------- #16 #21 #22 #23 #25 console behaviour

func TestRegrConsoleBehaviour(t *testing.T) {
	e := engineByName(t, "mysql")
	f := newFixture(t)
	c := f.save(e.cfg)
	f.exec(c.ID, "DROP TABLE IF EXISTS regr_misc")
	f.exec(c.ID, "CREATE TABLE regr_misc (id int PRIMARY KEY, v varchar(40))")
	t.Cleanup(func() { f.exec(c.ID, "DROP TABLE IF EXISTS regr_misc") })
	f.exec(c.ID, "INSERT INTO regr_misc VALUES (1,'x'),(2,'y')")
	if rs := f.run(c.ID, "m", "UPDATE regr_misc SET v = 'returning customer'"); rs[0].Error != "" || rs[0].RowsAffected != 2 || rs[0].HasResultSet {
		t.Errorf("returning in a string: %+v", rs[0])
	}
	if rs := f.run(c.ID, "m", "ANALYZE TABLE regr_misc"); rs[0].Error != "" || !rs[0].HasResultSet || len(rs[0].Rows) == 0 {
		t.Errorf("ANALYZE TABLE rows: %+v", rs[0])
	}
	if rs := f.run(c.ID, "m", "SELECT 5--1 AS a; SELECT 2 AS b"); len(rs) != 2 || lastErr(rs) != "" || rs[0].Rows[0][0] != int64(6) {
		t.Errorf("5--1: %+v", rs)
	}
	// USE carries over although the frontend sends the tab schema on every run
	rq := func(sql string) []model.QueryResult {
		r, _ := f.qs.RunQuery(f.ctx, model.QueryRequest{ConnID: c.ID, QueryID: "u", SessionID: "use", Schema: "dbviewer_dummy", SQL: sql})
		return r
	}
	rq("USE dbviewer_dummy_ro")
	if rs := rq("SELECT DATABASE()"); len(rs) != 1 || rs[0].Rows[0][0] != "dbviewer_dummy_ro" {
		t.Errorf("USE did not carry over: %+v", rs)
	}
	// trailing comment in the WHERE input
	if _, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: c.ID, Table: "regr_misc", Where: "id = 1 -- only the first"}); err != nil {
		t.Errorf("where with trailing comment: %v", err)
	}
	if _, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: c.ID, Table: "regr_misc", OrderBy: "v -- comment"}); err != nil {
		t.Errorf("order by with trailing comment: %v", err)
	}

	pe := engineByName(t, "postgres")
	pc := f.save(pe.cfg)
	if rs := f.run(pc.ID, "p", `SELECT E'it\'s; fine' AS s`); len(rs) != 1 || rs[0].Error != "" || rs[0].Rows[0][0] != "it's; fine" {
		t.Errorf("E-string: %+v", rs)
	}
	rs := f.run(pc.ID, "p", `BEGIN; DECLARE cur CURSOR FOR SELECT 1 AS one; FETCH ALL FROM cur; COMMIT`)
	if len(rs) != 4 || lastErr(rs) != "" || !rs[2].HasResultSet || len(rs[2].Rows) != 1 {
		t.Errorf("FETCH rows: %+v", rs)
	}
	f.run(pc.ID, "p", "SELECT 1")
	if rs := f.run(pc.ID, "p", "DISCARD ALL"); lastErr(rs) != "" {
		t.Errorf("discard all: %+v", rs)
	}
	if rs := f.run(pc.ID, "p", "SELECT 1"); lastErr(rs) != "" {
		t.Errorf("session after DISCARD ALL: %+v", rs)
	}
}

// ---------------------------------------------------------------- #7 #15 sqlite

func TestRegrSQLiteDatesAndBrokenView(t *testing.T) {
	e := engineByName(t, "sqlite")
	f := newFixture(t)
	c := f.save(e.cfg)
	f.exec(c.ID, "CREATE TABLE regr_dt (id INTEGER PRIMARY KEY, d DATETIME, dd DATE, ts TIMESTAMP)")
	f.exec(c.ID, `INSERT INTO regr_dt VALUES (1,'2025-01-31T10:00:00Z','2025-01-31 23:59:59','2025-01-31 10:00:00.123456789'), (2,'2025-01-31 10:00','31-01-2025',1738317600)`)
	pg := f.page(c.ID, "regr_dt", "")
	want := [][]any{{int64(1), "2025-01-31T10:00:00Z", "2025-01-31 23:59:59", "2025-01-31 10:00:00.123456789"}, {int64(2), "2025-01-31 10:00", "31-01-2025", int64(1738317600)}}
	if fmt.Sprint(pg.Rows) != fmt.Sprint(want) {
		t.Errorf("stored text not passed through:\n got  %v\n want %v", pg.Rows, want)
	}
	f.exec(c.ID, "CREATE TABLE regr_dtpk (d DATETIME PRIMARY KEY, v TEXT)")
	f.exec(c.ID, "INSERT INTO regr_dtpk VALUES ('2025-01-31T10:00:00Z','a')")
	p2 := f.page(c.ID, "regr_dtpk", "")
	if r, err := f.ds.ApplyChanges(f.ctx, model.ChangeSet{ConnID: c.ID, Table: "regr_dtpk",
		Updates: []model.RowUpdate{{PK: map[string]any{"d": p2.Rows[0][0]}, Set: map[string]any{"v": "b"}}}}); err != nil || !r.OK {
		t.Errorf("datetime pk update: %+v %v", r, err)
	}
	f.exec(c.ID, "CREATE TABLE regr_base (a int)")
	f.exec(c.ID, "CREATE VIEW regr_v AS SELECT a FROM regr_base")
	f.exec(c.ID, "DROP TABLE regr_base")
	tables, err := f.ss.Tables(f.ctx, c.ID, "", true)
	if err != nil || len(tables) < 266 {
		t.Fatalf("Tables with a broken view: %d %v", len(tables), err)
	}
	if _, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: c.ID, Table: "store"}); err != nil {
		t.Fatalf("FetchPage with a broken view in the schema: %v", err)
	}
}

// ---------------------------------------------------------------- #14 #27 mysql bool / bit, #24 ipv6

func TestRegrMySQLBoolBitIPv6(t *testing.T) {
	e := engineByName(t, "mysql")
	f := newFixture(t)
	c := f.save(e.cfg)
	f.exec(c.ID, "DROP TABLE IF EXISTS regr_bool")
	f.exec(c.ID, "CREATE TABLE regr_bool (id int PRIMARY KEY, flag tinyint(1), b8 bit(8), b1 bit(1))")
	t.Cleanup(func() { f.exec(c.ID, "DROP TABLE IF EXISTS regr_bool") })
	f.exec(c.ID, "INSERT INTO regr_bool VALUES (1, 2, b'01000001', b'1'), (2, -1, b'11111111', b'0'), (3, 1, b'00000000', b'0')")
	for _, filtered := range []bool{false, true} {
		req := model.PageRequest{ConnID: c.ID, Table: "regr_bool"}
		if filtered {
			req.ColumnFilters = []model.ColumnFilter{{Column: "id", Op: "gte", Value: "1"}}
		}
		p, err := f.ds.FetchPage(f.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		flags := fmt.Sprint(p.Rows[0][1], p.Rows[1][1], p.Rows[2][1])
		if flags != "2 -1 true" {
			t.Errorf("filtered=%v tinyint(1) values: %s", filtered, flags)
		}
		if b, ok := p.Rows[0][2].(query.Binary); !ok || b.Hex != "41" {
			t.Errorf("filtered=%v bit(8): %#v", filtered, p.Rows[0][2])
		}
		if b, ok := p.Rows[2][2].(query.Binary); !ok || b.Hex != "00" {
			t.Errorf("filtered=%v bit(8) zero: %#v", filtered, p.Rows[2][2])
		}
	}
	out := filepath.Join(t.TempDir(), "x.sql")
	if _, err := f.ds.Export(f.ctx, model.ExportRequest{ConnID: c.ID, Format: "sql", Path: out, Page: &model.PageRequest{Table: "regr_bool"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), "VALUES (1, 2, ") || !strings.Contains(string(b), "VALUES (2, -1, ") {
		t.Errorf("sql export rewrote values:\n%s", b)
	}
	empty := ""
	for _, host := range []string{"::1", "[::1]"} {
		if _, err := f.cs.TestConnection(f.ctx, model.ConnectionConfig{ProjectID: f.pid, Name: "v6", Driver: model.DriverMySQL, Host: host, Port: 3384, User: "root", Password: &empty}); err != nil {
			if strings.Contains(err.Error(), "refused") {
				t.Skip("mysql does not listen on [::1]:3384")
			}
			t.Errorf("mysql %s: %v", host, err)
		}
	}
}

// ---------------------------------------------------------------- #26 postgres BC dates round trip

func TestRegrPostgresBCDates(t *testing.T) {
	e := engineByName(t, "postgres")
	f := newFixture(t)
	c := f.save(e.cfg)
	f.exec(c.ID, "DROP TABLE IF EXISTS regr_bc")
	f.exec(c.ID, "CREATE TABLE regr_bc (id int PRIMARY KEY, d date, ts timestamp)")
	t.Cleanup(func() { f.exec(c.ID, "DROP TABLE IF EXISTS regr_bc") })
	f.exec(c.ID, "INSERT INTO regr_bc VALUES (1, '0044-03-15 BC', '0001-01-01 10:00:00 BC')")
	p := f.page(c.ID, "regr_bc", "")
	if p.Rows[0][1] != "0044-03-15 BC" || p.Rows[0][2] != "0001-01-01 10:00:00 BC" {
		t.Fatalf("BC display: %#v", p.Rows[0])
	}
	r, err := f.ds.ApplyChanges(f.ctx, model.ChangeSet{ConnID: c.ID, Table: "regr_bc",
		Inserts: []model.RowInsert{{Values: map[string]any{"id": float64(2), "d": p.Rows[0][1], "ts": p.Rows[0][2]}}}})
	if err != nil || !r.OK {
		t.Fatalf("write back: %+v %v", r, err)
	}
	if f.scalar(c.ID, "SELECT count(DISTINCT (d, ts)) FROM regr_bc") != "1" {
		t.Fatal("BC values changed on write back")
	}
}

// ---------------------------------------------------------------- #11 secrets in errors

func TestRegrPasswordNotInErrors(t *testing.T) {
	f := newFixture(t)
	pw := "hunter2' tail-of-secret"
	cfg := model.ConnectionConfig{ProjectID: f.pid, Name: "x", Driver: model.DriverPostgres, Host: "127.0.0.1", Port: 55432, User: "postgres",
		Password: &pw, Database: "dbviewer_dummy", SSLMode: model.SSLDisable, Params: map[string]string{"connect_timeout": "abc"}}
	_, err := f.cs.TestConnection(f.ctx, cfg)
	if err == nil || strings.Contains(err.Error(), "tail-of-secret") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error leaks the password: %v", err)
	}
	cfg.SSLMode = "bogus"
	if _, err := f.cs.TestConnection(f.ctx, cfg); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("bogus sslmode: %v", err)
	}
	if _, err := f.cs.SaveConnection(cfg); err == nil {
		t.Fatal("bogus sslmode saved")
	}
}

// ---------------------------------------------------------------- #12 #13 #28 .env

func TestRegrEnvImport(t *testing.T) {
	r := envimport.Parse("DATABASE_URL=postgres://app:s3cr3t@prod.example.com:5433/shop?sslmode=require\nDB_CONNECTION=mysql\nDB_HOST=127.0.0.1\nDB_PORT=3306\nDB_DATABASE=laravel\nDB_USERNAME=root\nDB_PASSWORD=\n", "/base")
	c := r.Config
	if c.Driver != model.DriverPostgres || c.Host != "prod.example.com" || c.Port != 5433 || c.User != "app" || *c.Password != "s3cr3t" || c.Database != "shop" {
		t.Errorf("URL precedence: %+v", c)
	}
	if strings.Contains(r.Found["DATABASE_URL"], "s3cr3t") {
		t.Errorf("URL password not masked: %q", r.Found["DATABASE_URL"])
	}
	if len(r.Warnings) == 0 {
		t.Error("no warning about ignored DB_* keys")
	}
	r = envimport.Parse("DB_CONNECTION=mysql\nDB_HOST=db\nDB_PASSWORD=\"sec#ret\" # the \"real\" one\nDB_USERNAME='root' # it's root\nDB_DATABASE=app # main\n", "")
	if *r.Config.Password != "sec#ret" || r.Config.User != "root" || r.Config.Database != "app" {
		t.Errorf("quoted values: pw=%q user=%q db=%q", *r.Config.Password, r.Config.User, r.Config.Database)
	}
	r = envimport.Parse("\ufeffDB_CONNECTION=pgsql\nexport\tDB_HOST=pg\n", "")
	if r.Config.Driver != model.DriverPostgres || r.Config.Host != "pg" {
		t.Errorf("BOM/export: %+v", r.Config)
	}
	r = envimport.Parse("DB_CONNECTION=mysql\nDB_HOST=localhost\nDB_SOCKET=null\nDB_PASSWORD=null\nDB_DATABASE=app\n", "")
	if r.Config.Socket != "" || (r.Config.Password != nil && *r.Config.Password != "") || len(r.Warnings) != 0 {
		t.Errorf("null values: %+v %v", r.Config, r.Warnings)
	}
	if r = envimport.Parse("DATABASE_URL=sqlite:database/app.sqlite\n", "/base"); r.Config.FilePath != "/base/database/app.sqlite" {
		t.Errorf("opaque sqlite url: %q", r.Config.FilePath)
	}
	if r = envimport.Parse("DB_CONNECTION=sqlite\nDB_DATABASE=:memory:\n", "/base"); r.Config.FilePath != "" || len(r.Warnings) == 0 {
		t.Errorf(":memory: %q %v", r.Config.FilePath, r.Warnings)
	}
	if r = envimport.Parse("DB_CONNECTION=mysql\nDB_HOST=h\nDB_PASSWORD=\"pa$$w${ord}x\"\n", ""); len(r.Warnings) == 0 {
		t.Error("no warning for undefined ${ord}")
	}
}

// ---------------------------------------------------------------- #6 #8 ssh

func TestRegrSSHKnownHostKeyType(t *testing.T) {
	f := newFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	ed := newSigner(t)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ec, _ := ssh.NewSignerFromKey(ecKey)
	srv := startSSHServer(t, ed, "secret", nil, ec)
	host, portS, _ := net.SplitHostPort(srv.addr)
	port, _ := strconv.Atoi(portS)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	line := knownhosts.Line([]string{knownhosts.Normalize(srv.addr)}, ed.PublicKey())
	os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte(line+"\n"), 0o600)
	pgpw, sshpw := "dbviewer", "secret"
	cfg := model.ConnectionConfig{ProjectID: f.pid, Name: "pg via ssh", Driver: model.DriverPostgres, Host: "127.0.0.1", Port: 55432,
		User: "postgres", Password: &pgpw, Database: "dbviewer_dummy", SSLMode: model.SSLDisable,
		SSH: model.SSHTunnel{Enabled: true, Host: host, Port: port, User: "tunnel", AuthMethod: "password", Password: &sshpw}}
	if _, err := f.cs.TestConnection(f.ctx, cfg); err != nil {
		t.Fatalf("host known by its ed25519 key: %v", err)
	}
}

func TestRegrSSHTestKeepsPool(t *testing.T) {
	f := newFixture(t)
	srv := startSSHServer(t, newSigner(t), "secret", nil)
	host, portS, _ := net.SplitHostPort(srv.addr)
	port, _ := strconv.Atoi(portS)
	var prompts []model.HostKeyPrompt
	f.core.Conns.OnHostKey = func(p model.HostKeyPrompt) { prompts = append(prompts, p) }
	empty, sshpw := "", "secret"
	cfg := model.ConnectionConfig{ProjectID: f.pid, Name: "my via ssh", Driver: model.DriverMySQL, Host: "127.0.0.1", Port: 3384,
		User: "root", Password: &empty, Database: "dbviewer_dummy",
		SSH: model.SSHTunnel{Enabled: true, Host: host, Port: port, User: "tunnel", AuthMethod: "password", Password: &sshpw}}
	f.cs.TestConnection(f.ctx, cfg)
	if len(prompts) == 0 {
		t.Fatal("no host key prompt")
	}
	f.cs.TrustHostKey(prompts[0].Host, prompts[0].Fingerprint)
	saved := f.save(cfg)
	if _, err := f.cs.Connect(f.ctx, saved.ID, ""); err != nil {
		t.Skipf("mysql over tcp 3384 not available: %v", err)
	}
	if _, err := f.cs.TestConnection(f.ctx, saved); err != nil {
		t.Fatal(err)
	}
	p, _ := f.core.Conns.Get(f.ctx, saved.ID)
	p.DB.SetMaxIdleConns(0) // force new physical connections through the tunnel
	for i := 0; i < 3; i++ {
		if _, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: saved.ID, Table: "store"}); err != nil {
			t.Fatalf("after TestConnection, query %d: %v", i, err)
		}
	}
}

// ---------------------------------------------------------------- #30 history

func TestRegrHistoryMasksSecrets(t *testing.T) {
	f := newFixture(t)
	if err := f.core.Store.AddHistory(model.QueryHistory{ConnID: "c1", SQL: "CREATE USER 'a'@'%' IDENTIFIED BY 's3cret'; ALTER ROLE r PASSWORD 'p4ss'"}); err != nil {
		t.Fatal(err)
	}
	h, _ := f.qs.History("c1", "", 10)
	if len(h) != 1 || strings.Contains(h[0].SQL, "s3cret") || strings.Contains(h[0].SQL, "p4ss") {
		t.Fatalf("history: %+v", h)
	}
}
