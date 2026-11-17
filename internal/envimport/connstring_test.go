package envimport

import (
	"strings"
	"testing"

	"dbviewer/internal/model"
)

func pw(s string) *string { return &s }

func TestParseConnectionString(t *testing.T) {
	type want struct {
		format, driver, host, socket, user, db, file, ssl, schema string
		port                                                      int
		password                                                  *string // nil = not set
		readOnly                                                  bool
		warn                                                      string // substring of some warning
	}
	cases := []struct {
		in string
		w  want
	}{
		// URLs
		{"mysql://root:s3cr%40t@db.example.com:3307/shop", want{format: FormatURL, driver: "mysql", host: "db.example.com", port: 3307, user: "root", password: pw("s3cr@t"), db: "shop"}},
		{"mariadb://app@127.0.0.1/app?ssl-mode=REQUIRED&charset=utf8mb4", want{format: FormatURL, driver: "mysql", host: "127.0.0.1", user: "app", db: "app", ssl: "require", warn: "charset"}},
		{"mysql://root:p@ss/w#rd@db.example.com/app", want{format: FormatURL, driver: "mysql", host: "db.example.com", user: "root", password: pw("p@ss/w#rd"), db: "app"}},
		{"mysql://root@localhost/app?socket=/tmp/mysql.sock", want{format: FormatURL, driver: "mysql", host: "localhost", socket: "/tmp/mysql.sock", user: "root", db: "app"}},
		{"mysql://u:p@h/d?unix_socket=/var/run/mysqld/mysqld.sock&tls=false", want{format: FormatURL, driver: "mysql", host: "h", socket: "/var/run/mysqld/mysqld.sock", user: "u", password: pw("p"), db: "d", ssl: "disable"}},
		{"postgres://app:s3cr3t@prod.example.com:5433/shop?sslmode=verify-full&application_name=x", want{format: FormatURL, driver: "postgres", host: "prod.example.com", port: 5433, user: "app", password: pw("s3cr3t"), db: "shop", ssl: "verify-full"}},
		{"postgresql://u@[::1]:55432/dbviewer_dummy", want{format: FormatURL, driver: "postgres", host: "::1", port: 55432, user: "u", db: "dbviewer_dummy"}},
		{"pgsql://u@[2001:db8::1]/d", want{format: FormatURL, driver: "postgres", host: "2001:db8::1", user: "u", db: "d"}},
		{"postgresql:///shop?host=/var/run/postgresql&user=me", want{format: FormatURL, driver: "postgres", socket: "/var/run/postgresql", user: "me", db: "shop"}},
		{"postgres://u@h/d?search_path=reporting,public", want{format: FormatURL, driver: "postgres", host: "h", user: "u", db: "d", schema: "reporting"}},
		{"postgres://u@h", want{format: FormatURL, driver: "postgres", host: "h", user: "u", db: "postgres"}},
		{"  'postgres://u:pw@h/d'  ", want{format: FormatURL, driver: "postgres", host: "h", user: "u", password: pw("pw"), db: "d"}},
		{"sqlite:///Users/me/app.sqlite", want{format: FormatURL, driver: "sqlite", file: "/Users/me/app.sqlite"}},
		{"sqlite://database/app.sqlite", want{format: FormatURL, driver: "sqlite", file: "database/app.sqlite", warn: "relative"}},
		{"file:/tmp/my%20db.sqlite?mode=ro", want{format: FormatURL, driver: "sqlite", file: "/tmp/my db.sqlite", readOnly: true}},
		{"file:///tmp/x.db", want{format: FormatURL, driver: "sqlite", file: "/tmp/x.db"}},
		// JDBC
		{"jdbc:mysql://db:3306/shop?user=root&password=se%26cret&useSSL=false&serverTimezone=UTC", want{format: FormatJDBC, driver: "mysql", host: "db", port: 3306, user: "root", password: pw("se&cret"), db: "shop", ssl: "disable"}},
		{"jdbc:mariadb://h1,h2:3307/d", want{format: FormatJDBC, driver: "mysql", host: "h1", db: "d", warn: "several hosts"}},
		{"jdbc:postgresql://localhost:5432/shop?user=app&password=x&currentSchema=sales&sslmode=require", want{format: FormatJDBC, driver: "postgres", host: "localhost", port: 5432, user: "app", password: pw("x"), db: "shop", ssl: "require", schema: "sales"}},
		{"jdbc:sqlite:/data/app.db", want{format: FormatJDBC, driver: "sqlite", file: "/data/app.db"}},
		// Go MySQL DSN
		{"root:p@ss@tcp(127.0.0.1:3306)/dbviewer_dummy?parseTime=true&charset=utf8mb4", want{format: FormatMySQLDSN, driver: "mysql", host: "127.0.0.1", port: 3306, user: "root", password: pw("p@ss"), db: "dbviewer_dummy", warn: "charset"}},
		{"root@unix(/opt/workbench/data/run/mysqld/mysql84.sock)/dbviewer_dummy", want{format: FormatMySQLDSN, driver: "mysql", socket: "/opt/workbench/data/run/mysqld/mysql84.sock", user: "root", db: "dbviewer_dummy"}},
		{"u:@tcp([::1]:3384)/", want{format: FormatMySQLDSN, driver: "mysql", host: "::1", port: 3384, user: "u", password: pw("")}},
		{"tcp(db:3306)/app?tls=skip-verify", want{format: FormatMySQLDSN, driver: "mysql", host: "db", port: 3306, db: "app", ssl: "require"}},
		// libpq
		{"host=127.0.0.1 port=55432 dbname=dbviewer_dummy user=postgres password=dbviewer sslmode=disable", want{format: FormatLibpq, driver: "postgres", host: "127.0.0.1", port: 55432, user: "postgres", password: pw("dbviewer"), db: "dbviewer_dummy", ssl: "disable"}},
		{"host=/var/run/postgresql dbname=shop user=me password='it\\'s a secret'", want{format: FormatLibpq, driver: "postgres", socket: "/var/run/postgresql", user: "me", password: pw("it's a secret"), db: "shop"}},
		{"dbname=x options='-c search_path=reporting' target_session_attrs=read-write", want{format: FormatLibpq, driver: "postgres", db: "x", schema: "reporting", warn: "target_session_attrs"}},
		// env lines
		{"DATABASE_URL=postgres://app:pw@h:5433/shop", want{format: FormatEnv, driver: "postgres", host: "h", port: 5433, user: "app", password: pw("pw"), db: "shop"}},
		{"DB_CONNECTION=mysql\nDB_SOCKET=/tmp/m.sock\nDB_DATABASE=app\nDB_USERNAME=root\nDB_PASSWORD=\n", want{format: FormatEnv, driver: "mysql", socket: "/tmp/m.sock", user: "root", password: pw(""), db: "app"}},
	}
	for _, c := range cases {
		t.Run(strings.SplitN(strings.TrimSpace(c.in), "\n", 2)[0], func(t *testing.T) {
			r, err := ParseConnectionString(c.in)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			g, w := r.Config, c.w
			check := func(name string, got, want any) {
				if got != want {
					t.Errorf("%s = %#v, want %#v", name, got, want)
				}
			}
			check("format", r.Found["format"], w.format)
			check("driver", g.Driver, w.driver)
			check("host", g.Host, w.host)
			check("port", g.Port, w.port)
			check("socket", g.Socket, w.socket)
			check("user", g.User, w.user)
			check("database", g.Database, w.db)
			check("file", g.FilePath, w.file)
			check("sslMode", g.SSLMode, w.ssl)
			check("schema", g.DefaultSchema, w.schema)
			check("readOnly", g.ReadOnly, w.readOnly)
			switch {
			case w.password == nil && g.Password != nil:
				t.Errorf("password set (%d chars), want unset", len(*g.Password))
			case w.password != nil && (g.Password == nil || *g.Password != *w.password):
				t.Errorf("password mismatch")
			}
			if w.warn != "" && !strings.Contains(strings.Join(r.Warnings, "|"), w.warn) {
				t.Errorf("warnings %v, want one containing %q", r.Warnings, w.warn)
			}
			// the password never shows up in warnings or found
			if g.Password != nil && len(*g.Password) >= 2 {
				for _, s := range r.Warnings {
					if strings.Contains(s, *g.Password) {
						t.Errorf("password leaked in warning %q", s)
					}
				}
				if f, ok := r.Found["password"]; ok && f != "••••" {
					t.Errorf("found[password] = %q", f)
				}
			}
			if g.Driver != model.DriverSQLite && g.Name == "" {
				t.Errorf("no name suggested")
			}
		})
	}
}

func TestParseConnectionStringErrors(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"hello world",
		"oracle://scott:tiger@db/orcl",
		"mysql:/missing-slashes",
		"postgres://u@h:notaport/d",
		"postgres://u@[::1/d",
		"sqlite://",
		"foo=bar baz=1",
		"host=h password='unterminated",
		"FOO=bar\nBAR=baz",
	} {
		if r, err := ParseConnectionString(in); err == nil {
			t.Errorf("%q: expected an error, got %+v", in, r.Config)
		} else if !strings.HasPrefix(err.Error(), model.ErrInvalid) {
			t.Errorf("%q: error code: %v", in, err)
		}
	}
}
