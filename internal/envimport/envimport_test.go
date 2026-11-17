package envimport

import (
	"path/filepath"
	"testing"

	"dbviewer/internal/model"
)

func TestSampleEnv(t *testing.T) {
	r, err := ParseFile(filepath.Join("..", "..", "testdata", "sample.env"))
	if err != nil {
		t.Fatal(err)
	}
	c := r.Config
	if c.Driver != model.DriverMySQL || c.Socket != "/opt/workbench/data/run/mysqld/mysql84.sock" ||
		c.Database != "dbviewer_dummy" || c.User != "root" || c.Password == nil || *c.Password != "" || c.Host != "" || c.Port != 0 {
		t.Fatalf("unexpected config: %+v", c)
	}
	if c.Name != "DummyShop (dbviewer_dummy)" {
		t.Fatalf("name %q", c.Name)
	}
}

func TestVariants(t *testing.T) {
	r := Parse("DB_CONNECTION=pgsql\nDB_HOST=db.example.com\nDB_PORT=6543\nDB_DATABASE=\"shop\" # comment\nDB_USERNAME=u\nDB_PASSWORD='p#w'\nAPP_ENV=production\n", "")
	c := r.Config
	if c.Driver != model.DriverPostgres || c.Port != 6543 || c.Database != "shop" || *c.Password != "p#w" || !c.ReadOnly || c.Color != model.ColorRed {
		t.Fatalf("unexpected: %+v", c)
	}
	r = Parse("DATABASE_URL=mysql://app:secret@10.0.0.5:3307/prod_db\n", "")
	if c := r.Config; c.Driver != model.DriverMySQL || c.Host != "10.0.0.5" || c.Port != 3307 || c.Database != "prod_db" || *c.Password != "secret" {
		t.Fatalf("url: %+v", c)
	}
	r = Parse("DB_CONNECTION=sqlite\nDB_DATABASE=database/app.sqlite\n", "/proj")
	if c := r.Config; c.Driver != model.DriverSQLite || c.FilePath != "/proj/database/app.sqlite" {
		t.Fatalf("sqlite: %+v", c)
	}
}
