package ai

import (
	"strings"
	"testing"
	"time"

	"dbviewer/internal/model"
)

func TestExtractFilter(t *testing.T) {
	cases := []struct {
		dialect, reply, where, order string
		err                          bool
	}{
		{"sqlite", "```sql\nSELECT * FROM sales_order WHERE status <> 'canceled' AND grand_total > 100;\n```", "status <> 'canceled' AND grand_total > 100", "", false},
		{"mysql", "```sql\nSELECT * FROM sales_order\nWHERE created_at >= NOW() - INTERVAL 7 DAY\nORDER BY grand_total DESC\nLIMIT 50;\n```", "created_at >= NOW() - INTERVAL 7 DAY", "grand_total DESC", false},
		{"sqlite", "```sql\nSELECT * FROM t\nWHERE a = 1\n  AND b = 2\nORDER BY\n  c\n```", "a = 1 AND b = 2", "c", false},
		// alias removed, subquery kept intact (its WHERE / ORDER BY are not top-level)
		{"postgres", "SELECT * FROM sales_order o WHERE o.customer_id IN (SELECT entity_id FROM customer_entity c WHERE c.email LIKE '%@x.nl' ORDER BY 1) ORDER BY o.created_at DESC", "customer_id IN (SELECT entity_id FROM customer_entity c WHERE c.email LIKE '%@x.nl' ORDER BY 1)", "created_at DESC", false},
		{"sqlite", "SELECT * FROM sales_order AS so WHERE so.state = 'new'", "state = 'new'", "", false},
		// bare conditions
		{"sqlite", "WHERE status = 'pending'", "status = 'pending'", "", false},
		{"sqlite", "```sql\nstatus = 'pending' ORDER BY id\n```", "status = 'pending'", "id", false},
		// only a sort
		{"sqlite", "SELECT * FROM t ORDER BY created_at DESC", "", "created_at DESC", false},
		// strings containing keywords stay intact
		{"sqlite", "SELECT * FROM t WHERE note = 'order by me; limit' AND x = 1", "note = 'order by me; limit' AND x = 1", "", false},
		{"mysql", "SELECT * FROM `t` WHERE `t`.`status` = 'x'", "`t`.`status` = 'x'", "", false},
		{"sqlite", "SELECT o.* FROM sales_order o JOIN store s ON s.store_id = o.store_id WHERE s.code = 'nl'", "", "", true},
		{"sqlite", "SELECT * FROM t", "", "", true},
		{"sqlite", "I cannot help with that.", "", "", true},
	}
	for _, c := range cases {
		w, o, err := ExtractFilter(c.dialect, c.reply)
		if (err != nil) != c.err || w != c.where || o != c.order {
			t.Errorf("ExtractFilter(%q)\n got  %q | %q | %v\n want %q | %q | err=%v", c.reply, w, o, err, c.where, c.order, c.err)
		}
	}
}

func TestExtractTitle(t *testing.T) {
	cases := map[string]string{
		"Paid orders per month":                       "Paid orders per month",
		"\"Paid orders per month.\"":                  "Paid orders per month",
		"**Title:** customers without orders\n":       "Customers without orders",
		"<think>hmm</think>\nTop 5 products by sales": "Top 5 products by sales",
		"Name: `Recent invoices`":                     "Recent invoices",
		"":                                            "",
		"A very long title that goes on and on describing every single detail of the query": "A very long title that goes on and on describing every",
	}
	for in, want := range cases {
		if got := ExtractTitle(in); got != want {
			t.Errorf("ExtractTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilterCandidates(t *testing.T) {
	cols := []model.Column{
		{Name: "entity_id", Kind: model.KindInt, PK: true},
		{Name: "increment_id", Type: "varchar(50)", Kind: model.KindString},
		{Name: "state", Type: "varchar(32)", Kind: model.KindString},
		{Name: "status", Type: "varchar(32)", Kind: model.KindString},
		{Name: "customer_email", Type: "varchar(128)", Kind: model.KindString},
		{Name: "base_currency_code", Type: "varchar(3)", Kind: model.KindString},
		{Name: "customer_note", Type: "text", Kind: model.KindText},
		{Name: "shipping_method", Type: "varchar(255)", Kind: model.KindString},
		{Name: "payment_type", Type: "enum('card','cash')", Kind: model.KindString},
		{Name: "carrier_code", Type: "varchar(32)", Kind: model.KindString},
	}
	names := func(cs []model.Column) string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}
	// the column the prompt names comes first; email, long varchar, text and enum columns are skipped
	if got := names(FilterCandidates(cols, "orders paid in EUR by carrier, customer email at gmail", 4)); got != "carrier_code,state,status,base_currency_code" {
		t.Errorf("candidates: %s", got)
	}
	if got := names(FilterCandidates(cols, "anything", 2)); got != "state,status" {
		t.Errorf("max: %s", got)
	}
}

func TestValuesText(t *testing.T) {
	got := ValuesText([]string{"status", "none"}, map[string][]string{"status": {"canceled", "it's"}})
	if got != "status: 'canceled', 'it''s'\n" {
		t.Errorf("ValuesText = %q", got)
	}
}

func TestTaskMessages(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	m := FixMessages(model.DriverMySQL, "SELECT nme FROM t", "Unknown column 'nme' in 'field list'", "t(id int PK, name varchar(20))\n", now)
	if !strings.Contains(m[0].Content, "MySQL") || !strings.Contains(m[1].Content, "Schema:\nt(id int PK") || !strings.Contains(m[1].Content, "Error: Unknown column 'nme'") {
		t.Errorf("fix: %+v", m)
	}
	m = ConvertMessages(model.DriverMySQL, model.DriverPostgres, "SELECT `a` FROM t LIMIT 1")
	if !strings.Contains(m[0].Content, "from MySQL to PostgreSQL") || !strings.Contains(m[0].Content, "never backticks") {
		t.Errorf("convert: %+v", m)
	}
	m = ExplainMessages(model.DriverSQLite, "SELECT 1", "")
	if strings.Contains(m[1].Content, "Schema:") || !strings.Contains(m[0].Content, "3 to 5") {
		t.Errorf("explain: %+v", m)
	}
	tables := PlanTablesText([]PlanTable{{Name: "sales_order", Rows: 1234567, Indexes: []model.Index{{Name: "PRIMARY", Primary: true, Columns: []string{"entity_id"}}, {Name: "ix_status", Columns: []string{"status"}}}, Columns: []string{"entity_id", "status"}}})
	if tables != "- sales_order (~1,234,567 rows): indexes PRIMARY(entity_id), ix_status(status); columns used by the query: entity_id, status\n" {
		t.Errorf("plan tables: %q", tables)
	}
	m = PlanMessages(model.DriverMySQL, "SELECT * FROM sales_order WHERE customer_email = 'x'", "id\ttable\ttype\n1\tsales_order\tALL", tables)
	if !strings.Contains(m[1].Content, "EXPLAIN output:\nid\ttable") || !strings.Contains(m[1].Content, "Tables (estimated rows") {
		t.Errorf("plan: %+v", m)
	}
	m = FilterMessages(model.DriverSQLite, "sales_order", "sales_order(status varchar(32))\n", "status: 'canceled'\n", "not cancelled", now)
	if !strings.Contains(m[0].Content, `"SELECT * FROM sales_order WHERE …"`) || !strings.Contains(m[1].Content, "Values:\nstatus: 'canceled'") || !strings.Contains(m[1].Content, "Request: not cancelled") {
		t.Errorf("filter: %+v", m)
	}
	if n := NameMessages("SELECT 1"); !strings.Contains(n[0].Content, "2 to 5 words") {
		t.Errorf("name: %+v", n)
	}
}
