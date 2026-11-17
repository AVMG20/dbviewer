package driver

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"dbviewer/internal/model"

	_ "modernc.org/sqlite"
)

type sqliteDriver struct{}

func init() { register(sqliteDriver{}) }

func (sqliteDriver) Name() string { return model.DriverSQLite }

func (sqliteDriver) Open(ctx context.Context, o OpenOptions) (*sql.DB, error) {
	path := o.Config.FilePath
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if _, err := os.Stat(path); err != nil {
		return nil, model.Errf(model.ErrInvalid, "SQLite file not found: %s", path)
	}
	q := url.Values{}
	// keep DATE/DATETIME/TIMESTAMP text exactly as stored (no time.Time parsing)
	q.Set("_texttotime", "0")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	if o.ReadOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	}
	for k, v := range o.Config.Params {
		q.Add(k, v)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	// escape the path: '#', '?' and '%' are URI syntax
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(MaxPoolConns)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (sqliteDriver) ServerVersion(ctx context.Context, db *sql.DB) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&v)
	return "SQLite " + v, err
}

func (sqliteDriver) CurrentDatabase(ctx context.Context, db *sql.DB) (string, error) {
	return "main", nil
}

func (sqliteDriver) DefaultSchema(ctx context.Context, db *sql.DB) (string, error) {
	return "main", nil
}

func (d sqliteDriver) Databases(ctx context.Context, db *sql.DB) ([]string, error) {
	s, err := d.Schemas(ctx, db)
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.Name
	}
	return out, err
}

func (sqliteDriver) Schemas(ctx context.Context, db *sql.DB) ([]model.SchemaInfo, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_database_list ORDER BY seq`)
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
		out = append(out, model.SchemaInfo{Name: n, IsDefault: n == "main", IsSystem: n == "temp"})
	}
	return out, rows.Err()
}

func sqliteSchema(s string) string {
	if s == "" {
		return "main"
	}
	return s
}

func (d sqliteDriver) Tables(ctx context.Context, db *sql.DB, schema string) ([]model.Table, error) {
	schema = sqliteSchema(schema)
	rows, err := db.QueryContext(ctx, `SELECT name, type FROM `+d.QuoteIdent(schema)+`.sqlite_master
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Table{}
	for rows.Next() {
		t := model.Table{Schema: schema, EstRows: -1, SizeBytes: -1}
		if err := rows.Scan(&t.Name, &t.Kind); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// sqlite_stat1 (after ANALYZE) gives a row estimate in its first number.
	if st, err := db.QueryContext(ctx, `SELECT tbl, MAX(CAST(stat AS INTEGER)) FROM `+d.QuoteIdent(schema)+`.sqlite_stat1 GROUP BY tbl`); err == nil {
		est := map[string]int64{}
		for st.Next() {
			var n string
			var c int64
			if st.Scan(&n, &c) == nil {
				est[n] = c
			}
		}
		st.Close()
		for i := range out {
			if c, ok := est[out[i].Name]; ok {
				out[i].EstRows = c
			}
		}
	}
	return out, nil
}

func (d sqliteDriver) Columns(ctx context.Context, db *sql.DB, schema string) (map[string][]model.Column, error) {
	schema = sqliteSchema(schema)
	qs := d.QuoteIdent(schema)
	rows, err := db.QueryContext(ctx, `
SELECT m.name, p.cid, p.name, p.type, p."notnull", p.dflt_value, p.pk, p.hidden
FROM `+qs+`.sqlite_master m
JOIN pragma_table_xinfo(m.name, '`+strings.ReplaceAll(schema, "'", "''")+`') p
WHERE m.type IN ('table', 'view') AND m.name NOT LIKE 'sqlite\_%' ESCAPE '\'
ORDER BY m.name, p.cid`)
	if err != nil {
		// one broken view (dropped base table) or a virtual table of a missing
		// module fails the bulk join: fall back to one pragma per table
		return d.columnsOneByOne(ctx, db, schema)
	}
	defer rows.Close()
	return d.scanColumns(ctx, db, schema, rows)
}

func (d sqliteDriver) columnsOneByOne(ctx context.Context, db *sql.DB, schema string) (map[string][]model.Column, error) {
	names, err := db.QueryContext(ctx, `SELECT name FROM `+d.QuoteIdent(schema)+`.sqlite_master
WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for names.Next() {
		var n string
		if names.Scan(&n) == nil {
			tables = append(tables, n)
		}
	}
	names.Close()
	all := map[string][]model.Column{}
	for _, t := range tables {
		rows, err := db.QueryContext(ctx, `SELECT ?, p.cid, p.name, p.type, p."notnull", p.dflt_value, p.pk, p.hidden
FROM pragma_table_xinfo(?, ?) p ORDER BY p.cid`, t, t, schema)
		if err != nil {
			all[t] = []model.Column{}
			continue
		}
		cols, err := d.scanColumns(ctx, db, schema, rows)
		rows.Close()
		if err != nil {
			all[t] = []model.Column{} // unreadable (broken view): listed without columns
			continue
		}
		for k, v := range cols {
			all[k] = v
		}
		if _, ok := all[t]; !ok {
			all[t] = []model.Column{}
		}
	}
	return all, nil
}

func (d sqliteDriver) scanColumns(ctx context.Context, db *sql.DB, schema string, rows *sql.Rows) (map[string][]model.Column, error) {
	qs := d.QuoteIdent(schema)
	out := map[string][]model.Column{}
	pkCount := map[string]int{}
	for rows.Next() {
		var table, name, typ string
		var cid, notnull, pk, hidden int
		var def sql.NullString
		if err := rows.Scan(&table, &cid, &name, &typ, &notnull, &def, &pk, &hidden); err != nil {
			return nil, err
		}
		c := model.Column{
			Name: name, Ordinal: len(out[table]) + 1, Type: typ, DataType: strings.ToLower(baseType(typ)),
			Kind: d.Kind(typ), Nullable: notnull == 0 && pk == 0, PK: pk > 0, PKOrdinal: pk,
			Generated: hidden == 2 || hidden == 3,
		}
		if c.Type == "" {
			c.Type, c.Kind = "ANY", model.KindOther
		}
		if def.Valid {
			v := def.String
			c.Default = &v
		}
		if pk > 0 {
			pkCount[table]++
		}
		out[table] = append(out[table], c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for t, cols := range out {
		// INTEGER PRIMARY KEY (single) is an alias of rowid => auto increment.
		if pkCount[t] == 1 {
			for i := range cols {
				if cols[i].PK && strings.EqualFold(cols[i].Type, "INTEGER") {
					cols[i].AutoIncrement = true
				}
			}
		}
	}
	// single-column foreign keys
	fks, err := db.QueryContext(ctx, `
SELECT m.name, f."from", f."table", f."to"
FROM `+qs+`.sqlite_master m
JOIN pragma_foreign_key_list(m.name, '`+strings.ReplaceAll(schema, "'", "''")+`') f
WHERE m.type = 'table'
  AND (SELECT COUNT(*) FROM pragma_foreign_key_list(m.name, '`+strings.ReplaceAll(schema, "'", "''")+`') f2 WHERE f2.id = f.id) = 1`)
	if err == nil {
		defer fks.Close()
		for fks.Next() {
			var t, from, refT string
			var to sql.NullString
			if fks.Scan(&t, &from, &refT, &to) != nil {
				continue
			}
			for i := range out[t] {
				if out[t][i].Name == from && out[t][i].FK == nil {
					out[t][i].FK = &model.FKRef{Schema: schema, Table: refT, Column: to.String}
				}
			}
		}
	}
	return out, nil
}

func baseType(t string) string {
	if i := strings.IndexAny(t, "( "); i > 0 {
		return t[:i]
	}
	return t
}

func (sqliteDriver) QuoteIdent(name string) string { return quoteWith(name, '"') }

func (d sqliteDriver) QualifiedName(schema, table string) string {
	if schema == "" || schema == "main" {
		return d.QuoteIdent(table)
	}
	return d.QuoteIdent(schema) + "." + d.QuoteIdent(table)
}

func (sqliteDriver) Placeholder(n int) string { return "?" }

func (sqliteDriver) UseSchema(schema string) string { return "" }

// Kind follows SQLite type affinity rules, with common declared names recognised first.
func (sqliteDriver) Kind(t string) string {
	k := kindFromType(t)
	if k != model.KindOther {
		return k
	}
	u := strings.ToUpper(t)
	switch {
	case strings.Contains(u, "INT"):
		return model.KindInt
	case strings.Contains(u, "CHAR") || strings.Contains(u, "CLOB") || strings.Contains(u, "TEXT"):
		return model.KindText
	case strings.Contains(u, "BLOB") || u == "":
		return model.KindOther
	case strings.Contains(u, "REAL") || strings.Contains(u, "FLOA") || strings.Contains(u, "DOUB"):
		return model.KindFloat
	}
	return model.KindOther
}
