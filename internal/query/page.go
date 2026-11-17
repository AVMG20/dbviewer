// Package query builds and runs paged table selects and console queries.
package query

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"dbviewer/internal/conn"
	"dbviewer/internal/model"
)

const (
	DefaultPageSize = 500
	MaxPageSize     = 10000
)

// builder accumulates SQL with dialect placeholders.
type builder struct {
	p    *conn.Pool
	args []any
}

func (b *builder) arg(v any) string {
	b.args = append(b.args, v)
	return b.p.Driver.Placeholder(len(b.args))
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// where builds the WHERE clause (without keyword) from raw where + column filters.
func (b *builder) where(req model.PageRequest, cols []model.Column) (string, error) {
	var parts []string
	if w := strings.TrimSpace(req.Where); w != "" {
		parts = append(parts, "("+w+"\n)") // newline: a trailing -- comment must not eat the ")"
	}
	known := map[string]model.Column{}
	for _, c := range cols {
		known[c.Name] = c
	}
	d := b.p.Driver
	for _, f := range req.ColumnFilters {
		col, ok := known[f.Column]
		if !ok {
			return "", model.Errf(model.ErrInvalid, "unknown column %q", f.Column)
		}
		q := d.QuoteIdent(f.Column)
		text := q
		if d.Name() == model.DriverPostgres && col.Kind != model.KindString && col.Kind != model.KindText {
			text = "CAST(" + q + " AS TEXT)"
		}
		like := "LIKE"
		if d.Name() == model.DriverPostgres {
			like = "ILIKE"
		}
		esc := ` ESCAPE '\'`
		if d.Name() == model.DriverMySQL {
			esc = ` ESCAPE '\\'`
		}
		switch f.Op {
		case "eq":
			parts = append(parts, q+" = "+b.arg(f.Value))
		case "ne":
			parts = append(parts, "("+q+" <> "+b.arg(f.Value)+" OR "+q+" IS NULL)")
		case "gt":
			parts = append(parts, q+" > "+b.arg(f.Value))
		case "gte":
			parts = append(parts, q+" >= "+b.arg(f.Value))
		case "lt":
			parts = append(parts, q+" < "+b.arg(f.Value))
		case "lte":
			parts = append(parts, q+" <= "+b.arg(f.Value))
		case "contains":
			parts = append(parts, text+" "+like+" "+b.arg("%"+likeEscape(f.Value)+"%")+esc)
		case "not_contains":
			parts = append(parts, "("+text+" NOT "+like+" "+b.arg("%"+likeEscape(f.Value)+"%")+esc+" OR "+q+" IS NULL)")
		case "starts_with":
			parts = append(parts, text+" "+like+" "+b.arg(likeEscape(f.Value)+"%")+esc)
		case "ends_with":
			parts = append(parts, text+" "+like+" "+b.arg("%"+likeEscape(f.Value))+esc)
		case "like":
			parts = append(parts, text+" "+like+" "+b.arg(f.Value))
		case "not_like":
			parts = append(parts, text+" NOT "+like+" "+b.arg(f.Value))
		case "is_null":
			parts = append(parts, q+" IS NULL")
		case "not_null":
			parts = append(parts, q+" IS NOT NULL")
		case "in", "not_in":
			if len(f.Values) == 0 {
				return "", model.Errf(model.ErrInvalid, "filter %s on %s needs values", f.Op, f.Column)
			}
			ph := make([]string, len(f.Values))
			for i, v := range f.Values {
				ph[i] = b.arg(v)
			}
			op := " IN ("
			if f.Op == "not_in" {
				op = " NOT IN ("
			}
			parts = append(parts, q+op+strings.Join(ph, ", ")+")")
		case "between":
			if len(f.Values) != 2 {
				return "", model.Errf(model.ErrInvalid, "between on %s needs 2 values", f.Column)
			}
			parts = append(parts, q+" BETWEEN "+b.arg(f.Values[0])+" AND "+b.arg(f.Values[1]))
		default:
			return "", model.Errf(model.ErrInvalid, "unknown filter op %q", f.Op)
		}
	}
	return strings.Join(parts, " AND "), nil
}

// ResultColumns converts metadata columns into result columns.
func ResultColumns(cols []model.Column) []model.ResultColumn {
	out := make([]model.ResultColumn, len(cols))
	for i, c := range cols {
		out[i] = model.ResultColumn{Name: c.Name, Type: c.Type, Kind: c.Kind, Nullable: c.Nullable, PK: c.PK, FK: c.FK}
	}
	return out
}

// tieBreaker returns a unique sort key so LIMIT/OFFSET paging is stable: the
// primary key, else ctid (Postgres tables), rowid (SQLite tables) or all columns
// (MySQL). Postgres/SQLite views without a key get none.
func tieBreaker(p *conn.Pool, cols []model.Column, tableKind string) string {
	d := p.Driver
	var pk []string
	for _, c := range sortedPK(cols) {
		pk = append(pk, d.QuoteIdent(c.Name))
	}
	if len(pk) > 0 {
		return strings.Join(pk, ", ")
	}
	switch p.Config.Driver {
	case model.DriverPostgres:
		if tableKind == "table" {
			return "ctid"
		}
	case model.DriverSQLite:
		if tableKind == "table" {
			return "rowid"
		}
	case model.DriverMySQL:
		pos := make([]string, len(cols))
		for i := range cols {
			pos[i] = fmt.Sprint(i + 1)
		}
		return strings.Join(pos, ", ")
	}
	return ""
}

// SelectExpr is the select-list expression for a column. SQLite's driver turns
// text in DATE/DATETIME/TIMESTAMP columns into time.Time (losing the stored
// form); selecting it through an expression keeps the stored text.
func SelectExpr(p *conn.Pool, c model.Column) string {
	q := p.Driver.QuoteIdent(c.Name)
	if p.Config.Driver == model.DriverSQLite {
		switch strings.ToUpper(strings.TrimSpace(c.Type)) {
		case "DATE", "DATETIME", "TIMESTAMP":
			return "CASE WHEN typeof(" + q + ") = 'text' THEN CAST(" + q + " AS TEXT) ELSE " + q + " END AS " + q
		}
	}
	return q
}

// BuildSelect builds the SELECT for a table request. limit <= 0 means no LIMIT
// (export). Without OrderBy the rows are ordered by the primary key; a user
// ORDER BY gets the key appended as a tie-breaker. tableKind is table/view/...
func BuildSelect(p *conn.Pool, req model.PageRequest, cols []model.Column, limit int, offset int64, tableKind string) (string, []any, string, error) {
	b := &builder{p: p}
	where, err := b.where(req, cols)
	if err != nil {
		return "", nil, "", err
	}
	d := p.Driver
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = SelectExpr(p, c)
	}
	q := "SELECT " + strings.Join(sel, ", ") + " FROM " + d.QualifiedName(p.Schema(req.Schema), req.Table)
	if where != "" {
		q += " WHERE " + where
	}
	order := strings.TrimSpace(req.OrderBy)
	tie := tieBreaker(p, cols, tableKind)
	switch {
	case order == "":
		order = tie
	case tie != "":
		order += "\n, " + tie
	}
	if order != "" {
		q += " ORDER BY " + order
	}
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	} else if offset > 0 {
		return "", nil, "", model.Errf(model.ErrInvalid, "offset without limit")
	}
	return q, b.args, where, nil
}

// FetchPage selects one page of a table. cols are the table's metadata columns.
func FetchPage(ctx context.Context, p *conn.Pool, req model.PageRequest, cols []model.Column, tableKind string) (model.QueryResult, error) {
	start := time.Now()
	schema := p.Schema(req.Schema)
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	if req.Offset < 0 {
		req.Offset = 0
	}
	q, args, where, err := BuildSelect(p, req, cols, limit+1, req.Offset, tableKind)
	if err != nil {
		return model.QueryResult{}, err
	}
	res := model.QueryResult{
		Statement: q, Columns: ResultColumns(cols), Rows: [][]any{}, HasResultSet: true, RowsAffected: -1,
		Total: -1, EstimatedTotal: -1, Offset: req.Offset, Source: &model.TableRef{ConnID: p.Config.ID, Schema: schema, Table: req.Table},
	}
	kinds := make([]string, len(cols))
	for i, c := range cols {
		kinds[i] = EncodeKind(c)
	}
	err = p.Read(ctx, false, func(qr conn.Querier) error {
		rows, err := qr.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		res.Rows, res.HasMore, err = scanRows(rows, kinds, limit)
		rows.Close() // before the next query: a transaction has one connection
		if err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if req.CountTotal {
			if n, err := countWhere(ctx, qr, p, schema, req.Table, where, args); err == nil {
				res.Total = n
			}
		}
		return nil
	})
	if err != nil {
		return res, model.Wrap(model.ErrDB, err)
	}
	for _, r := range res.Rows {
		for _, v := range r {
			if t, ok := v.(LongText); ok && t.Truncated {
				res.Truncated = true
			}
		}
	}
	if !req.CountTotal && !res.HasMore {
		res.Total = req.Offset + int64(len(res.Rows))
	}
	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}

func sortedPK(cols []model.Column) []model.Column {
	var pk []model.Column
	for i := 1; i <= len(cols); i++ {
		for _, c := range cols {
			if c.PKOrdinal == i {
				pk = append(pk, c)
			}
		}
	}
	return pk
}

// CountRows counts rows matching the request's Where + ColumnFilters.
func CountRows(ctx context.Context, p *conn.Pool, req model.PageRequest, cols []model.Column) (int64, error) {
	b := &builder{p: p}
	where, err := b.where(req, cols)
	if err != nil {
		return 0, err
	}
	var n int64
	err = p.Read(ctx, false, func(qr conn.Querier) error {
		var err error
		n, err = countWhere(ctx, qr, p, p.Schema(req.Schema), req.Table, where, b.args)
		return err
	})
	return n, model.Wrap(model.ErrDB, err)
}

func countWhere(ctx context.Context, qr conn.Querier, p *conn.Pool, schema, table, where string, args []any) (int64, error) {
	q := "SELECT COUNT(*) FROM " + p.Driver.QualifiedName(schema, table)
	if where != "" {
		q += " WHERE " + where
	}
	var n int64
	err := qr.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// ScanRows reads up to max rows (max <= 0 = unlimited), encoding each cell.
func ScanRows(rows *sql.Rows, kinds []string, max int) ([][]any, bool, error) {
	return scanRows(rows, kinds, max)
}

// scanRows reads up to max rows (max <= 0 = unlimited) and reports whether more exist.
func scanRows(rows *sql.Rows, kinds []string, max int) ([][]any, bool, error) {
	out := [][]any{}
	n := len(kinds)
	for rows.Next() {
		if max > 0 && len(out) >= max {
			return out, true, nil
		}
		vals := make([]any, n)
		ptrs := make([]any, n)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return out, false, err
		}
		row := make([]any, n)
		for i, v := range vals {
			row[i] = Encode(v, kinds[i], false)
		}
		out = append(out, row)
	}
	return out, false, rows.Err()
}
