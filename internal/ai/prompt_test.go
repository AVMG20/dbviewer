package ai

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"dbviewer/internal/model"
)

func col(name string) model.Column { return model.Column{Name: name, Type: "int"} }
func pk(name string) model.Column  { return model.Column{Name: name, Type: "int", PK: true} }
func fk(name, table, column string) model.Column {
	return model.Column{Name: name, Type: "int", FK: &model.FKRef{Table: table, Column: column}}
}

// magentoTables is a Magento-like schema: main tables, their helper tables and
// enough unrelated tables that PickTables has to choose.
func magentoTables() []TableInfo {
	ts := []TableInfo{
		{Name: "sales_order", Columns: []model.Column{pk("entity_id"), col("increment_id"), col("state"), fk("store_id", "store", "store_id"),
			fk("customer_id", "customer_entity", "entity_id"), col("customer_email"), col("grand_total"), col("created_at")}},
		{Name: "sales_order_grid", Columns: []model.Column{pk("entity_id"), col("increment_id"), col("customer_id"), col("customer_email"),
			col("customer_name"), col("grand_total"), col("created_at")}},
		{Name: "sales_order_item", Columns: []model.Column{pk("item_id"), fk("order_id", "sales_order", "entity_id"),
			fk("product_id", "catalog_product_entity", "entity_id"), col("sku"), col("qty_ordered")}},
		{Name: "report_product_map", Columns: []model.Column{pk("id"), col("code"), col("amount")}},
		{Name: "widget_product_option", Columns: []model.Column{pk("id"), col("value")}},
		{Name: "sales_order_address", Columns: []model.Column{pk("entity_id"), fk("parent_id", "sales_order", "entity_id"), col("customer_id"), col("email")}},
		{Name: "sales_order_status_history", Columns: []model.Column{pk("entity_id"), fk("parent_id", "sales_order", "entity_id"), col("comment")}},
		{Name: "sales_invoice", Columns: []model.Column{pk("entity_id"), fk("order_id", "sales_order", "entity_id"), col("grand_total")}},
		{Name: "customer_entity", Columns: []model.Column{pk("entity_id"), col("email"), col("firstname"), col("lastname"), col("created_at")}},
		{Name: "customer_grid_flat", Columns: []model.Column{pk("entity_id"), col("email"), col("name")}},
		{Name: "customer_address_entity", Columns: []model.Column{pk("entity_id"), fk("parent_id", "customer_entity", "entity_id"), col("city")}},
		{Name: "customer_entity_varchar", Columns: []model.Column{pk("value_id"), col("entity_id"), col("value")}},
		{Name: "quote", Columns: []model.Column{pk("entity_id"), col("customer_id"), col("customer_email")}},
		{Name: "store", Columns: []model.Column{pk("store_id"), col("code"), col("name")}},
		{Name: "catalog_product_entity", Columns: []model.Column{pk("entity_id"), col("sku")}},
	}
	for i := 0; i < 980; i++ {
		ts = append(ts, TableInfo{Name: fmt.Sprintf("vendor%d_misc_%d", i%40, i), Columns: []model.Column{pk("id"), col("value")}})
	}
	return ts
}

func names(ts []*TableInfo) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func index(list []string, name string) int {
	for i, n := range list {
		if n == name {
			return i
		}
	}
	return -1
}

func TestPickTablesMagento(t *testing.T) {
	tables := magentoTables()
	got := names(PickTables("orders from last week with the customer email", "", "", tables))
	t.Logf("picked: %v", got)
	if len(got) == 0 || len(got) > maxTables {
		t.Fatalf("picked %d tables", len(got))
	}
	so, ce, grid := index(got, "sales_order"), index(got, "customer_entity"), index(got, "sales_order_grid")
	if so < 0 || ce < 0 {
		t.Fatalf("sales_order and customer_entity must be picked: %v", got)
	}
	if grid >= 0 && (grid < so || grid < ce) {
		t.Errorf("sales_order_grid ranks above the main tables: %v", got)
	}
	for _, n := range got {
		if strings.HasPrefix(n, "vendor") {
			t.Errorf("unrelated table %s picked", n)
		}
	}
}

func TestPickTablesProducts(t *testing.T) {
	got := names(PickTables("top 5 products by quantity ordered", "", "", magentoTables()))
	t.Logf("picked: %v", got)
	soi, cpe := index(got, "sales_order_item"), index(got, "catalog_product_entity")
	if soi < 0 || cpe < 0 {
		t.Fatalf("sales_order_item and catalog_product_entity must be picked: %v", got)
	}
	for _, junk := range []string{"report_product_map", "widget_product_option"} {
		if i := index(got, junk); i >= 0 && (i < soi || i < cpe) {
			t.Errorf("%s ranks above the main tables: %v", junk, got)
		}
	}
}

func TestPickTablesExplicitAndQuery(t *testing.T) {
	tables := magentoTables()
	// a table named exactly in the prompt wins
	got := names(PickTables("count rows in vendor3_misc_83 per value", "", "", tables))
	if len(got) == 0 || got[0] != "vendor3_misc_83" {
		t.Errorf("explicit table not first: %v", got)
	}
	// the tables of the query being changed are kept, even if the prompt does not name them
	got = names(PickTables("only the last 10", "SELECT * FROM `catalog_product_entity` p", "", tables))
	if index(got, "catalog_product_entity") != 0 {
		t.Errorf("table of the current query not first: %v", got)
	}
	// FK neighbours of the best match are added
	got = names(PickTables("invoices", "", "", tables))
	if index(got, "sales_invoice") < 0 || index(got, "sales_order") < 0 {
		t.Errorf("FK neighbour missing: %v", got)
	}
}

func TestPickTablesSmallSchemaWhole(t *testing.T) {
	small := magentoTables()[:10]
	if got := PickTables("anything", "", "", small); len(got) != 10 {
		t.Errorf("small schema: got %d tables, want all 10", len(got))
	}
}

func TestSchemaText(t *testing.T) {
	tables := magentoTables()
	picked := PickTables("orders with customer email", "", "", tables)
	text, shown := SchemaText(picked, "orders with customer email")
	if len(shown) != len(picked) {
		t.Errorf("shown %d of %d", len(shown), len(picked))
	}
	if !strings.Contains(text, "sales_order(entity_id int PK,") || !strings.Contains(text, "customer_id int -> customer_entity.entity_id") {
		t.Errorf("unexpected schema text:\n%s", text)
	}
}

func TestMessages(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	m := Messages(model.DriverSQLite, "orders last week", "", "sales_order(entity_id int PK)\n", 995, 8, now)
	if len(m) != 2 || m[0].Role != "system" || !strings.Contains(m[0].Content, "SQLite") {
		t.Fatalf("bad messages: %+v", m)
	}
	if !strings.Contains(m[1].Content, "out of 995") || !strings.Contains(m[1].Content, "2026-11-20 (Friday)") || !strings.Contains(m[1].Content, "Write a query for: orders last week") {
		t.Errorf("bad user message:\n%s", m[1].Content)
	}
	m = Messages(model.DriverMySQL, "add the email", "SELECT id FROM t", "", 3, 3, now)
	if !strings.Contains(m[1].Content, "Current query:\n```sql\nSELECT id FROM t\n```") || strings.Contains(m[1].Content, "out of") {
		t.Errorf("bad change message:\n%s", m[1].Content)
	}
}

func TestExtractSQL(t *testing.T) {
	cases := []struct {
		reply    string
		sql      string
		thinking bool
	}{
		{"```sql\nSELECT 1;\n```", "SELECT 1;", false},
		{"Here you go:\n```sql\nSELECT *\nFROM t\n```\nThis selects everything.", "SELECT *\nFROM t", false},
		{"```sql\nSELECT a,\n  b", "SELECT a,\n  b", false}, // streaming
		{"```sql\nSELECT a\n``", "SELECT a", false},         // closing fence half streamed
		{"```sq", "", false},                      // fence line not complete yet
		{"```\nselect 2\n```", "select 2", false}, // fence without language
		{"<think>the user wants", "", true},       // still thinking
		{"<think>select x</think>\n```sql\nSELECT 3\n```", "SELECT 3", false},
		{"plan: select x</think>\nSELECT 4", "SELECT 4", false}, // template opened the think block
		{"Sure! The query is:\nSELECT id FROM t WHERE x = 1", "SELECT id FROM t WHERE x = 1", false},
		{"WITH a AS (SELECT 1) SELECT * FROM a", "WITH a AS (SELECT 1) SELECT * FROM a", false},
		{"I can't help with that.", "", false},
	}
	for _, c := range cases {
		sql, thinking := ExtractSQL(c.reply)
		if sql != c.sql || thinking != c.thinking {
			t.Errorf("ExtractSQL(%q) = %q, %v; want %q, %v", c.reply, sql, thinking, c.sql, c.thinking)
		}
	}
}
