package driver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"dbviewer/internal/model"

	"github.com/go-sql-driver/mysql"
)

type mysqlDriver struct{}

var dialSeq atomic.Int64

func init() { register(mysqlDriver{}) }

func (mysqlDriver) Name() string { return model.DriverMySQL }

func (mysqlDriver) Open(ctx context.Context, o OpenOptions) (*sql.DB, error) {
	c := o.Config
	mc := mysql.NewConfig()
	mc.User = c.User
	mc.Passwd = o.Password
	mc.DBName = c.Database
	if c.Socket != "" {
		mc.Net, mc.Addr = "unix", c.Socket
	} else {
		host := c.Host
		if host == "" {
			host = "127.0.0.1"
		}
		port := c.Port
		if port == 0 {
			port = 3306
		}
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]") // "[::1]" -> "::1"
		mc.Net, mc.Addr = "tcp", net.JoinHostPort(host, strconv.Itoa(port))
	}
	if o.Dial != nil {
		// unique per pool: a second Open for the same connection (Test while
		// connected) must not replace the live pool's dialer
		name := fmt.Sprintf("dbviewer-ssh-%d", dialSeq.Add(1))
		if o.AddCloser != nil {
			o.AddCloser(func() { mysql.DeregisterDialContext(name) })
		}
		network := "tcp"
		if c.Socket != "" {
			network = "unix" // remote socket via the tunnel
		}
		mysql.RegisterDialContext(name, func(ctx context.Context, addr string) (net.Conn, error) { return o.Dial(ctx, network, addr) })
		mc.Net = name
	}
	switch c.SSLMode {
	case model.SSLDisable:
		mc.TLSConfig = "false"
	case model.SSLPrefer, "":
		mc.TLSConfig = "preferred"
	case model.SSLRequire:
		mc.TLSConfig = "skip-verify"
	case model.SSLVerifyCA, model.SSLVerifyFull:
		mc.TLSConfig = "true"
	}
	if (c.Socket != "" || o.Dial != nil) && (c.SSLMode == "" || c.SSLMode == model.SSLPrefer) {
		mc.TLSConfig = "false"
	}
	mc.AllowNativePasswords = true
	mc.AllowCleartextPasswords = c.SSLMode == model.SSLRequire || c.SSLMode == model.SSLVerifyCA || c.SSLMode == model.SSLVerifyFull
	mc.ParseTime = false // keep DATETIME text exactly as stored
	mc.Timeout = 10 * time.Second
	mc.MultiStatements = false
	mc.ClientFoundRows = true // UPDATE reports matched rows (needed for the exactly-one-row check)
	mc.Params = map[string]string{}
	for k, v := range c.Params {
		if k == "charset" {
			_ = mc.Apply(mysql.Charset(v, ""))
			continue
		}
		mc.Params[k] = v // anything else is sent as SET <k>=<v>
	}
	conn, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}
	if o.ReadOnly {
		// every new session: SET SESSION TRANSACTION READ ONLY (MySQL and MariaDB),
		// run after the connection params so they cannot override it
		conn = readOnlyConnector{conn}
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(MaxPoolConns)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (mysqlDriver) ServerVersion(ctx context.Context, db *sql.DB) (string, error) {
	var v string
	if err := db.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&v); err != nil {
		return "", err
	}
	if strings.Contains(strings.ToLower(v), "mariadb") {
		return "MariaDB " + strings.Split(v, "-")[0], nil
	}
	return "MySQL " + v, nil
}

func (mysqlDriver) CurrentDatabase(ctx context.Context, db *sql.DB) (string, error) {
	var v sql.NullString
	err := db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&v)
	return v.String, err
}

func (d mysqlDriver) DefaultSchema(ctx context.Context, db *sql.DB) (string, error) {
	return d.CurrentDatabase(ctx, db)
}

var mysqlSystemSchemas = map[string]bool{"information_schema": true, "mysql": true, "performance_schema": true, "sys": true}

func (mysqlDriver) Databases(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SHOW DATABASES`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (d mysqlDriver) Schemas(ctx context.Context, db *sql.DB) ([]model.SchemaInfo, error) {
	names, err := d.Databases(ctx, db)
	if err != nil {
		return nil, err
	}
	cur, _ := d.CurrentDatabase(ctx, db)
	out := make([]model.SchemaInfo, 0, len(names))
	for _, n := range names {
		out = append(out, model.SchemaInfo{Name: n, IsDefault: n == cur, IsSystem: mysqlSystemSchemas[n]})
	}
	return out, nil
}

// mysqlSchemaExpr returns a SQL expression + args for the schema filter.
func mysqlSchemaExpr(schema string) (string, []any) {
	if schema == "" {
		return "DATABASE()", nil
	}
	return "?", []any{schema}
}

func (mysqlDriver) Tables(ctx context.Context, db *sql.DB, schema string) ([]model.Table, error) {
	expr, args := mysqlSchemaExpr(schema)
	rows, err := db.QueryContext(ctx, `
SELECT table_schema, table_name, table_type, COALESCE(table_rows, -1),
       COALESCE(data_length + index_length, -1), COALESCE(table_comment, '')
FROM information_schema.tables WHERE table_schema = `+expr+` ORDER BY table_name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Table{}
	for rows.Next() {
		var t model.Table
		var typ string
		if err := rows.Scan(&t.Schema, &t.Name, &typ, &t.EstRows, &t.SizeBytes, &t.Comment); err != nil {
			return nil, err
		}
		t.Kind = "table"
		if strings.Contains(typ, "VIEW") {
			t.Kind, t.EstRows, t.SizeBytes = "view", -1, -1
		}
		if t.Kind == "view" && t.Comment == "VIEW" {
			t.Comment = ""
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d mysqlDriver) Columns(ctx context.Context, db *sql.DB, schema string) (map[string][]model.Column, error) {
	expr, args := mysqlSchemaExpr(schema)
	rows, err := db.QueryContext(ctx, `
SELECT c.table_name, c.column_name, c.ordinal_position, c.column_type, c.data_type, c.is_nullable,
       c.column_default, c.column_key, c.extra, COALESCE(c.column_comment, ''), COALESCE(c.generation_expression, ''),
       COALESCE(pk.ordinal_position, 0)
FROM information_schema.columns c
LEFT JOIN information_schema.key_column_usage pk
  ON pk.table_schema = c.table_schema AND pk.table_name = c.table_name
 AND pk.column_name = c.column_name AND pk.constraint_name = 'PRIMARY'
WHERE c.table_schema = `+expr+`
ORDER BY c.table_name, c.ordinal_position`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]model.Column{}
	for rows.Next() {
		var table, name, ctype, dtype, nullable, key, extra, comment, genExpr string
		var ord, pkOrd int
		var def sql.NullString
		if err := rows.Scan(&table, &name, &ord, &ctype, &dtype, &nullable, &def, &key, &extra, &comment, &genExpr, &pkOrd); err != nil {
			return nil, err
		}
		c := model.Column{
			Name: name, Ordinal: ord, Type: ctype, DataType: strings.ToLower(dtype), Kind: d.Kind(ctype),
			Nullable: nullable == "YES", PK: pkOrd > 0, PKOrdinal: pkOrd,
			AutoIncrement: strings.Contains(extra, "auto_increment"),
			Generated:     genExpr != "" || strings.Contains(extra, "GENERATED"),
			Comment:       comment,
		}
		if def.Valid {
			v := def.String
			c.Default = &v
		}
		out[table] = append(out[table], c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	fks, err := db.QueryContext(ctx, `
SELECT k.table_name, k.column_name, k.referenced_table_schema, k.referenced_table_name, k.referenced_column_name
FROM information_schema.key_column_usage k
WHERE k.table_schema = `+expr+` AND k.referenced_table_name IS NOT NULL
  AND (SELECT COUNT(*) FROM information_schema.key_column_usage k2
       WHERE k2.constraint_schema = k.constraint_schema AND k2.constraint_name = k.constraint_name
         AND k2.table_name = k.table_name) = 1`, args...)
	if err == nil {
		defer fks.Close()
		for fks.Next() {
			var t, col, rs, rt, rc string
			if fks.Scan(&t, &col, &rs, &rt, &rc) != nil {
				continue
			}
			for i := range out[t] {
				if out[t][i].Name == col && out[t][i].FK == nil {
					out[t][i].FK = &model.FKRef{Schema: rs, Table: rt, Column: rc}
				}
			}
		}
	}
	return out, nil
}

func (d mysqlDriver) DDL(ctx context.Context, db *sql.DB, schema, table string) (string, error) {
	var name, ddl string
	err := db.QueryRowContext(ctx, `SHOW CREATE TABLE `+d.QualifiedName(schema, table)).Scan(&name, &ddl)
	if err != nil {
		// views return 4 columns
		var cs, coll string
		if err2 := db.QueryRowContext(ctx, `SHOW CREATE VIEW `+d.QualifiedName(schema, table)).Scan(&name, &ddl, &cs, &coll); err2 == nil {
			return ddl + ";", nil
		}
		return "", err
	}
	return ddl + ";", nil
}

func (mysqlDriver) QuoteIdent(name string) string { return quoteWith(name, '`') }

func (d mysqlDriver) QualifiedName(schema, table string) string {
	if schema == "" {
		return d.QuoteIdent(table)
	}
	return d.QuoteIdent(schema) + "." + d.QuoteIdent(table)
}

func (mysqlDriver) Placeholder(n int) string { return "?" }

func (d mysqlDriver) UseSchema(schema string) string { return "USE " + d.QuoteIdent(schema) }

func (mysqlDriver) Kind(t string) string {
	lt := strings.ToLower(strings.TrimSpace(t))
	if lt == "tinyint(1)" || lt == "bit(1)" {
		return model.KindBool
	}
	if lt == "bit" || strings.HasPrefix(lt, "bit(") {
		return model.KindBinary // bit(n), n > 1: raw bytes
	}
	if strings.HasPrefix(lt, "enum") || strings.HasPrefix(lt, "set(") {
		return model.KindString
	}
	if lt == "year" || strings.HasPrefix(lt, "year(") {
		return model.KindInt
	}
	return kindFromType(lt)
}

// readOnlyConnector makes every new MySQL/MariaDB session read-only.
type readOnlyConnector struct{ driver.Connector }

func (c readOnlyConnector) Connect(ctx context.Context) (driver.Conn, error) {
	dc, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	ex, ok := dc.(driver.ExecerContext)
	if !ok {
		dc.Close()
		return nil, fmt.Errorf("mysql driver cannot execute statements on connect")
	}
	if _, err := ex.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY", nil); err != nil {
		dc.Close()
		return nil, err
	}
	return dc, nil
}
