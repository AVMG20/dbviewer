package driver

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"
	"time"

	"dbviewer/internal/model"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/stdlib"
)

type postgresDriver struct{}

func init() { register(postgresDriver{}) }

func (postgresDriver) Name() string { return model.DriverPostgres }

func pgQuoteDSN(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

func (postgresDriver) Open(ctx context.Context, o OpenOptions) (*sql.DB, error) {
	c := o.Config
	parts := []string{}
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+pgQuoteDSN(v))
		}
	}
	host := c.Host
	if c.Socket != "" {
		host = c.Socket // socket directory
	}
	if host == "" {
		host = "127.0.0.1"
	}
	port := c.Port
	if port == 0 {
		port = 5432
	}
	db := c.Database
	if db == "" {
		db = "postgres"
	}
	add("host", host)
	add("port", fmt.Sprint(port))
	add("user", c.User)
	add("dbname", db)
	sslmode := c.SSLMode
	if sslmode == "" {
		sslmode = model.SSLPrefer
	}
	if c.Socket != "" {
		sslmode = model.SSLDisable
	}
	add("sslmode", sslmode)
	add("connect_timeout", "10")
	add("application_name", "dbviewer")
	for k, v := range c.Params {
		add(k, v)
	}
	// The password is set on the parsed config, never put into the DSN, so a
	// parse error cannot echo it.
	cfg, err := pgx.ParseConfig(strings.Join(parts, " "))
	if err != nil {
		return nil, model.Wrap(model.ErrInvalid, err)
	}
	if o.Password != "" {
		cfg.Password = o.Password
	}
	if o.ReadOnly {
		cfg.RuntimeParams["default_transaction_read_only"] = "on"
	}
	// On context cancel send a cancel request and keep the connection (console
	// sessions survive a cancelled query) instead of closing it.
	cfg.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, CancelRequestDelay: 0, DeadlineDelay: 5 * time.Second}
	}
	if c.DefaultSchema != "" {
		cfg.RuntimeParams["search_path"] = c.DefaultSchema
	}
	if o.Dial != nil {
		cfg.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) { return o.Dial(ctx, network, addr) }
		// resolve host names on the far side of the tunnel, not locally
		cfg.LookupFunc = func(ctx context.Context, host string) ([]string, error) { return []string{host}, nil }
	}
	pool := stdlib.OpenDB(*cfg)
	pool.SetMaxOpenConns(MaxPoolConns)
	pool.SetConnMaxIdleTime(5 * time.Minute)
	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func (postgresDriver) ServerVersion(ctx context.Context, db *sql.DB) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, `SHOW server_version`).Scan(&v)
	if i := strings.Index(v, " "); i > 0 {
		v = v[:i]
	}
	return "PostgreSQL " + v, err
}

func (postgresDriver) CurrentDatabase(ctx context.Context, db *sql.DB) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&v)
	return v, err
}

func (postgresDriver) DefaultSchema(ctx context.Context, db *sql.DB) (string, error) {
	var v sql.NullString
	err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&v)
	if v.String == "" {
		return "public", err
	}
	return v.String, err
}

func (postgresDriver) Databases(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT datname FROM pg_database WHERE NOT datistemplate AND datallowconn ORDER BY datname`)
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

func (d postgresDriver) Schemas(ctx context.Context, db *sql.DB) ([]model.SchemaInfo, error) {
	cur, _ := d.DefaultSchema(ctx, db)
	rows, err := db.QueryContext(ctx, `
SELECT nspname FROM pg_namespace
WHERE nspname NOT LIKE 'pg\_toast%' AND nspname NOT LIKE 'pg\_temp\_%'
ORDER BY nspname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SchemaInfo{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, model.SchemaInfo{Name: n, IsDefault: n == cur, IsSystem: n == "pg_catalog" || n == "information_schema"})
	}
	return out, rows.Err()
}

func pgSchemaExpr(schema string) (string, []any) {
	if schema == "" {
		return "current_schema()", nil
	}
	return "$1", []any{schema}
}

func (postgresDriver) Tables(ctx context.Context, db *sql.DB, schema string) ([]model.Table, error) {
	expr, args := pgSchemaExpr(schema)
	rows, err := db.QueryContext(ctx, `
SELECT n.nspname, c.relname, c.relkind::text,
       CASE WHEN c.relkind IN ('r','p','m') AND c.reltuples >= 0 THEN c.reltuples::bigint ELSE -1 END,
       CASE WHEN c.relkind IN ('r','p','m') THEN pg_total_relation_size(c.oid) ELSE -1 END,
       COALESCE(obj_description(c.oid, 'pg_class'), '')
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = `+expr+` AND c.relkind IN ('r','p','v','m','f') AND NOT c.relispartition
ORDER BY c.relname`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Table{}
	for rows.Next() {
		var t model.Table
		var kind string
		if err := rows.Scan(&t.Schema, &t.Name, &kind, &t.EstRows, &t.SizeBytes, &t.Comment); err != nil {
			return nil, err
		}
		switch kind {
		case "v":
			t.Kind = "view"
		case "m":
			t.Kind = "materialized_view"
		default:
			t.Kind = "table"
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d postgresDriver) Columns(ctx context.Context, db *sql.DB, schema string) (map[string][]model.Column, error) {
	expr, args := pgSchemaExpr(schema)
	rows, err := db.QueryContext(ctx, `
SELECT c.relname, a.attname, a.attnum, format_type(a.atttypid, a.atttypmod), t.typname,
       CASE WHEN t.typcategory = 'A' THEN COALESCE((SELECT et.oid::regtype::text FROM pg_type et WHERE et.oid = t.typelem), 'text') || '[]'
            ELSE a.atttypid::regtype::text END,
       NOT a.attnotnull, pg_get_expr(ad.adbin, ad.adrelid),
       COALESCE((SELECT k.n FROM unnest(pk.indkey) WITH ORDINALITY k(attnum, n) WHERE k.attnum = a.attnum), 0)::int,
       a.attidentity <> '' OR COALESCE(pg_get_expr(ad.adbin, ad.adrelid), '') LIKE 'nextval(%',
       a.attgenerated <> '', COALESCE(col_description(c.oid, a.attnum), ''),
       fn.nspname, fc.relname, fa.attname
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
LEFT JOIN pg_index pk ON pk.indrelid = c.oid AND pk.indisprimary
LEFT JOIN LATERAL (
  SELECT co.confrelid, co.confkey[1] AS fkey FROM pg_constraint co
  WHERE co.conrelid = c.oid AND co.contype = 'f' AND array_length(co.conkey, 1) = 1 AND co.conkey[1] = a.attnum
  LIMIT 1) fk ON true
LEFT JOIN pg_class fc ON fc.oid = fk.confrelid
LEFT JOIN pg_namespace fn ON fn.oid = fc.relnamespace
LEFT JOIN pg_attribute fa ON fa.attrelid = fk.confrelid AND fa.attnum = fk.fkey
WHERE n.nspname = `+expr+` AND c.relkind IN ('r','p','v','m','f') AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY c.relname, a.attnum`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]model.Column{}
	for rows.Next() {
		var table, name, ftype, typname, castType, comment string
		var ord, pkOrd int
		var nullable, auto, gen bool
		var def, fs, ft, fc sql.NullString
		if err := rows.Scan(&table, &name, &ord, &ftype, &typname, &castType, &nullable, &def, &pkOrd, &auto, &gen, &comment, &fs, &ft, &fc); err != nil {
			return nil, err
		}
		c := model.Column{
			Name: name, Ordinal: len(out[table]) + 1, Type: ftype, DataType: typname, Kind: d.Kind(typname),
			Nullable: nullable, PK: pkOrd > 0, PKOrdinal: pkOrd, AutoIncrement: auto, Generated: gen, Comment: comment,
			CastType: pgCastType(castType),
		}
		if strings.HasPrefix(typname, "_") {
			c.Kind = model.KindOther // arrays are sent as text
		}
		if def.Valid {
			v := def.String
			c.Default = &v
		}
		if ft.Valid {
			c.FK = &model.FKRef{Schema: fs.String, Table: ft.String, Column: fc.String}
		}
		out[table] = append(out[table], c)
	}
	return out, rows.Err()
}

func (postgresDriver) QuoteIdent(name string) string { return quoteWith(name, '"') }

func (d postgresDriver) QualifiedName(schema, table string) string {
	if schema == "" {
		return d.QuoteIdent(table)
	}
	return d.QuoteIdent(schema) + "." + d.QuoteIdent(table)
}

func (postgresDriver) Placeholder(n int) string { return fmt.Sprintf("$%d", n) }

func (d postgresDriver) UseSchema(schema string) string {
	return "SET search_path TO " + d.QuoteIdent(schema)
}

func (postgresDriver) Kind(t string) string {
	switch strings.ToLower(t) {
	case "int2", "int4", "int8", "oid", "xid":
		return model.KindInt
	case "float4", "float8":
		return model.KindFloat
	case "bpchar", "varchar", "name", "char", "citext", "inet", "cidr", "macaddr":
		return model.KindString
	case "timestamptz", "timestamp":
		return model.KindDateTime
	case "timetz":
		return model.KindTime
	case "interval", "bit", "varbit":
		return model.KindString // bit strings come back as text ('1010')
	}
	return kindFromType(t)
}

// pgCastType maps a regtype name to a cast target that does not truncate or pad:
// char(n) -> bpchar (unbounded), bit(n) -> bit varying; regtype already omits
// the typmod for varchar, numeric, timestamp(p), ...
func pgCastType(t string) string {
	base, arr := strings.CutSuffix(t, "[]")
	switch base {
	case "character", "\"char\"":
		if base == "character" {
			base = "bpchar"
		}
	case "bit":
		base = "bit varying"
	}
	if arr {
		return base + "[]"
	}
	return base
}
