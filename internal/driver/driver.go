// Package driver abstracts the SQL dialects (postgres, mysql, sqlite).
package driver

import (
	"context"
	"database/sql"
	"net"
	"strings"

	"dbviewer/internal/model"
)

// MaxPoolConns is the pool size for server databases. Console sessions keep a
// connection each, so it is larger than the grid alone needs.
const MaxPoolConns = 24

// DialFunc dials through an SSH tunnel when set (nil = direct).
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// OpenOptions are resolved connection settings (secrets already looked up).
type OpenOptions struct {
	Config   model.ConnectionConfig
	Password string
	ReadOnly bool     // enforce at session level (see PLAN "Read-only and production safety")
	Dial     DialFunc // optional
	// AddCloser registers cleanup to run when the pool is closed (optional).
	AddCloser func(func())
}

// Driver is implemented once per database engine. All metadata methods take the
// pool returned by Open; schema "" means the default schema.
type Driver interface {
	Name() string

	// Open returns a ready pool. When opts.ReadOnly is set every pooled session must
	// be read-only (postgres default_transaction_read_only, mysql transaction_read_only,
	// sqlite mode=ro + query_only).
	Open(ctx context.Context, opts OpenOptions) (*sql.DB, error)
	ServerVersion(ctx context.Context, db *sql.DB) (string, error)
	CurrentDatabase(ctx context.Context, db *sql.DB) (string, error)
	DefaultSchema(ctx context.Context, db *sql.DB) (string, error)

	Databases(ctx context.Context, db *sql.DB) ([]string, error)
	Schemas(ctx context.Context, db *sql.DB) ([]model.SchemaInfo, error)
	Tables(ctx context.Context, db *sql.DB, schema string) ([]model.Table, error)
	// Columns returns columns for every table/view in the schema (bulk, for the search
	// index), keyed by table name, in ordinal order. PK and single-column FK info filled.
	Columns(ctx context.Context, db *sql.DB, schema string) (map[string][]model.Column, error)
	Indexes(ctx context.Context, db *sql.DB, schema, table string) ([]model.Index, error)
	ForeignKeys(ctx context.Context, db *sql.DB, schema, table string) ([]model.ForeignKey, error)
	ReferencedBy(ctx context.Context, db *sql.DB, schema, table string) ([]model.ForeignKey, error)
	Triggers(ctx context.Context, db *sql.DB, schema, table string) ([]model.Trigger, error)
	DDL(ctx context.Context, db *sql.DB, schema, table string) (string, error)

	QuoteIdent(name string) string
	// QualifiedName quotes schema.table ("" schema = unqualified).
	QualifiedName(schema, table string) string
	Placeholder(n int) string // 1-based: "$1" (postgres) or "?"
	// UseSchema returns the statement that makes schema the session default.
	UseSchema(schema string) string
	// Kind maps a database type name to a model.Kind* value.
	Kind(dbType string) string
}

var registry = map[string]Driver{}

func register(d Driver) { registry[d.Name()] = d }

// Get returns the driver for name ("postgres", "mysql", "sqlite").
func Get(name string) (Driver, error) {
	d, ok := registry[name]
	if !ok {
		return nil, model.Errf(model.ErrInvalid, "unknown driver %q", name)
	}
	return d, nil
}

func quoteWith(name string, q byte) string {
	s := string(q)
	return s + strings.ReplaceAll(name, s, s+s) + s
}

// kindFromType is a shared heuristic mapping of SQL type names to value kinds.
func kindFromType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if i := strings.IndexAny(t, "( "); i > 0 {
		base := t[:i]
		if base == "double" || base == "character" || base == "timestamp" || base == "time" || base == "bit" {
			// multi-word types handled below with the full string
		} else {
			t = base
		}
	}
	switch {
	case t == "bool" || t == "boolean":
		return model.KindBool
	case t == "tinyint(1)":
		return model.KindBool
	case strings.Contains(t, "int") && !strings.Contains(t, "interval") && !strings.Contains(t, "point"):
		return model.KindInt
	case t == "serial" || t == "bigserial" || t == "smallserial" || t == "year":
		return model.KindInt
	case t == "decimal" || t == "numeric" || t == "money" || strings.HasPrefix(t, "decimal") || strings.HasPrefix(t, "numeric"):
		return model.KindDecimal
	case t == "real" || t == "float" || t == "float4" || t == "float8" || strings.HasPrefix(t, "double"):
		return model.KindFloat
	case t == "json" || t == "jsonb":
		return model.KindJSON
	case t == "uuid":
		return model.KindUUID
	case t == "date":
		return model.KindDate
	case strings.HasPrefix(t, "timestamp") || t == "datetime" || strings.HasPrefix(t, "datetime"):
		return model.KindDateTime
	case strings.HasPrefix(t, "time"):
		return model.KindTime
	case strings.Contains(t, "blob") || t == "bytea" || strings.Contains(t, "binary") || t == "bit":
		return model.KindBinary
	case strings.Contains(t, "text") || t == "clob":
		return model.KindText
	case strings.Contains(t, "char") || t == "enum" || t == "set" || t == "citext" || t == "name" || t == "string":
		return model.KindString
	}
	return model.KindOther
}
