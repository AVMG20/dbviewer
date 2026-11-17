// Package changes turns a staged ChangeSet into SQL and applies it in one transaction.
package changes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"dbviewer/internal/conn"
	"dbviewer/internal/model"
	"dbviewer/internal/query"
)

func dialectOf(p *conn.Pool) dialect {
	return dialect{driver: p.Config.Driver, quote: p.Driver.QuoteIdent, ph: p.Driver.Placeholder}
}

type tableInfo struct {
	cols  map[string]model.Column
	order []model.Column
	pk    []model.Column
}

func newTableInfo(cols []model.Column) tableInfo {
	t := tableInfo{cols: map[string]model.Column{}, order: cols}
	for _, c := range cols {
		t.cols[c.Name] = c
		if c.PK {
			t.pk = append(t.pk, c)
		}
	}
	sort.Slice(t.pk, func(i, j int) bool { return t.pk[i].PKOrdinal < t.pk[j].PKOrdinal })
	return t
}

// sortedKeys returns map keys in table column order (unknown keys last, sorted).
func (t tableInfo) sortedKeys(m map[string]any) ([]string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		if _, ok := t.cols[k]; !ok {
			return nil, model.Errf(model.ErrInvalid, "unknown column %q", k)
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return t.cols[keys[i]].Ordinal < t.cols[keys[j]].Ordinal })
	return keys, nil
}

func (t tableInfo) where(d dialect, s *stmt, pk map[string]any) error {
	if len(t.pk) == 0 {
		return model.Errf(model.ErrNoPrimaryKey, "table has no primary key; rows cannot be updated or deleted")
	}
	if len(pk) != len(t.pk) {
		return model.Errf(model.ErrInvalid, "pk must contain exactly the primary key columns")
	}
	s.raw(" WHERE ")
	for i, c := range t.pk {
		raw, ok := pk[c.Name]
		if !ok {
			return model.Errf(model.ErrInvalid, "pk is missing column %q", c.Name)
		}
		v, err := decode(raw, c, d.driver)
		if err != nil {
			return err
		}
		if v.isDefault || v.arg == nil {
			return model.Errf(model.ErrInvalid, "pk column %q cannot be NULL/DEFAULT", c.Name)
		}
		if i > 0 {
			s.raw(" AND ")
		}
		s.raw(d.quote(c.Name) + " = ")
		s.val(d, v, c)
	}
	return nil
}

// build generates the statements for a change set: deletes, updates, inserts.
func build(p *conn.Pool, cs model.ChangeSet, cols []model.Column) ([]*stmt, error) {
	d := dialectOf(p)
	t := newTableInfo(cols)
	if len(cols) == 0 {
		return nil, model.Errf(model.ErrNotFound, "table %s not found", cs.Table)
	}
	name := p.Driver.QualifiedName(p.Schema(cs.Schema), cs.Table)
	var out []*stmt

	for _, del := range cs.Deletes {
		s := &stmt{expectOne: true}
		s.raw("DELETE FROM " + name)
		if err := t.where(d, s, del.PK); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	for _, up := range cs.Updates {
		if len(up.Set) == 0 {
			continue
		}
		keys, err := t.sortedKeys(up.Set)
		if err != nil {
			return nil, err
		}
		s := &stmt{expectOne: true}
		s.raw("UPDATE " + name + " SET ")
		for i, k := range keys {
			c := t.cols[k]
			if c.Generated {
				return nil, model.Errf(model.ErrInvalid, "column %q is generated and cannot be set", k)
			}
			v, err := decode(up.Set[k], c, d.driver)
			if err != nil {
				return nil, err
			}
			if i > 0 {
				s.raw(", ")
			}
			s.raw(d.quote(k) + " = ")
			if v.isDefault && d.driver == model.DriverSQLite {
				// SQLite has no DEFAULT keyword in UPDATE: use the declared default.
				if c.Default != nil {
					s.raw(*c.Default)
				} else {
					s.raw("NULL")
				}
				continue
			}
			s.val(d, v, c)
		}
		if err := t.where(d, s, up.PK); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	for _, ins := range cs.Inserts {
		keys, err := t.sortedKeys(ins.Values)
		if err != nil {
			return nil, err
		}
		s := &stmt{pkValues: map[string]any{}}
		var vals []value
		var vcols []model.Column
		for _, k := range keys {
			c := t.cols[k]
			if c.Generated {
				return nil, model.Errf(model.ErrInvalid, "column %q is generated and cannot be set", k)
			}
			v, err := decode(ins.Values[k], c, d.driver)
			if err != nil {
				return nil, err
			}
			if v.isDefault && d.driver == model.DriverSQLite {
				continue // omitted column gets its default
			}
			vals = append(vals, v)
			vcols = append(vcols, c)
			if c.PK && !v.isDefault {
				s.pkValues[c.Name] = query.Encode(v.arg, c.Kind, true)
			}
		}
		s.raw("INSERT INTO " + name)
		if len(vcols) == 0 {
			if d.driver == model.DriverMySQL {
				s.raw(" () VALUES ()")
			} else {
				s.raw(" DEFAULT VALUES")
			}
		} else {
			names := make([]string, len(vcols))
			for i, c := range vcols {
				names[i] = d.quote(c.Name)
			}
			s.raw(" (" + strings.Join(names, ", ") + ") VALUES (")
			for i, v := range vals {
				if i > 0 {
					s.raw(", ")
				}
				s.val(d, v, vcols[i])
			}
			s.raw(")")
		}
		if len(t.pk) > 0 {
			switch d.driver {
			case model.DriverPostgres, model.DriverSQLite:
				names := make([]string, len(t.pk))
				for i, c := range t.pk {
					names[i] = d.quote(c.Name)
					s.returning = append(s.returning, c.Name)
				}
				s.sql.WriteString(" RETURNING " + strings.Join(names, ", "))
			case model.DriverMySQL:
				if len(t.pk) == 1 && t.pk[0].AutoIncrement {
					if _, given := s.pkValues[t.pk[0].Name]; !given {
						s.pkAutoInc = t.pk[0].Name
					}
				}
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// Preview returns the statements Apply would run (literals inlined for display),
// in execution order: deletes, updates, inserts.
func Preview(ctx context.Context, p *conn.Pool, cs model.ChangeSet, cols []model.Column) ([]string, error) {
	stmts, err := build(p, cs, cols)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(stmts))
	for i, s := range stmts {
		out[i] = s.display.String() + ";"
	}
	return out, nil
}

// Apply runs the change set in one transaction with bound parameters. Read-only
// connections are refused before anything runs. Each UPDATE/DELETE must affect
// exactly one row, otherwise everything is rolled back. DB failures are reported
// in ApplyResult (OK=false, FailedIndex, Error) after rollback, not as a Go error.
func Apply(ctx context.Context, p *conn.Pool, cs model.ChangeSet, cols []model.Column) (model.ApplyResult, error) {
	res := model.ApplyResult{FailedIndex: -1, Statements: []string{}, InsertedPKs: []map[string]any{}}
	if p.ReadOnly() {
		return res, model.Errf(model.ErrReadOnly, "connection %q is read-only", p.Config.Name)
	}
	stmts, err := build(p, cs, cols)
	if err != nil {
		return res, err
	}
	for _, s := range stmts {
		res.Statements = append(res.Statements, s.display.String()+";")
	}
	if len(stmts) == 0 {
		res.OK = true
		return res, nil
	}
	colByName := map[string]model.Column{}
	for _, c := range cols {
		colByName[c.Name] = c
	}
	start := time.Now()
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return res, model.Wrap(model.ErrDB, err)
	}
	fail := func(i int, err error) (model.ApplyResult, error) {
		_ = tx.Rollback()
		res.OK, res.FailedIndex, res.Executed = false, i, i
		res.Error = err.Error()
		if errors.Is(err, context.Canceled) {
			res.Error = "canceled"
		}
		res.RowsAffected = 0
		res.InsertedPKs = []map[string]any{}
		res.DurationMs = time.Since(start).Milliseconds()
		return res, nil
	}
	for i, s := range stmts {
		q := s.sql.String()
		if len(s.returning) > 0 {
			vals := make([]any, len(s.returning))
			ptrs := make([]any, len(vals))
			for j := range vals {
				ptrs[j] = &vals[j]
			}
			if err := tx.QueryRowContext(ctx, q, s.args...).Scan(ptrs...); err != nil {
				return fail(i, err)
			}
			pk := map[string]any{}
			for j, n := range s.returning {
				pk[n] = query.Encode(vals[j], colByName[n].Kind, true)
			}
			res.InsertedPKs = append(res.InsertedPKs, pk)
			res.RowsAffected++
			continue
		}
		r, err := tx.ExecContext(ctx, q, s.args...)
		if err != nil {
			return fail(i, err)
		}
		n, err := r.RowsAffected()
		if err != nil {
			return fail(i, err)
		}
		if s.expectOne && n != 1 {
			verb := strings.SplitN(s.display.String(), " ", 2)[0]
			if n == 0 {
				return fail(i, fmt.Errorf("%s matched no row (expected exactly 1): the row was changed or deleted in the meantime", verb))
			}
			return fail(i, fmt.Errorf("%s matched %d rows (expected exactly 1): the primary key is not unique", verb, n))
		}
		res.RowsAffected += n
		if s.pkValues != nil {
			pk := s.pkValues
			if s.pkAutoInc != "" {
				if id, err := r.LastInsertId(); err == nil {
					pk = map[string]any{s.pkAutoInc: query.Encode(id, model.KindInt, true)}
				}
			}
			res.InsertedPKs = append(res.InsertedPKs, pk)
		}
	}
	if err := tx.Commit(); err != nil {
		return fail(len(stmts)-1, fmt.Errorf("commit failed: %w", err))
	}
	res.OK, res.Executed = true, len(stmts)
	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}

// RowsAsSQL renders rows as INSERT or UPDATE statements in the pool's dialect.
// cols is the table metadata (may be nil for ad-hoc results; kinds are then inferred).
func RowsAsSQL(p *conn.Pool, req model.CopySQLRequest, cols []model.Column) (string, error) {
	d := dialectOf(p)
	t := newTableInfo(cols)
	colFor := func(name string, sample any) model.Column {
		if c, ok := t.cols[name]; ok {
			return c
		}
		c := model.Column{Name: name, Kind: model.KindString}
		switch sample.(type) {
		case float64, int64:
			c.Kind = model.KindFloat
		case bool:
			c.Kind = model.KindBool
		}
		return c
	}
	if len(req.Columns) == 0 {
		return "", model.Errf(model.ErrInvalid, "columns are required")
	}
	name := d.quote(req.Table)
	if req.Schema != "" && req.Schema != p.DefaultSchema {
		name = p.Driver.QualifiedName(req.Schema, req.Table)
	}
	var b strings.Builder
	switch req.Kind {
	case "insert", "":
		qcols := make([]string, len(req.Columns))
		for i, c := range req.Columns {
			qcols[i] = d.quote(c)
		}
		for _, row := range req.Rows {
			if len(row) != len(req.Columns) {
				return "", model.Errf(model.ErrInvalid, "row has %d values, expected %d", len(row), len(req.Columns))
			}
			b.WriteString("INSERT INTO " + name + " (" + strings.Join(qcols, ", ") + ") VALUES (")
			for i, raw := range row {
				c := colFor(req.Columns[i], raw)
				v, err := decode(raw, c, d.driver)
				if err != nil {
					return "", err
				}
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(d.literal(v, c))
			}
			b.WriteString(");\n")
		}
	case "update":
		pkCols := req.PKCols
		if len(pkCols) == 0 {
			for _, c := range t.pk {
				pkCols = append(pkCols, c.Name)
			}
		}
		if len(pkCols) == 0 {
			return "", model.Errf(model.ErrNoPrimaryKey, "no primary key: cannot build UPDATE statements")
		}
		idx := map[string]int{}
		for i, c := range req.Columns {
			idx[c] = i
		}
		isPK := map[string]bool{}
		for _, k := range pkCols {
			if _, ok := idx[k]; !ok {
				return "", model.Errf(model.ErrInvalid, "primary key column %q is not among the copied columns", k)
			}
			isPK[k] = true
		}
		for _, row := range req.Rows {
			if len(row) != len(req.Columns) {
				return "", model.Errf(model.ErrInvalid, "row has %d values, expected %d", len(row), len(req.Columns))
			}
			var sets, conds []string
			for i, cn := range req.Columns {
				c := colFor(cn, row[i])
				v, err := decode(row[i], c, d.driver)
				if err != nil {
					return "", err
				}
				if isPK[cn] {
					conds = append(conds, d.quote(cn)+" = "+d.literal(v, c))
				} else {
					sets = append(sets, d.quote(cn)+" = "+d.literal(v, c))
				}
			}
			if len(sets) == 0 {
				continue
			}
			b.WriteString("UPDATE " + name + " SET " + strings.Join(sets, ", ") + " WHERE " + strings.Join(conds, " AND ") + ";\n")
		}
	default:
		return "", model.Errf(model.ErrInvalid, "unknown kind %q (insert|update)", req.Kind)
	}
	return b.String(), nil
}

// CellValue loads the full value of one cell identified by its primary key.
func CellValue(ctx context.Context, p *conn.Pool, req model.CellRequest, cols []model.Column) (any, error) {
	d := dialectOf(p)
	t := newTableInfo(cols)
	col, ok := t.cols[req.Column]
	if !ok {
		return nil, model.Errf(model.ErrInvalid, "unknown column %q", req.Column)
	}
	s := &stmt{}
	s.raw("SELECT " + query.SelectExpr(p, col) + " FROM " + p.Driver.QualifiedName(p.Schema(req.Schema), req.Table))
	if err := t.where(d, s, req.PK); err != nil {
		return nil, err
	}
	var v any
	err := p.Read(ctx, false, func(q conn.Querier) error {
		return q.QueryRowContext(ctx, s.sql.String(), s.args...).Scan(&v)
	})
	if err == sql.ErrNoRows {
		return nil, model.Errf(model.ErrNotFound, "row not found")
	}
	if err != nil {
		return nil, model.Wrap(model.ErrDB, err)
	}
	return query.Encode(v, query.EncodeKind(col), true), nil
}
