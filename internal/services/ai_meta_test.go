package services

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbviewer/internal/ai"
	"dbviewer/internal/model"

	_ "modernc.org/sqlite"
)

func createMagentoEAVTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "magento_eav.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE eav_entity_type (entity_type_id INTEGER PRIMARY KEY, entity_type_code TEXT, entity_table TEXT)`,
		`CREATE TABLE eav_attribute (attribute_id INTEGER PRIMARY KEY, entity_type_id INTEGER REFERENCES eav_entity_type(entity_type_id), attribute_code TEXT, backend_type TEXT, frontend_input TEXT, frontend_label TEXT)`,
		`CREATE TABLE catalog_product_entity (entity_id INTEGER PRIMARY KEY, sku TEXT, type_id TEXT)`,
		`CREATE TABLE catalog_product_entity_varchar (value_id INTEGER PRIMARY KEY, attribute_id INTEGER REFERENCES eav_attribute(attribute_id), store_id INTEGER, entity_id INTEGER REFERENCES catalog_product_entity(entity_id), value TEXT)`,
		`CREATE TABLE catalog_product_entity_text (value_id INTEGER PRIMARY KEY, attribute_id INTEGER REFERENCES eav_attribute(attribute_id), store_id INTEGER, entity_id INTEGER REFERENCES catalog_product_entity(entity_id), value TEXT)`,
		`CREATE TABLE catalog_product_entity_int (value_id INTEGER PRIMARY KEY, attribute_id INTEGER REFERENCES eav_attribute(attribute_id), store_id INTEGER, entity_id INTEGER REFERENCES catalog_product_entity(entity_id), value INTEGER)`,
		`INSERT INTO eav_entity_type VALUES (4, 'catalog_product', 'catalog_product_entity')`,
		`INSERT INTO eav_attribute VALUES (73, 4, 'name', 'varchar', 'text', 'Product Name')`,
		`INSERT INTO eav_attribute VALUES (187, 4, 'showrooms', 'varchar', 'multiselect', 'Showrooms')`,
		`INSERT INTO eav_attribute VALUES (188, 4, 'color', 'int', 'select', 'Color')`,
		`INSERT INTO catalog_product_entity VALUES (1, 'SKU-1', 'simple'), (2, 'SKU-2', 'simple'), (3, 'SKU-3', 'simple')`,
		`INSERT INTO catalog_product_entity_varchar VALUES (1, 187, 0, 1, '12,15'), (2, 187, 0, 2, ''), (3, 73, 0, 1, 'Chair'), (4, 73, 0, 2, 'Table')`,
	}
	// Add 100 noise tables so totalTables >> AllTablesBelow (like a real Magento DB).
	for i := 0; i < 100; i++ {
		stmts = append(stmts, fmt.Sprintf(`CREATE TABLE vendor_misc_%03d (id INTEGER PRIMARY KEY, val TEXT)`, i))
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return dbPath
}

func TestLookupEAVMetaMagento(t *testing.T) {
	dbPath := createMagentoEAVTestDB(t)
	c := testCore(t)
	ctx := context.Background()
	projects, _ := NewProjectService(c).Projects()
	saved, err := NewConnectionService(c).SaveConnection(model.ConnectionConfig{
		ProjectID: projects[0].ID, Name: "magento", Driver: model.DriverSQLite, ReadOnly: true, FilePath: dbPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewAIService(c)
	p, err := c.pool(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	infos, err := svc.tableInfos(ctx, p, "")
	if err != nil {
		t.Fatal(err)
	}
	meta := svc.lookupDynamicMeta(ctx, p, "", "create a query to select all products that have the showrooms attribute filled in (this is magento)", nil, infos)
	t.Logf("meta.Text:\n%s\nforceTables: %v", meta.Text, meta.ForceTables)
	if !strings.Contains(meta.Text, "showrooms") || !strings.Contains(meta.Text, "attribute_id=187") || !strings.Contains(meta.Text, "catalog_product_entity_varchar") {
		t.Fatalf("expected showrooms EAV metadata, got:\n%s", meta.Text)
	}
	hasVarchar := false
	for _, ft := range meta.ForceTables {
		if ft == "catalog_product_entity_varchar" {
			hasVarchar = true
		}
	}
	if !hasVarchar {
		t.Errorf("expected catalog_product_entity_varchar in ForceTables: %v", meta.ForceTables)
	}
}

// TestAgyMagentoEAVLive tests the exact Magento EAV prompt against Antigravity CLI.
// Opt-in: DBVIEWER_AGY_LIVE=1 go test ./internal/services -run TestAgyMagentoEAVLive -v
func TestAgyMagentoEAVLive(t *testing.T) {
	if os.Getenv("DBVIEWER_AGY_LIVE") == "" {
		t.Skip("set DBVIEWER_AGY_LIVE=1 to run against Antigravity CLI (agy)")
	}
	dbPath := createMagentoEAVTestDB(t)
	realHome := os.Getenv("HOME")
	c := testCore(t)
	if realHome != "" {
		t.Setenv("HOME", realHome)
	}
	ctx := context.Background()
	projects, _ := NewProjectService(c).Projects()
	saved, err := NewConnectionService(c).SaveConnection(model.ConnectionConfig{
		ProjectID: projects[0].ID, Name: "magento", Driver: model.DriverSQLite, ReadOnly: true, FilePath: dbPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewAIService(c)
	qs := NewQueryService(c)
	res, err := svc.GenerateSQL(ctx, model.AISQLRequest{
		AISettings: model.AISettings{Provider: ai.ProviderAgy, Model: ai.DefaultAgyModel},
		ConnID:     saved.ID,
		Prompt:     "create a query to select all products that have the showrooms attribute filled in (this is magento)",
	})
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	t.Logf("GenerateSQL (%d ms, %d/%d tables %v):\n%s", res.DurationMs, len(res.Tables), res.TotalTables, res.Tables, res.SQL)
	out, err := qs.RunQuery(ctx, model.QueryRequest{ConnID: saved.ID, SQL: res.SQL, MaxRows: 10})
	if err != nil || len(out) == 0 || out[0].Error != "" {
		t.Fatalf("generated SQL failed to run: %v %+v", err, out)
	}
	if len(out[0].Rows) != 1 {
		t.Errorf("expected 1 product row (SKU-1 with non-empty showrooms), got %d rows: %+v", len(out[0].Rows), out[0].Rows)
	}
}
