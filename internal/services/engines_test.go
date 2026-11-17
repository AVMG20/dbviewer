package services

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dbviewer/internal/model"
	"dbviewer/internal/query"
)

// Per-engine integration tests against the seeded dummy databases
// (testdata/seed.sh). Writes only happen in scratch tables named dbviewer_test_*
// (dropped afterwards) and in a temporary copy of dummy.sqlite.

type engine struct {
	name     string
	cfg      model.ConnectionConfig
	scratch  string // CREATE TABLE for dbviewer_test_vals
	bigValue string // largest value for column big
	exactDec bool   // decimals are exact (not sqlite)
	sleepSQL string
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	out.Close()
}

func engines(t *testing.T) []engine {
	tmp := t.TempDir()
	sq := filepath.Join(tmp, "dummy.sqlite")
	copyFile(t, filepath.Join(repoRoot(), "testdata", "dummy.sqlite"), sq)
	empty, pgpw := "", "dbviewer"
	return []engine{
		{
			name: "mysql",
			cfg: model.ConnectionConfig{Name: "mysql", Driver: model.DriverMySQL, Socket: "/opt/workbench/data/run/mysqld/mysql84.sock",
				User: "root", Password: &empty, Database: "dbviewer_dummy"},
			scratch: "CREATE TABLE dbviewer_test_vals (id int unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, d decimal(20,4), big bigint unsigned, " +
				"f double, ff float, dt datetime, j json, b blob, t mediumtext, s varchar(50), flag tinyint(1))",
			bigValue: "18446744073709551615", exactDec: true, sleepSQL: "SELECT SLEEP(10)",
		},
		{
			name: "postgres",
			cfg: model.ConnectionConfig{Name: "pg", Driver: model.DriverPostgres, Host: "127.0.0.1", Port: 55432, User: "postgres",
				Password: &pgpw, Database: "dbviewer_dummy", SSLMode: model.SSLDisable},
			scratch: "CREATE TABLE dbviewer_test_vals (id serial PRIMARY KEY, d numeric(20,4), big bigint, f double precision, ff real, " +
				"dt timestamp, j jsonb, b bytea, t text, s varchar(50), flag boolean)",
			bigValue: "9223372036854775807", exactDec: true, sleepSQL: "SELECT pg_sleep(10)",
		},
		{
			name: "sqlite",
			cfg:  model.ConnectionConfig{Name: "sqlite", Driver: model.DriverSQLite, FilePath: sq},
			scratch: "CREATE TABLE dbviewer_test_vals (id INTEGER PRIMARY KEY, d DECIMAL(20,4), big INTEGER, f REAL, ff FLOAT, dt DATETIME, " +
				"j JSON, b BLOB, t TEXT, s VARCHAR(50), flag BOOLEAN)",
			bigValue: "9223372036854775807",
			sleepSQL: "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT max(x) FROM c",
		},
	}
}

type fixture struct {
	t    *testing.T
	ctx  context.Context
	core *Core
	ps   *ProjectService
	cs   *ConnectionService
	ss   *SchemaService
	ds   *DataService
	qs   *QueryService
	pid  string
}

func newFixture(t *testing.T) *fixture {
	c := testCore(t)
	f := &fixture{t: t, ctx: context.Background(), core: c, ps: NewProjectService(c), cs: NewConnectionService(c),
		ss: NewSchemaService(c), ds: NewDataService(c), qs: NewQueryService(c)}
	ps, err := f.ps.Projects()
	if err != nil {
		t.Fatal(err)
	}
	f.pid = ps[0].ID
	return f
}

func (f *fixture) save(cfg model.ConnectionConfig) model.ConnectionConfig {
	f.t.Helper()
	cfg.ProjectID = f.pid
	c, err := f.cs.SaveConnection(cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fixture) exec(connID, sql string) {
	f.t.Helper()
	p, err := f.core.Conns.Get(f.ctx, connID)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := p.DB.ExecContext(f.ctx, sql); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	f.core.Meta.Invalidate(connID, "")
}

func (f *fixture) page(connID, table, where string) model.QueryResult {
	f.t.Helper()
	r, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: connID, Table: table, Where: where, Limit: 100})
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func colIndex(r model.QueryResult, name string) int {
	for i, c := range r.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

func TestEnginesFull(t *testing.T) {
	for _, e := range engines(t) {
		t.Run(e.name, func(t *testing.T) {
			if e.cfg.Socket != "" {
				if _, err := os.Stat(e.cfg.Socket); err != nil {
					t.Skip("mysql socket not available")
				}
			}
			f := newFixture(t)
			conn := f.save(e.cfg)
			if _, err := f.cs.Connect(f.ctx, conn.ID, ""); err != nil {
				t.Fatalf("connect (run testdata/seed.sh first): %v", err)
			}
			f.exec(conn.ID, "DROP TABLE IF EXISTS dbviewer_test_vals")
			f.exec(conn.ID, e.scratch)
			t.Cleanup(func() {
				p, err := f.core.Conns.Get(context.Background(), conn.ID)
				if err == nil {
					_, _ = p.DB.Exec("DROP TABLE IF EXISTS dbviewer_test_vals")
				}
			})

			t.Run("metadata", func(t *testing.T) { testMetadata(t, f, conn.ID) })
			t.Run("values+changes", func(t *testing.T) { testValuesAndChanges(t, f, conn.ID, e) })
			t.Run("copy", func(t *testing.T) { testCopy(t, f, conn.ID) })
			t.Run("structure", func(t *testing.T) { testStructure(t, f, conn.ID) })
			t.Run("export", func(t *testing.T) { testExport(t, f, conn.ID, e) })
			t.Run("find", func(t *testing.T) { testFind(t, f, conn.ID) })
			t.Run("sessions", func(t *testing.T) { testSessions(t, f, conn.ID, e) })
			t.Run("cancel", func(t *testing.T) { testCancel(t, f, conn.ID, e) })
			t.Run("search", func(t *testing.T) { testSearch(t, f, conn.ID) })
			t.Run("readonly", func(t *testing.T) { testReadOnly(t, f, e) })
		})
	}
}

func testMetadata(t *testing.T, f *fixture, id string) {
	start := time.Now()
	tables, err := f.ss.Tables(f.ctx, id, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("metadata for %d tables took %s (want < 1s)", len(tables), el)
	}
	if len(tables) < 266 {
		t.Fatalf("tables: %d", len(tables))
	}
	st, err := f.ss.TableStructure(f.ctx, id, "", "sales_order")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Columns) != 25 || !strings.Contains(st.DDL, "CREATE TABLE") {
		t.Fatalf("structure: %d cols, ddl %q", len(st.Columns), st.DDL)
	}
	var primary, emailIdx bool
	for _, ix := range st.Indexes {
		primary = primary || (ix.Primary && len(ix.Columns) == 1 && ix.Columns[0] == "entity_id")
		emailIdx = emailIdx || (len(ix.Columns) == 1 && ix.Columns[0] == "customer_email" && !ix.Unique)
	}
	if !primary || !emailIdx {
		t.Fatalf("indexes: %+v", st.Indexes)
	}
	fkTo := map[string]bool{}
	for _, fk := range st.ForeignKeys {
		fkTo[fk.RefTable+"."+strings.Join(fk.RefColumns, ",")] = true
		if fk.OnDelete != "CASCADE" {
			t.Errorf("fk %s on delete %q", fk.Name, fk.OnDelete)
		}
	}
	if !fkTo["store.store_id"] || !fkTo["customer_entity.entity_id"] {
		t.Fatalf("fks: %+v", st.ForeignKeys)
	}
	refBy := map[string]bool{}
	for _, fk := range st.ReferencedBy {
		refBy[fk.Table+"."+strings.Join(fk.Columns, ",")] = true
	}
	if !refBy["sales_order_item.order_id"] || !refBy["sales_order_status_history.parent_id"] {
		t.Fatalf("referenced by: %+v", st.ReferencedBy)
	}
	ccp, err := f.ss.Indexes(f.ctx, id, "", "catalog_category_product")
	if err != nil || len(ccp) == 0 || !ccp[0].Primary || len(ccp[0].Columns) != 2 {
		t.Fatalf("composite pk index: %+v %v", ccp, err)
	}
	view, err := f.ss.DDL(f.ctx, id, "", "sales_order_summary_view")
	if err != nil || !strings.Contains(strings.ToUpper(view), "VIEW") {
		t.Fatalf("view ddl: %q %v", view, err)
	}
	nopk, err := f.ss.TableStructure(f.ctx, id, "", "customer_visitor_log")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range nopk.Columns {
		if c.PK {
			t.Fatalf("customer_visitor_log should have no pk")
		}
	}
}

func testValuesAndChanges(t *testing.T, f *fixture, id string, e engine) {
	long := strings.Repeat("x", query.MaxCellBytes+500)
	cs := model.ChangeSet{ConnID: id, Table: "dbviewer_test_vals", Inserts: []model.RowInsert{
		{Values: map[string]any{"d": "12345678901234.5678", "big": e.bigValue, "f": 1.5, "ff": 0.1, "dt": "2025-01-31 13:45:00",
			"j": `{"a": 1, "b": [true, null]}`, "b": map[string]any{"$type": "binary", "hex": "00ff10"}, "t": long, "s": "", "flag": true}},
		{Values: map[string]any{"d": nil, "s": nil, "t": "it's a \"quote\" \\ backslash", "flag": false}},
		{Values: map[string]any{"s": map[string]any{"$type": "default"}}},
	}}
	prev, err := f.ds.PreviewChanges(f.ctx, cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(prev) != 3 || !strings.Contains(prev[0], "12345678901234.5678") || !strings.Contains(prev[0], "'2025-01-31 13:45:00'") {
		t.Fatalf("preview: %v", prev)
	}
	res, err := f.ds.ApplyChanges(f.ctx, cs)
	if err != nil || !res.OK {
		t.Fatalf("apply: %+v %v", res, err)
	}
	if len(res.InsertedPKs) != 3 {
		t.Fatalf("inserted pks: %+v", res.InsertedPKs)
	}
	firstID := res.InsertedPKs[0]["id"]
	if firstID == nil {
		t.Fatalf("no id in %+v", res.InsertedPKs)
	}

	p := f.page(id, "dbviewer_test_vals", "")
	if len(p.Rows) != 3 {
		t.Fatalf("rows: %d", len(p.Rows))
	}
	r0, r1 := p.Rows[0], p.Rows[1]
	get := func(r []any, c string) any { return r[colIndex(p, c)] }
	if e.exactDec && get(r0, "d") != "12345678901234.5678" {
		t.Errorf("decimal: %#v", get(r0, "d"))
	}
	if get(r0, "big") != e.bigValue {
		t.Errorf("big: %#v", get(r0, "big"))
	}
	if get(r0, "f") != 1.5 {
		t.Errorf("float: %#v", get(r0, "f"))
	}
	if ff, ok := get(r0, "ff").(float64); !ok || ff != 0.1 {
		t.Errorf("float32: %#v", get(r0, "ff"))
	}
	if dt, _ := get(r0, "dt").(string); !strings.HasPrefix(dt, "2025-01-31 13:45:00") {
		t.Errorf("datetime: %#v", get(r0, "dt"))
	}
	var j map[string]any
	if err := json.Unmarshal([]byte(get(r0, "j").(string)), &j); err != nil || j["a"] != 1.0 {
		t.Errorf("json: %#v", get(r0, "j"))
	}
	if b, ok := get(r0, "b").(query.Binary); !ok || b.Hex != "00ff10" || b.Size != 3 {
		t.Errorf("binary: %#v", get(r0, "b"))
	}
	if lt, ok := get(r0, "t").(query.LongText); !ok || !lt.Truncated || lt.Size != len(long) {
		t.Errorf("long text: %T", get(r0, "t"))
	}
	if get(r0, "s") != "" || get(r1, "s") != nil {
		t.Errorf("empty vs null: %#v %#v", get(r0, "s"), get(r1, "s"))
	}
	if get(r0, "flag") != true || get(r1, "flag") != false {
		t.Errorf("bool: %#v %#v", get(r0, "flag"), get(r1, "flag"))
	}
	if get(r1, "t") != "it's a \"quote\" \\ backslash" {
		t.Errorf("quotes: %#v", get(r1, "t"))
	}
	full, err := f.ds.CellValue(f.ctx, model.CellRequest{ConnID: id, Table: "dbviewer_test_vals", PK: map[string]any{"id": firstID}, Column: "t"})
	if err != nil || full != long {
		t.Fatalf("cell value: %v len=%d", err, len(full.(string)))
	}

	// update + delete in one transaction
	pk0 := map[string]any{"id": get(r0, "id")}
	pk1 := map[string]any{"id": get(r1, "id")}
	res, err = f.ds.ApplyChanges(f.ctx, model.ChangeSet{ConnID: id, Table: "dbviewer_test_vals",
		Updates: []model.RowUpdate{{PK: pk0, Set: map[string]any{"s": "changed", "d": "1.5000"}}},
		Deletes: []model.RowDelete{{PK: pk1}}})
	if err != nil || !res.OK || res.RowsAffected != 2 {
		t.Fatalf("update/delete: %+v %v", res, err)
	}
	// same value again: still exactly one matched row
	res, err = f.ds.ApplyChanges(f.ctx, model.ChangeSet{ConnID: id, Table: "dbviewer_test_vals",
		Updates: []model.RowUpdate{{PK: pk0, Set: map[string]any{"s": "changed"}}}})
	if err != nil || !res.OK {
		t.Fatalf("idempotent update: %+v %v", res, err)
	}
	// a failing statement rolls everything back
	res, err = f.ds.ApplyChanges(f.ctx, model.ChangeSet{ConnID: id, Table: "dbviewer_test_vals",
		Updates: []model.RowUpdate{
			{PK: pk0, Set: map[string]any{"s": "should be rolled back"}},
			{PK: map[string]any{"id": 999999}, Set: map[string]any{"s": "missing"}},
		}})
	if err != nil || res.OK || res.FailedIndex != 1 || !strings.Contains(res.Error, "no row") {
		t.Fatalf("expected failure at 1: %+v %v", res, err)
	}
	p = f.page(id, "dbviewer_test_vals", "")
	if len(p.Rows) != 2 || p.Rows[0][colIndex(p, "s")] != "changed" {
		t.Fatalf("after rollback: %+v", p.Rows)
	}
	// unknown column and pk mismatch are input errors
	if _, err := f.ds.ApplyChanges(f.ctx, model.ChangeSet{ConnID: id, Table: "dbviewer_test_vals",
		Updates: []model.RowUpdate{{PK: pk0, Set: map[string]any{"nope": 1}}}}); err == nil {
		t.Fatal("unknown column accepted")
	}
	if _, err := f.ds.ApplyChanges(f.ctx, model.ChangeSet{ConnID: id, Table: "customer_visitor_log",
		Deletes: []model.RowDelete{{PK: map[string]any{"visitor_id": 1}}}}); err == nil || !strings.HasPrefix(err.Error(), model.ErrNoPrimaryKey) {
		t.Fatalf("no-pk delete: %v", err)
	}
}

func testCopy(t *testing.T, f *fixture, id string) {
	out, err := f.ds.CopyAsSQL(f.ctx, model.CopySQLRequest{ConnID: id, Table: "store", Kind: "insert",
		Columns: []string{"store_id", "code", "name"}, Rows: [][]any{{1.0, "nl", "O'Brien"}, {2.0, nil, "x"}}})
	if err != nil || !strings.Contains(out, "VALUES (1, 'nl', 'O''Brien');") || !strings.Contains(out, "(2, NULL, 'x')") {
		t.Fatalf("copy insert: %q %v", out, err)
	}
	out, err = f.ds.CopyAsSQL(f.ctx, model.CopySQLRequest{ConnID: id, Table: "store", Kind: "update",
		Columns: []string{"store_id", "name"}, Rows: [][]any{{3.0, "Deutsch"}}})
	if err != nil || !strings.Contains(out, "SET") || !strings.Contains(out, "WHERE") || !strings.Contains(out, "= 3;") {
		t.Fatalf("copy update: %q %v", out, err)
	}
}

func testStructure(t *testing.T, f *fixture, id string) {
	def := "'n/a'"
	ch := model.StructureChange{ConnID: id, Table: "dbviewer_test_vals",
		AddColumns: []model.ColumnDef{{Name: "note", Type: "varchar(40)", Nullable: false, Default: &def}}}
	prev, err := f.ss.PreviewStructureChanges(f.ctx, ch)
	if err != nil || len(prev) != 1 || !strings.Contains(prev[0], "ADD COLUMN") {
		t.Fatalf("preview add: %v %v", prev, err)
	}
	if res, err := f.ss.ApplyStructureChanges(f.ctx, ch); err != nil || !res.OK {
		t.Fatalf("add: %+v %v", res, err)
	}
	res, err := f.ss.ApplyStructureChanges(f.ctx, model.StructureChange{ConnID: id, Table: "dbviewer_test_vals",
		RenameColumns: map[string]string{"note": "remark"}, DropColumns: []string{"ff"}})
	if err != nil || !res.OK {
		t.Fatalf("rename/drop: %+v %v", res, err)
	}
	cols, err := f.ss.Columns(f.ctx, id, "", "dbviewer_test_vals")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, c := range cols {
		names[c.Name] = true
	}
	if !names["remark"] || names["note"] || names["ff"] {
		t.Fatalf("columns after structure change: %v", names)
	}
	f.exec(id, "CREATE INDEX ix_dbv_test_s ON dbviewer_test_vals (s)")
	ixs, _ := f.ss.Indexes(f.ctx, id, "", "dbviewer_test_vals")
	if len(ixs) != 2 {
		t.Fatalf("indexes before drop: %+v", ixs)
	}
	if res, err := f.ss.ApplyStructureChanges(f.ctx, model.StructureChange{ConnID: id, Table: "dbviewer_test_vals", DropIndexes: []string{"ix_dbv_test_s"}}); err != nil || !res.OK {
		t.Fatalf("drop index: %+v %v", res, err)
	}
	if ixs, _ = f.ss.Indexes(f.ctx, id, "", "dbviewer_test_vals"); len(ixs) != 1 {
		t.Fatalf("indexes after drop: %+v", ixs)
	}
	if _, err := f.ss.PreviewStructureChanges(f.ctx, model.StructureChange{ConnID: id, Table: "dbviewer_test_vals",
		AddColumns: []model.ColumnDef{{Name: "evil", Type: "int; DROP TABLE store"}}}); err == nil {
		t.Fatal("injection in type accepted")
	}
}

func testExport(t *testing.T, f *fixture, id string, e engine) {
	dir := t.TempDir()
	for _, format := range []string{"csv", "tsv", "json", "sql", "markdown"} {
		path := filepath.Join(dir, "orders."+format)
		res, err := f.ds.Export(f.ctx, model.ExportRequest{ConnID: id, Format: format, Path: path, IncludeHeader: true,
			Page: &model.PageRequest{Table: "sales_order", Where: "entity_id <= 50"}, Columns: []string{"entity_id", "grand_total", "customer_note", "extra_data"}})
		if err != nil || res.Rows != 50 || res.Bytes == 0 {
			t.Fatalf("%s: %+v %v", format, res, err)
		}
		b, _ := os.ReadFile(path)
		switch format {
		case "json":
			var rows []map[string]any
			if err := json.Unmarshal(b, &rows); err != nil || len(rows) != 50 {
				t.Fatalf("json export invalid: %v", err)
			}
		case "sql":
			if strings.Count(string(b), "INSERT INTO") != 50 {
				t.Fatalf("sql export: %d inserts", strings.Count(string(b), "INSERT INTO"))
			}
		case "csv":
			if !strings.HasPrefix(string(b), "entity_id,grand_total,customer_note,extra_data\n") {
				t.Fatalf("csv header: %q", string(b[:60]))
			}
		}
	}
	res, err := f.ds.Export(f.ctx, model.ExportRequest{ConnID: id, Format: "csv", Path: filepath.Join(dir, "q.csv"),
		SQL: "SELECT entity_id, increment_id FROM sales_order ORDER BY entity_id"})
	if err != nil || res.Rows != 2000 {
		t.Fatalf("sql source: %+v %v", res, err)
	}
}

func testFind(t *testing.T, f *fixture, id string) {
	res, err := f.ds.FindInDatabase(f.ctx, model.FindRequest{ConnID: id, SearchID: "s1", Term: "Ring twice",
		Tables: []string{"sales_order", "store", "customer_entity"}, LimitPerTable: 5})
	if err != nil || len(res) != 1 || res[0].Table != "sales_order" || len(res[0].Result.Rows) != 5 || !res[0].Result.HasMore {
		t.Fatalf("find: %+v %v", res, err)
	}
	if len(res[0].MatchedColumns) != 1 || res[0].MatchedColumns[0] != "customer_note" {
		t.Fatalf("matched columns: %v", res[0].MatchedColumns)
	}
	all, err := f.ds.FindInDatabase(f.ctx, model.FindRequest{ConnID: id, SearchID: "s2", Term: "admin@shop.test", Mode: "exact"})
	if err != nil || len(all) == 0 {
		t.Fatalf("find all tables: %v %v", all, err)
	}
}

func testSessions(t *testing.T, f *fixture, id string, e engine) {
	run := func(sql string) []model.QueryResult {
		t.Helper()
		r, err := f.qs.RunQuery(f.ctx, model.QueryRequest{ConnID: id, SQL: sql, SessionID: "tab-1", QueryID: "q"})
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range r {
			if x.Error != "" {
				t.Fatalf("%s: %s", sql, x.Error)
			}
		}
		return r
	}
	// a transaction spans runs and can be rolled back
	run("BEGIN")
	run("INSERT INTO dbviewer_test_vals (s) VALUES ('in tx')")
	r := run("SELECT COUNT(*) AS n FROM dbviewer_test_vals WHERE s = 'in tx'")
	if n, _ := r[0].Rows[0][0].(int64); n != 1 {
		t.Fatalf("inside tx: %#v", r[0].Rows)
	}
	run("ROLLBACK")
	r = run("SELECT COUNT(*) AS n FROM dbviewer_test_vals WHERE s = 'in tx'")
	if n, _ := r[0].Rows[0][0].(int64); n != 0 {
		t.Fatalf("after rollback: %#v", r[0].Rows)
	}
	switch e.name {
	case "mysql":
		run("SET @dbv = 42")
		if r := run("SELECT @dbv AS v"); r[0].Rows[0][0] != int64(42) {
			t.Fatalf("session var: %#v", r[0].Rows)
		}
	case "postgres":
		run("SET search_path TO reporting")
		if r := run("SELECT COUNT(*) FROM daily_sales"); len(r[0].Rows) != 1 {
			t.Fatal("search_path not kept")
		}
	case "sqlite":
		run("CREATE TEMP TABLE dbv_tmp (x)")
		run("INSERT INTO dbv_tmp VALUES (7)")
		if r := run("SELECT x FROM dbv_tmp"); r[0].Rows[0][0] != int64(7) {
			t.Fatalf("temp table: %#v", r[0].Rows)
		}
	}
	if err := f.qs.CloseSession("tab-1"); err != nil {
		t.Fatal(err)
	}
	if e.name == "mysql" {
		r, _ := f.qs.RunQuery(f.ctx, model.QueryRequest{ConnID: id, SQL: "SELECT @dbv AS v", SessionID: "tab-1"})
		if r[0].Rows[0][0] != nil {
			t.Fatalf("session not closed: %#v", r[0].Rows)
		}
	}
}

func testCancel(t *testing.T, f *fixture, id string, e engine) {
	start := time.Now()
	go func() {
		time.Sleep(400 * time.Millisecond)
		f.qs.CancelQuery("slow")
	}()
	r, err := f.qs.RunQuery(f.ctx, model.QueryRequest{ConnID: id, SQL: e.sleepSQL, QueryID: "slow", SessionID: "tab-c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 1 || !strings.HasPrefix(r[0].Error, model.ErrCanceled) {
		t.Fatalf("cancel: %+v", r)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("cancel took %s", el)
	}
	// the session keeps working
	r, _ = f.qs.RunQuery(f.ctx, model.QueryRequest{ConnID: id, SQL: "SELECT 1", SessionID: "tab-c"})
	if len(r) != 1 || r[0].Error != "" {
		t.Fatalf("after cancel: %+v", r)
	}
}

func testSearch(t *testing.T, f *fixture, id string) {
	hits, err := f.ss.SearchObjects(f.ctx, f.pid, "custemail", 20)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %v %v", hits, err)
	}
	if hits[0].Kind != "column" || hits[0].Column != "customer_email" {
		t.Fatalf("top hit: %+v", hits[0])
	}
	hits, _ = f.ss.SearchObjects(f.ctx, f.pid, "sales_order", 5)
	if len(hits) == 0 || hits[0].Table != "sales_order" || hits[0].Kind != "table" {
		t.Fatalf("exact table hit: %+v", hits)
	}
}

func testReadOnly(t *testing.T, f *fixture, e engine) {
	cfg := e.cfg
	cfg.ID, cfg.Name, cfg.ReadOnly = "", e.name+" ro", true
	ro := f.save(cfg)
	// service layer
	_, err := f.ds.ApplyChanges(f.ctx, model.ChangeSet{ConnID: ro.ID, Table: "store",
		Updates: []model.RowUpdate{{PK: map[string]any{"store_id": 1}, Set: map[string]any{"name": "x"}}}})
	if err == nil || !strings.HasPrefix(err.Error(), model.ErrReadOnly) {
		t.Fatalf("apply on ro: %v", err)
	}
	if _, err := f.ss.ApplyStructureChanges(f.ctx, model.StructureChange{ConnID: ro.ID, Table: "store", DropColumns: []string{"sort_order"}}); err == nil {
		t.Fatal("structure change on ro accepted")
	}
	// console
	for _, sql := range []string{
		"UPDATE store SET name = 'x' WHERE store_id = 1",
		"DELETE FROM store",
		"SET SESSION transaction_read_only = 0",
		"SET default_transaction_read_only = off",
		"BEGIN READ WRITE",
		"SELECT set_config('default_transaction_read_only', 'off', false)",
		"PRAGMA query_only = 0",
		"CREATE TABLE x (id int)",
	} {
		r, err := f.qs.RunQuery(f.ctx, model.QueryRequest{ConnID: ro.ID, SQL: sql, SessionID: "ro-tab"})
		if err != nil || len(r) != 1 || !strings.HasPrefix(r[0].Error, model.ErrReadOnly) {
			t.Fatalf("console %q on ro: %+v %v", sql, r, err)
		}
	}
	r, err := f.qs.RunQuery(f.ctx, model.QueryRequest{ConnID: ro.ID, SQL: "SELECT COUNT(*) FROM store", SessionID: "ro-tab"})
	if err != nil || r[0].Error != "" {
		t.Fatalf("select on ro: %+v %v", r, err)
	}
	// session layer: a write that bypasses the service checks still fails in the DB
	p, err := f.core.Conns.Get(f.ctx, ro.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DB.ExecContext(f.ctx, "UPDATE store SET name = name WHERE store_id = 1"); err == nil {
		t.Fatal("database session accepted a write on a read-only connection")
	}
	// WHERE injection of session changes is refused
	if _, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: ro.ID, Table: "store",
		Where: "set_config('default_transaction_read_only','off',false) IS NOT NULL"}); err == nil {
		t.Fatal("set_config in where accepted on ro")
	}
}

func TestUseDatabase(t *testing.T) {
	if _, err := os.Stat("/opt/workbench/data/run/mysqld/mysql84.sock"); err != nil {
		t.Skip("mysql socket not available")
	}
	f := newFixture(t)
	empty := ""
	c := f.save(model.ConnectionConfig{Name: "m", Driver: model.DriverMySQL, Socket: "/opt/workbench/data/run/mysqld/mysql84.sock", User: "root", Password: &empty, Database: "dbviewer_dummy"})
	st, err := f.cs.UseDatabase(f.ctx, c.ID, "dbviewer_dummy_ro")
	if err != nil || st.Database != "dbviewer_dummy_ro" {
		t.Fatalf("use: %+v %v", st, err)
	}
	tables, err := f.ss.Tables(f.ctx, c.ID, "", false)
	if err != nil || len(tables) != 16 {
		t.Fatalf("tables after use: %d %v", len(tables), err)
	}
	pw := "dbviewer"
	pg := f.save(model.ConnectionConfig{Name: "p", Driver: model.DriverPostgres, Host: "127.0.0.1", Port: 55432, User: "postgres", Password: &pw, Database: "dbviewer_dummy", SSLMode: model.SSLDisable})
	if st, err := f.cs.UseDatabase(f.ctx, pg.ID, "postgres"); err != nil || st.Database != "postgres" {
		t.Fatalf("pg use: %+v %v", st, err)
	}
	dbs, err := f.ss.Databases(f.ctx, pg.ID)
	if err != nil || len(dbs) < 2 {
		t.Fatalf("databases: %v %v", dbs, err)
	}
}
