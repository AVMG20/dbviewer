package driver

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"dbviewer/internal/model"
)

func (mysqlDriver) Indexes(ctx context.Context, db *sql.DB, schema, table string) ([]model.Index, error) {
	expr, args := mysqlSchemaExpr(schema)
	rows, err := db.QueryContext(ctx, `
SELECT index_name, non_unique, column_name, expression, index_type, sub_part
FROM information_schema.statistics
WHERE table_schema = `+expr+` AND table_name = ?
ORDER BY index_name = 'PRIMARY' DESC, index_name, seq_in_index`, append(args, table)...)
	if err != nil {
		// MySQL < 8.0.13 / MariaDB have no `expression` column.
		rows, err = db.QueryContext(ctx, `
SELECT index_name, non_unique, column_name, NULL, index_type, sub_part
FROM information_schema.statistics
WHERE table_schema = `+expr+` AND table_name = ?
ORDER BY index_name = 'PRIMARY' DESC, index_name, seq_in_index`, append(args, table)...)
		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()
	out := []model.Index{}
	pos := map[string]int{}
	for rows.Next() {
		var name, typ string
		var nonUnique int
		var col, ex sql.NullString
		var sub sql.NullInt64
		if err := rows.Scan(&name, &nonUnique, &col, &ex, &typ, &sub); err != nil {
			return nil, err
		}
		i, ok := pos[name]
		if !ok {
			out = append(out, model.Index{Name: name, Columns: []string{}, Unique: nonUnique == 0, Primary: name == "PRIMARY", Method: strings.ToLower(typ)})
			i = len(out) - 1
			pos[name] = i
		}
		c := col.String
		if !col.Valid {
			c = "(" + ex.String + ")"
		}
		if sub.Valid {
			c += "(" + strconv.FormatInt(sub.Int64, 10) + ")"
		}
		out[i].Columns = append(out[i].Columns, c)
	}
	return out, rows.Err()
}

const mysqlFKSelect = `
SELECT k.constraint_name, k.table_schema, k.table_name, k.column_name,
       k.referenced_table_schema, k.referenced_table_name, k.referenced_column_name,
       r.update_rule, r.delete_rule
FROM information_schema.key_column_usage k
JOIN information_schema.referential_constraints r
  ON r.constraint_schema = k.constraint_schema AND r.constraint_name = k.constraint_name AND r.table_name = k.table_name
`

func scanMySQLFKs(rows *sql.Rows) ([]model.ForeignKey, error) {
	defer rows.Close()
	out := []model.ForeignKey{}
	pos := map[string]int{}
	for rows.Next() {
		var f model.ForeignKey
		var col, refCol string
		if err := rows.Scan(&f.Name, &f.Schema, &f.Table, &col, &f.RefSchema, &f.RefTable, &refCol, &f.OnUpdate, &f.OnDelete); err != nil {
			return nil, err
		}
		key := f.Schema + "." + f.Table + "." + f.Name
		i, ok := pos[key]
		if !ok {
			f.Columns, f.RefColumns = []string{}, []string{}
			out = append(out, f)
			i = len(out) - 1
			pos[key] = i
		}
		out[i].Columns = append(out[i].Columns, col)
		out[i].RefColumns = append(out[i].RefColumns, refCol)
	}
	return out, rows.Err()
}

func (mysqlDriver) ForeignKeys(ctx context.Context, db *sql.DB, schema, table string) ([]model.ForeignKey, error) {
	expr, args := mysqlSchemaExpr(schema)
	rows, err := db.QueryContext(ctx, mysqlFKSelect+`
WHERE k.table_schema = `+expr+` AND k.table_name = ? AND k.referenced_table_name IS NOT NULL
ORDER BY k.constraint_name, k.ordinal_position`, append(args, table)...)
	if err != nil {
		return nil, err
	}
	return scanMySQLFKs(rows)
}

func (mysqlDriver) ReferencedBy(ctx context.Context, db *sql.DB, schema, table string) ([]model.ForeignKey, error) {
	expr, args := mysqlSchemaExpr(schema)
	rows, err := db.QueryContext(ctx, mysqlFKSelect+`
WHERE k.referenced_table_schema = `+expr+` AND k.referenced_table_name = ?
ORDER BY k.table_schema, k.table_name, k.constraint_name, k.ordinal_position`, append(args, table)...)
	if err != nil {
		return nil, err
	}
	return scanMySQLFKs(rows)
}

func (mysqlDriver) Triggers(ctx context.Context, db *sql.DB, schema, table string) ([]model.Trigger, error) {
	expr, args := mysqlSchemaExpr(schema)
	rows, err := db.QueryContext(ctx, `
SELECT trigger_name, action_timing, event_manipulation, action_statement
FROM information_schema.triggers
WHERE event_object_schema = `+expr+` AND event_object_table = ?
ORDER BY action_timing, event_manipulation, action_order`, append(args, table)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Trigger{}
	for rows.Next() {
		var t model.Trigger
		var body string
		if err := rows.Scan(&t.Name, &t.Timing, &t.Event, &body); err != nil {
			return nil, err
		}
		t.Definition = "CREATE TRIGGER `" + t.Name + "` " + t.Timing + " " + t.Event + " ON `" + table + "` FOR EACH ROW " + body
		out = append(out, t)
	}
	return out, rows.Err()
}
