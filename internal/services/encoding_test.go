package services

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"dbviewer/internal/model"
)

func TestPostgresTypes(t *testing.T) {
	f := newFixture(t)
	pw := "dbviewer"
	c := f.save(model.ConnectionConfig{Name: "pg", Driver: model.DriverPostgres, Host: "127.0.0.1", Port: 55432, User: "postgres", Password: &pw, Database: "dbviewer_dummy", SSLMode: model.SSLDisable})
	page, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: c.ID, Schema: "reporting", Table: "export_log", Where: "rows_written IS NOT NULL", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	row := page.Rows[0]
	get := func(n string) any { return row[colIndex(page, n)] }
	if ts, _ := get("run_at").(string); !regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d(\.\d+)?[+-]\d\d:\d\d$`).MatchString(ts) {
		t.Errorf("timestamptz: %#v", get("run_at"))
	}
	if j, _ := get("meta").(string); !strings.HasPrefix(j, "{") {
		t.Errorf("jsonb: %#v", get("meta"))
	}
	if a, _ := get("tags").(string); !strings.HasPrefix(a, "{nightly,") {
		t.Errorf("text[]: %#v", get("tags"))
	}
	if u, _ := get("run_id").(string); len(u) != 36 {
		t.Errorf("uuid: %#v", get("run_id"))
	}
	if _, ok := get("id").(int64); !ok {
		t.Errorf("bigint identity: %#v", get("id"))
	}
	d, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: c.ID, Schema: "reporting", Table: "daily_sales", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if day, _ := d.Rows[0][0].(string); !regexp.MustCompile(`^\d{4}-\d\d-\d\d$`).MatchString(day) {
		t.Errorf("date: %#v", d.Rows[0][0])
	}
	if rev, _ := d.Rows[0][2].(string); !regexp.MustCompile(`^\d+\.\d{4}$`).MatchString(rev) {
		t.Errorf("numeric: %#v", d.Rows[0][2])
	}
	// console path (driver type names)
	r, err := f.qs.RunQuery(f.ctx, model.QueryRequest{ConnID: c.ID, SQL: "SELECT 12345678901234.5678::numeric(20,4) AS n, 0.1::real AS r, true AS b, '\\x00ff'::bytea AS y, NULL::text AS z, ''::text AS e, 9223372036854775807::bigint AS big"})
	if err != nil || r[0].Error != "" {
		t.Fatalf("%+v %v", r, err)
	}
	row = r[0].Rows[0]
	if row[0] != "12345678901234.5678" || row[1] != 0.1 || row[2] != true || row[4] != nil || row[5] != "" || row[6] != "9223372036854775807" {
		t.Fatalf("console values: %#v", row)
	}
}

func TestMySQLBinaryProtocol(t *testing.T) {
	if _, err := os.Stat("/opt/workbench/data/run/mysqld/mysql84.sock"); err != nil {
		t.Skip("mysql socket not available")
	}
	f := newFixture(t)
	empty := ""
	c := f.save(model.ConnectionConfig{Name: "m", Driver: model.DriverMySQL, Socket: "/opt/workbench/data/run/mysqld/mysql84.sock", User: "root", Password: &empty, Database: "dbviewer_dummy"})
	// column filters bind parameters => server-side prepared statement (binary protocol)
	p, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: c.ID, Table: "sales_order", Limit: 5,
		ColumnFilters: []model.ColumnFilter{{Column: "entity_id", Op: "lte", Value: "5"}}})
	if err != nil {
		t.Fatal(err)
	}
	row := p.Rows[0]
	get := func(n string) any { return row[colIndex(p, n)] }
	if gt, _ := get("grand_total").(string); !regexp.MustCompile(`^\d+\.\d{4}$`).MatchString(gt) {
		t.Errorf("decimal: %#v", get("grand_total"))
	}
	if dt, _ := get("created_at").(string); !regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d$`).MatchString(dt) {
		t.Errorf("datetime: %#v", get("created_at"))
	}
	if _, ok := get("customer_is_guest").(bool); !ok {
		t.Errorf("tinyint(1): %#v", get("customer_is_guest"))
	}
	if _, ok := get("entity_id").(int64); !ok {
		t.Errorf("int: %#v", get("entity_id"))
	}
	r, err := f.qs.RunQuery(f.ctx, model.QueryRequest{ConnID: c.ID, SQL: "SELECT CAST(18446744073709551615 AS UNSIGNED) AS u, CAST(1.5 AS DECIMAL(10,4)) AS d, NULL AS n, '' AS e"})
	if err != nil || r[0].Error != "" {
		t.Fatalf("%+v %v", r, err)
	}
	if row := r[0].Rows[0]; row[0] != "18446744073709551615" || row[1] != "1.5000" || row[2] != nil || row[3] != "" {
		t.Fatalf("console values: %#v", row)
	}
}
