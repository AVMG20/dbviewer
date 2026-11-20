package services

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"dbviewer/internal/model"
)

type completeCase struct {
	text   string   // "|" marks the cursor
	expect []string // a good suggestion contains one of these (lowercase); empty = anything non-empty
}

// sqlite dummy (Magento-like, ~270 tables)
var completeCasesDummy = []completeCase{
	{"SELECT o.increment_id, c.email\nFROM sales_order o\nJOIN customer_entity c |", []string{"on o.customer_id = c.entity_id", "on c.entity_id = o.customer_id"}},
	{"SELECT * FROM sales_order WHERE |", []string{"status", "state", "created_at", "customer", "grand_total", "increment_id", "store_id"}},
	{"SELECT * FROM sales_order_item i JOIN sales_order o ON |", []string{"i.order_id = o.entity_id", "o.entity_id = i.order_id"}},
	{"SELECT sku, SUM(qty_ordered) FROM sales_order_item GROUP BY |", []string{"sku"}},
	{"SELECT * FROM customer_entity WHERE em|", []string{"ail"}},
	{"SELECT COUNT(*) FROM sales_ord|", []string{"er"}},
	{"SELECT o.entity_id, o.grand_total FROM sales_order o ORDER BY |", []string{"grand_total", "created_at", "entity_id"}},
	{"SELECT p.sku, v.value\nFROM catalog_product_entity p\nJOIN catalog_product_entity_varchar v ON |", []string{"v.entity_id = p.entity_id", "p.entity_id = v.entity_id", "row_id"}},
	{"SELECT * FROM sales_order WHERE created_at >= date('now', |", []string{"day", "month", "'-"}},
	{"SELECT store_id, COUNT(*) AS orders\nFROM sales_order\n|", []string{"group by store_id"}},
	{"UPDATE sales_order SET status = 'canceled' WHERE |", []string{"entity_id", "increment_id", "status", "state"}},
	{"SELECT *\nFROM sales_order o\nWHERE o.customer_id IN (SELECT entity_id FROM customer_entity WHERE |)", []string{"email", "store", "created", "firstname", "lastname", "gender", "dob", "group"}},
}

// Magento (MySQL, ~1400 tables)
var completeCasesMagento = []completeCase{
	{"SELECT o.increment_id, a.city\nFROM sales_order o\nJOIN sales_order_address a |", []string{"on a.parent_id = o.entity_id", "on o.entity_id = a.parent_id"}},
	{"SELECT * FROM sales_order WHERE |", []string{"status", "state", "created_at", "customer", "grand_total", "increment_id", "store_id"}},
	{"SELECT e.sku, v.value\nFROM catalog_product_entity e\nJOIN catalog_product_entity_varchar v ON |", []string{"v.entity_id = e.entity_id", "e.entity_id = v.entity_id", "row_id"}},
	{"SELECT path, value FROM core_config_data WHERE path LIKE |", []string{"'"}},
	{"SELECT * FROM customer_entity WHERE email = |", []string{"'"}},
	{"SELECT COUNT(*) FROM quote WHERE is_active = 1 AND |", []string{"customer", "created_at", "updated_at", "store_id", "items_count", "reserved_order_id"}},
	{"SELECT i.sku, SUM(i.qty_ordered) AS qty\nFROM sales_order_item i\n|", []string{"group by", "join sales_order", "where"}},
	{"SELECT * FROM cms_page WHERE ident|", []string{"ifier"}},
}

func newCompleteConn(t *testing.T, c *Core, cfg model.ConnectionConfig) string {
	t.Helper()
	projects, _ := NewProjectService(c).Projects()
	cfg.ProjectID = projects[0].ID
	saved, err := NewConnectionService(c).SaveConnection(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return saved.ID
}

// TestAICompleteLive compares inline suggestions of a model on the dummy SQLite schema and
// (DBVIEWER_MAGENTO_DB, a local MySQL database read through the dummy socket; metadata only).
// Opt-in: DBVIEWER_OLLAMA_MODEL=qwen3:4b-instruct go test ./internal/services -run TestAICompleteLive -v
func TestAICompleteLive(t *testing.T) {
	mdl := os.Getenv("DBVIEWER_OLLAMA_MODEL")
	if mdl == "" {
		t.Skip("set DBVIEWER_OLLAMA_MODEL to run against a local Ollama")
	}
	c := testCore(t)
	ctx := context.Background()
	svc := NewAIService(c)
	set := model.AISettings{Model: mdl, KeepAlive: "5m"}
	if err := svc.Warm(ctx, set); err != nil {
		t.Fatalf("warm: %v", err)
	}
	type suite struct {
		name  string
		conn  string
		cases []completeCase
	}
	suites := []suite{{"sqlite", newCompleteConn(t, c, model.ConnectionConfig{Name: "sqlite", Driver: model.DriverSQLite, ReadOnly: true,
		FilePath: filepath.Join(repoRoot(), "testdata", "dummy.sqlite")}), completeCasesDummy}}
	if db := os.Getenv("DBVIEWER_MAGENTO_DB"); db != "" {
		empty := ""
		suites = append(suites, suite{"magento", newCompleteConn(t, c, model.ConnectionConfig{Name: "magento", Driver: model.DriverMySQL, ReadOnly: true,
			Socket: "/opt/workbench/data/run/mysqld/mysql84.sock", User: "root", PasswordMode: "save", Password: &empty, Database: db}), completeCasesMagento})
	}
	var all []int64
	good, total := 0, 0
	for _, s := range suites {
		// first call loads the schema metadata (cached afterwards)
		if _, err := svc.CompleteSQL(ctx, model.AICompleteRequest{AISettings: set, ConnID: s.conn, Before: "SELECT 1 FROM x WHERE "}); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		for _, tc := range s.cases {
			i := strings.Index(tc.text, "|")
			before, after := tc.text[:i], tc.text[i+1:]
			var res model.AICompleteResult
			var err error
			var times []int64
			// cold prompt (new statement), then the same again (prompt cache) and with one more char typed
			for k := 0; k < 2; k++ {
				t0 := time.Now()
				res, err = svc.CompleteSQL(ctx, model.AICompleteRequest{AISettings: set, ConnID: s.conn, Before: before, After: after})
				if err != nil {
					t.Fatalf("%q: %v", tc.text, err)
				}
				times = append(times, time.Since(t0).Milliseconds())
			}
			all = append(all, times...)
			ok := res.Text != ""
			if ok && len(tc.expect) > 0 {
				ok = false
				low := strings.ToLower(res.Text)
				for _, e := range tc.expect {
					if strings.Contains(low, e) {
						ok = true
					}
				}
			}
			total++
			if ok {
				good++
			}
			mark := "✗"
			if ok {
				mark = "✓"
			}
			t.Logf("%s [%s %s] %v ms  %q  →  %q   (raw %q, tables %v)", mark, s.name, res.Mode, times, strings.ReplaceAll(before, "\n", "⏎")+"|", res.Text, res.Raw, res.Tables)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	t.Logf("%s: %d/%d good; latency median %d ms, p90 %d ms, max %d ms", mdl, good, total, all[len(all)/2], all[len(all)*9/10], all[len(all)-1])
}
