package driver

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"dbviewer/internal/model"
)

func sqliteLit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (d sqliteDriver) Indexes(ctx context.Context, db *sql.DB, schema, table string) ([]model.Index, error) {
	schema = sqliteSchema(schema)
	rows, err := db.QueryContext(ctx, `SELECT name, "unique", origin, partial FROM pragma_index_list(?, ?) ORDER BY seq`, table, schema)
	if err != nil {
		return nil, err
	}
	type ix struct {
		name    string
		unique  bool
		origin  string
		partial bool
	}
	var list []ix
	for rows.Next() {
		var x ix
		var u, p int
		if err := rows.Scan(&x.name, &u, &x.origin, &p); err != nil {
			rows.Close()
			return nil, err
		}
		x.unique, x.partial = u == 1, p == 1
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []model.Index{}
	hasPK := false
	for _, x := range list {
		cr, err := db.QueryContext(ctx, `SELECT name FROM pragma_index_info(?, ?) ORDER BY seqno`, x.name, schema)
		if err != nil {
			return nil, err
		}
		cols := []string{}
		for cr.Next() {
			var n sql.NullString
			if err := cr.Scan(&n); err != nil {
				cr.Close()
				return nil, err
			}
			if n.Valid {
				cols = append(cols, n.String)
			} else {
				cols = append(cols, "<expression>")
			}
		}
		cr.Close()
		idx := model.Index{Name: x.name, Columns: cols, Unique: x.unique, Primary: x.origin == "pk", Method: "btree"}
		if x.partial {
			var def sql.NullString
			_ = db.QueryRowContext(ctx, `SELECT sql FROM `+d.QuoteIdent(schema)+`.sqlite_master WHERE type = 'index' AND name = ?`, x.name).Scan(&def)
			if i := strings.LastIndex(strings.ToUpper(def.String), " WHERE "); i >= 0 {
				idx.Where = strings.TrimSpace(def.String[i+7:])
			}
		}
		hasPK = hasPK || idx.Primary
		out = append(out, idx)
	}
	if !hasPK {
		// INTEGER PRIMARY KEY (rowid alias) has no index entry: synthesize it.
		cols, err := d.Columns(ctx, db, schema)
		if err == nil {
			var pk []model.Column
			for _, c := range cols[table] {
				if c.PK {
					pk = append(pk, c)
				}
			}
			sort.Slice(pk, func(i, j int) bool { return pk[i].PKOrdinal < pk[j].PKOrdinal })
			if len(pk) > 0 {
				names := make([]string, len(pk))
				for i, c := range pk {
					names[i] = c.Name
				}
				out = append([]model.Index{{Name: "PRIMARY", Columns: names, Unique: true, Primary: true, Method: "rowid"}}, out...)
			}
		}
	}
	return out, nil
}

func sqliteFKAction(s string) string {
	if s == "" {
		return "NO ACTION"
	}
	return s
}

func (d sqliteDriver) ForeignKeys(ctx context.Context, db *sql.DB, schema, table string) ([]model.ForeignKey, error) {
	schema = sqliteSchema(schema)
	rows, err := db.QueryContext(ctx, `SELECT id, "table", "from", "to", on_update, on_delete FROM pragma_foreign_key_list(?, ?) ORDER BY id, seq`, table, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteFKs(rows, schema, func(int) string { return table })
}

func scanSQLiteFKs(rows *sql.Rows, schema string, tableOf func(id int) string) ([]model.ForeignKey, error) {
	out := []model.ForeignKey{}
	byID := map[string]int{}
	for rows.Next() {
		var id int
		var refTable, from, onU, onD string
		var to sql.NullString
		var owner string
		cols, _ := rows.Columns()
		var err error
		if len(cols) == 7 {
			err = rows.Scan(&owner, &id, &refTable, &from, &to, &onU, &onD)
		} else {
			err = rows.Scan(&id, &refTable, &from, &to, &onU, &onD)
			owner = tableOf(id)
		}
		if err != nil {
			return nil, err
		}
		key := owner + "\x00" + fmt.Sprint(id)
		i, ok := byID[key]
		if !ok {
			out = append(out, model.ForeignKey{
				Name: fmt.Sprintf("%s_fk_%d", owner, id), Schema: schema, Table: owner, Columns: []string{},
				RefSchema: schema, RefTable: refTable, RefColumns: []string{}, OnUpdate: sqliteFKAction(onU), OnDelete: sqliteFKAction(onD),
			})
			i = len(out) - 1
			byID[key] = i
		}
		out[i].Columns = append(out[i].Columns, from)
		out[i].RefColumns = append(out[i].RefColumns, to.String) // "" = referenced table's PK
	}
	return out, rows.Err()
}

func (d sqliteDriver) ReferencedBy(ctx context.Context, db *sql.DB, schema, table string) ([]model.ForeignKey, error) {
	schema = sqliteSchema(schema)
	rows, err := db.QueryContext(ctx, `
SELECT m.name, f.id, f."table", f."from", f."to", f.on_update, f.on_delete
FROM `+d.QuoteIdent(schema)+`.sqlite_master m
JOIN pragma_foreign_key_list(m.name, `+sqliteLit(schema)+`) f
WHERE m.type = 'table' AND f."table" = ? COLLATE NOCASE
ORDER BY m.name, f.id, f.seq`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteFKs(rows, schema, nil)
}

var sqliteTrigRe = regexp.MustCompile(`(?is)CREATE\s+(?:TEMP\w*\s+)?TRIGGER\s+(?:IF\s+NOT\s+EXISTS\s+)?\S+\s+(BEFORE|AFTER|INSTEAD\s+OF)?\s*(INSERT|DELETE|UPDATE)`)

func (d sqliteDriver) Triggers(ctx context.Context, db *sql.DB, schema, table string) ([]model.Trigger, error) {
	schema = sqliteSchema(schema)
	rows, err := db.QueryContext(ctx, `SELECT name, sql FROM `+d.QuoteIdent(schema)+`.sqlite_master WHERE type = 'trigger' AND tbl_name = ? ORDER BY name`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Trigger{}
	for rows.Next() {
		var t model.Trigger
		var def sql.NullString
		if err := rows.Scan(&t.Name, &def); err != nil {
			return nil, err
		}
		t.Definition = def.String
		t.Timing = "BEFORE"
		if m := sqliteTrigRe.FindStringSubmatch(def.String); m != nil {
			if m[1] != "" {
				t.Timing = strings.ToUpper(strings.Join(strings.Fields(m[1]), " "))
			}
			t.Event = strings.ToUpper(m[2])
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d sqliteDriver) DDL(ctx context.Context, db *sql.DB, schema, table string) (string, error) {
	schema = sqliteSchema(schema)
	rows, err := db.QueryContext(ctx, `SELECT type, sql FROM `+d.QuoteIdent(schema)+`.sqlite_master
WHERE tbl_name = ? AND sql IS NOT NULL
ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'view' THEN 0 WHEN 'index' THEN 1 ELSE 2 END, name`, table)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var typ, s string
		if err := rows.Scan(&typ, &s); err != nil {
			return "", err
		}
		parts = append(parts, strings.TrimSpace(s)+";")
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return "", model.Errf(model.ErrNotFound, "table %s not found", table)
	}
	return strings.Join(parts, "\n\n") + "\n", nil
}
