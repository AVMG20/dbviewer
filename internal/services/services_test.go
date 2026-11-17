package services

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"dbviewer/internal/conn"
	"dbviewer/internal/model"
)

// Integration smoke test against the seeded dummy databases (testdata/seed.sh).
// Engines that are not reachable are skipped.

func testCore(t *testing.T) *Core {
	t.Helper()
	t.Setenv("DBVIEWER_DATA_DIR", t.TempDir())
	// never read the real ~/.ssh (config, keys, agent) in tests
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	conn.LaunchctlAgentSocket = func() string { return "" }
	c, err := NewCore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func repoRoot() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "..", "..")
}

func TestEngines(t *testing.T) {
	c := testCore(t)
	ctx := context.Background()
	ps := NewProjectService(c)
	cs := NewConnectionService(c)
	ss := NewSchemaService(c)
	ds := NewDataService(c)
	qs := NewQueryService(c)

	projects, err := ps.Projects()
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects: %v %v", projects, err)
	}
	pid := projects[0].ID
	empty := ""
	pgPw := "dbviewer"
	cfgs := []model.ConnectionConfig{
		{ProjectID: pid, Name: "sqlite", Driver: model.DriverSQLite, FilePath: filepath.Join(repoRoot(), "testdata", "dummy.sqlite")},
		{ProjectID: pid, Name: "mysql", Driver: model.DriverMySQL, Socket: "/opt/workbench/data/run/mysqld/mysql84.sock", User: "root", Password: &empty, Database: "dbviewer_dummy"},
		{ProjectID: pid, Name: "pg", Driver: model.DriverPostgres, Host: "127.0.0.1", Port: 55432, User: "postgres", Password: &pgPw, Database: "dbviewer_dummy", SSLMode: model.SSLDisable},
		{ProjectID: pid, Name: "mysql ro", Driver: model.DriverMySQL, Socket: "/opt/workbench/data/run/mysqld/mysql84.sock", User: "root", Database: "dbviewer_dummy_ro", ReadOnly: true, Color: model.ColorRed},
	}
	for _, cfg := range cfgs {
		t.Run(cfg.Name, func(t *testing.T) {
			if cfg.Driver == model.DriverMySQL {
				if _, err := os.Stat(cfg.Socket); err != nil {
					t.Skip("mysql socket not available")
				}
			}
			saved, err := cs.SaveConnection(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cs.TestConnection(ctx, saved); err != nil {
				t.Fatalf("connect failed (run testdata/seed.sh first): %v", err)
			}
			tables, err := ss.Tables(ctx, saved.ID, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if len(tables) < 16 {
				t.Fatalf("expected tables, got %d", len(tables))
			}
			idx, err := ss.SearchSchema(ctx, saved.ID, "", false)
			if err != nil || len(idx.Tables) != len(tables) {
				t.Fatalf("index: %d tables, err %v", len(idx.Tables), err)
			}
			page, err := ds.FetchPage(ctx, model.PageRequest{ConnID: saved.ID, Table: "sales_order", Limit: 100,
				Where: "grand_total > 0", ColumnFilters: []model.ColumnFilter{{Column: "status", Op: "ne", Value: "canceled"}}, CountTotal: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Rows) != 100 || !page.HasMore || page.Total <= 100 {
				t.Fatalf("page: rows=%d hasMore=%v total=%d", len(page.Rows), page.HasMore, page.Total)
			}
			var pkCol, fkCol bool
			for _, col := range page.Columns {
				pkCol = pkCol || (col.Name == "entity_id" && col.PK)
				fkCol = fkCol || (col.Name == "customer_id" && col.FK != nil && col.FK.Table == "customer_entity")
			}
			if !pkCol || !fkCol {
				t.Fatalf("pk/fk info missing: %+v", page.Columns)
			}
			res, err := qs.RunQuery(ctx, model.QueryRequest{ConnID: saved.ID, QueryID: "q1",
				SQL: "SELECT COUNT(*) AS n FROM sales_order; SELECT entity_id, grand_total, extra_data FROM sales_order ORDER BY entity_id LIMIT 3"})
			if err != nil || len(res) != 2 || res[0].Error != "" || len(res[1].Rows) != 3 {
				t.Fatalf("run: %+v %v", res, err)
			}
			if saved.ReadOnly {
				res, err := qs.RunQuery(ctx, model.QueryRequest{ConnID: saved.ID, SQL: "UPDATE store SET name = 'x' WHERE store_id = 1"})
				if err != nil || len(res) != 1 || res[0].Error == "" {
					t.Fatalf("read-only write not blocked: %+v %v", res, err)
				}
			}
		})
	}
}
