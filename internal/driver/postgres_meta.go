package driver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"dbviewer/internal/model"
)

// pgRelOID resolves schema.table to its pg_class oid.
func pgRelOID(ctx context.Context, db *sql.DB, schema, table string) (uint32, string, error) {
	expr := "current_schema()"
	args := []any{table}
	if schema != "" {
		expr = "$2"
		args = append(args, schema)
	}
	var oid uint32
	var kind string
	err := db.QueryRowContext(ctx, `SELECT c.oid, c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relname = $1 AND n.nspname = `+expr, args...).Scan(&oid, &kind)
	if err == sql.ErrNoRows {
		return 0, "", model.Errf(model.ErrNotFound, "table %s not found", table)
	}
	return oid, kind, err
}

func (postgresDriver) Indexes(ctx context.Context, db *sql.DB, schema, table string) ([]model.Index, error) {
	oid, _, err := pgRelOID(ctx, db, schema, table)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
SELECT ic.relname, i.indisunique, i.indisprimary, am.amname,
       EXISTS (SELECT 1 FROM pg_constraint co WHERE co.conindid = i.indexrelid AND co.conrelid = i.indrelid AND co.contype IN ('p','u','x')),
       COALESCE(pg_get_expr(i.indpred, i.indrelid, true), ''),
       array_to_json(ARRAY(SELECT pg_get_indexdef(i.indexrelid, k, true) FROM generate_series(1, i.indnkeyatts) k ORDER BY k))::text
FROM pg_index i
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_am am ON am.oid = ic.relam
WHERE i.indrelid = $1
ORDER BY i.indisprimary DESC, ic.relname`, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Index{}
	for rows.Next() {
		var x model.Index
		var cols string
		if err := rows.Scan(&x.Name, &x.Unique, &x.Primary, &x.Method, &x.Constraint, &x.Where, &cols); err != nil {
			return nil, err
		}
		x.Columns = parsePGTextArray(cols)
		out = append(out, x)
	}
	return out, rows.Err()
}

// parsePGTextArray parses a JSON array of strings (array_to_json output).
func parsePGTextArray(s string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

const pgFKSelect = `
SELECT co.conname, n.nspname, c.relname, fn.nspname, fc.relname,
       array_to_json(ARRAY(SELECT a.attname FROM unnest(co.conkey) WITH ORDINALITY k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = co.conrelid AND a.attnum = k.attnum ORDER BY k.ord))::text,
       array_to_json(ARRAY(SELECT a.attname FROM unnest(co.confkey) WITH ORDINALITY k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = co.confrelid AND a.attnum = k.attnum ORDER BY k.ord))::text,
       co.confupdtype::text, co.confdeltype::text
FROM pg_constraint co
JOIN pg_class c ON c.oid = co.conrelid JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_class fc ON fc.oid = co.confrelid JOIN pg_namespace fn ON fn.oid = fc.relnamespace
WHERE co.contype = 'f' AND `

func pgFKAction(c string) string {
	switch c {
	case "r":
		return "RESTRICT"
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "d":
		return "SET DEFAULT"
	}
	return "NO ACTION"
}

func scanPGFKs(rows *sql.Rows) ([]model.ForeignKey, error) {
	defer rows.Close()
	out := []model.ForeignKey{}
	for rows.Next() {
		var f model.ForeignKey
		var cols, refCols, upd, del string
		if err := rows.Scan(&f.Name, &f.Schema, &f.Table, &f.RefSchema, &f.RefTable, &cols, &refCols, &upd, &del); err != nil {
			return nil, err
		}
		f.Columns, f.RefColumns = parsePGTextArray(cols), parsePGTextArray(refCols)
		f.OnUpdate, f.OnDelete = pgFKAction(upd), pgFKAction(del)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (postgresDriver) ForeignKeys(ctx context.Context, db *sql.DB, schema, table string) ([]model.ForeignKey, error) {
	oid, _, err := pgRelOID(ctx, db, schema, table)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, pgFKSelect+`co.conrelid = $1 ORDER BY co.conname`, oid)
	if err != nil {
		return nil, err
	}
	return scanPGFKs(rows)
}

func (postgresDriver) ReferencedBy(ctx context.Context, db *sql.DB, schema, table string) ([]model.ForeignKey, error) {
	oid, _, err := pgRelOID(ctx, db, schema, table)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, pgFKSelect+`co.confrelid = $1 ORDER BY n.nspname, c.relname, co.conname`, oid)
	if err != nil {
		return nil, err
	}
	return scanPGFKs(rows)
}

func (postgresDriver) Triggers(ctx context.Context, db *sql.DB, schema, table string) ([]model.Trigger, error) {
	oid, _, err := pgRelOID(ctx, db, schema, table)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT tgname, tgtype, pg_get_triggerdef(oid, true) FROM pg_trigger WHERE tgrelid = $1 AND NOT tgisinternal ORDER BY tgname`, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Trigger{}
	for rows.Next() {
		var t model.Trigger
		var typ int
		if err := rows.Scan(&t.Name, &typ, &t.Definition); err != nil {
			return nil, err
		}
		switch {
		case typ&64 != 0:
			t.Timing = "INSTEAD OF"
		case typ&2 != 0:
			t.Timing = "BEFORE"
		default:
			t.Timing = "AFTER"
		}
		var ev []string
		for _, e := range []struct {
			bit  int
			name string
		}{{4, "INSERT"}, {8, "DELETE"}, {16, "UPDATE"}, {32, "TRUNCATE"}} {
			if typ&e.bit != 0 {
				ev = append(ev, e.name)
			}
		}
		t.Event = strings.Join(ev, ",")
		out = append(out, t)
	}
	return out, rows.Err()
}

// DDL reconstructs CREATE TABLE / VIEW from the catalog, including constraints,
// indexes, comments and triggers.
func (d postgresDriver) DDL(ctx context.Context, db *sql.DB, schema, table string) (string, error) {
	if schema == "" {
		var err error
		if schema, err = d.DefaultSchema(ctx, db); err != nil {
			return "", err
		}
	}
	oid, kind, err := pgRelOID(ctx, db, schema, table)
	if err != nil {
		return "", err
	}
	name := d.QualifiedName(schema, table)
	var b strings.Builder
	switch kind {
	case "v", "m":
		var def string
		if err := db.QueryRowContext(ctx, `SELECT pg_get_viewdef($1::oid, true)`, oid).Scan(&def); err != nil {
			return "", err
		}
		if kind == "v" {
			fmt.Fprintf(&b, "CREATE VIEW %s AS\n%s", name, strings.TrimRight(def, "; \n"))
		} else {
			fmt.Fprintf(&b, "CREATE MATERIALIZED VIEW %s AS\n%s", name, strings.TrimRight(def, "; \n"))
		}
		b.WriteString(";\n")
	default:
		rows, err := db.QueryContext(ctx, `
SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
       pg_get_expr(ad.adbin, ad.adrelid), a.attidentity::text, a.attgenerated::text,
       CASE WHEN a.attcollation <> t.typcollation AND a.attcollation <> 0 THEN (SELECT quote_ident(collname) FROM pg_collation WHERE oid = a.attcollation) END
FROM pg_attribute a
JOIN pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`, oid)
		if err != nil {
			return "", err
		}
		var lines []string
		for rows.Next() {
			var col, typ, ident, gen string
			var notnull bool
			var def, coll sql.NullString
			if err := rows.Scan(&col, &typ, &notnull, &def, &ident, &gen, &coll); err != nil {
				rows.Close()
				return "", err
			}
			l := "  " + d.QuoteIdent(col) + " " + typ
			if coll.Valid {
				l += " COLLATE " + coll.String
			}
			switch {
			case gen == "s":
				l += " GENERATED ALWAYS AS (" + def.String + ") STORED"
			case ident == "a":
				l += " GENERATED ALWAYS AS IDENTITY"
			case ident == "d":
				l += " GENERATED BY DEFAULT AS IDENTITY"
			case def.Valid:
				l += " DEFAULT " + def.String
			}
			if notnull {
				l += " NOT NULL"
			}
			lines = append(lines, l)
		}
		rows.Close()
		crow, err := db.QueryContext(ctx, `SELECT conname, pg_get_constraintdef(oid, true) FROM pg_constraint
WHERE conrelid = $1 AND contype IN ('p','u','f','c','x')
ORDER BY CASE contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'c' THEN 2 WHEN 'x' THEN 3 ELSE 4 END, conname`, oid)
		if err != nil {
			return "", err
		}
		for crow.Next() {
			var cn, def string
			if err := crow.Scan(&cn, &def); err != nil {
				crow.Close()
				return "", err
			}
			lines = append(lines, "  CONSTRAINT "+d.QuoteIdent(cn)+" "+def)
		}
		crow.Close()
		fmt.Fprintf(&b, "CREATE TABLE %s (\n%s\n)", name, strings.Join(lines, ",\n"))
		var partKey sql.NullString
		_ = db.QueryRowContext(ctx, `SELECT pg_get_partkeydef($1::oid)`, oid).Scan(&partKey)
		if partKey.Valid && partKey.String != "" {
			b.WriteString(" PARTITION BY " + partKey.String)
		}
		b.WriteString(";\n")
		irows, err := db.QueryContext(ctx, `SELECT pg_get_indexdef(i.indexrelid) FROM pg_index i
WHERE i.indrelid = $1 AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = i.indexrelid AND c.contype IN ('p','u','x'))
ORDER BY 1`, oid)
		if err != nil {
			return "", err
		}
		first := true
		for irows.Next() {
			var def string
			if err := irows.Scan(&def); err != nil {
				irows.Close()
				return "", err
			}
			if first {
				b.WriteString("\n")
				first = false
			}
			b.WriteString(def + ";\n")
		}
		irows.Close()
	}
	// comments
	var tcomment sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT obj_description($1::oid, 'pg_class')`, oid).Scan(&tcomment)
	var comments []string
	if tcomment.Valid {
		obj := "TABLE"
		if kind == "v" {
			obj = "VIEW"
		} else if kind == "m" {
			obj = "MATERIALIZED VIEW"
		}
		comments = append(comments, fmt.Sprintf("COMMENT ON %s %s IS %s;", obj, name, pgQuoteLit(tcomment.String)))
	}
	crows, err := db.QueryContext(ctx, `SELECT a.attname, col_description($1::oid, a.attnum) FROM pg_attribute a
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped AND col_description($1::oid, a.attnum) IS NOT NULL ORDER BY a.attnum`, oid)
	if err == nil {
		for crows.Next() {
			var col, c string
			if crows.Scan(&col, &c) == nil {
				comments = append(comments, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s;", name, d.QuoteIdent(col), pgQuoteLit(c)))
			}
		}
		crows.Close()
	}
	if len(comments) > 0 {
		b.WriteString("\n" + strings.Join(comments, "\n") + "\n")
	}
	trigs, err := d.Triggers(ctx, db, schema, table)
	if err == nil && len(trigs) > 0 {
		b.WriteString("\n")
		for _, t := range trigs {
			b.WriteString(t.Definition + ";\n")
		}
	}
	return b.String(), nil
}

func pgQuoteLit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
