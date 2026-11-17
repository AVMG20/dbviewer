package changes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"dbviewer/internal/conn"
	"dbviewer/internal/model"
)

// Type and default expressions are DDL typed by the user; they cannot be bound as
// parameters, so they are validated against a conservative pattern instead.
var (
	typeRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_ ]*(\(\s*\d+\s*(,\s*\d+\s*)?\))?( (unsigned|zerofill|with time zone|without time zone|varying))*(\[\])?$`)
	enumRe    = regexp.MustCompile(`^(?i:enum|set)\(\s*'([^'\\]|'')*'(\s*,\s*'([^'\\]|'')*')*\s*\)$`)
	badExprRe = regexp.MustCompile(`;|--|/\*|\*/`)
)

func validType(t string) bool {
	t = strings.TrimSpace(t)
	return typeRe.MatchString(t) || enumRe.MatchString(t)
}

// validDefault accepts literals and simple expressions without statement separators
// or comments; quotes must be balanced.
func validDefault(e string) bool {
	if badExprRe.MatchString(e) {
		return false
	}
	return strings.Count(e, "'")%2 == 0 && strings.Count(e, `"`)%2 == 0
}

// StructureStatements generates the DDL for a structure change.
func StructureStatements(p *conn.Pool, ch model.StructureChange, cols []model.Column, indexes []model.Index) ([]string, error) {
	d := dialectOf(p)
	schema := p.Schema(ch.Schema)
	table := p.Driver.QualifiedName(schema, ch.Table)
	known := map[string]bool{}
	for _, c := range cols {
		known[c.Name] = true
	}
	if len(cols) == 0 {
		return nil, model.Errf(model.ErrNotFound, "table %s not found", ch.Table)
	}
	idx := map[string]model.Index{}
	for _, i := range indexes {
		idx[i.Name] = i
	}
	var out []string
	for _, name := range ch.DropIndexes {
		ix, ok := idx[name]
		if !ok {
			return nil, model.Errf(model.ErrInvalid, "unknown index %q", name)
		}
		switch d.driver {
		case model.DriverMySQL:
			if ix.Primary {
				out = append(out, "ALTER TABLE "+table+" DROP PRIMARY KEY")
			} else {
				out = append(out, "ALTER TABLE "+table+" DROP INDEX "+d.quote(name))
			}
		case model.DriverPostgres:
			if ix.Constraint {
				// a constraint-backed index is dropped by dropping its constraint
				out = append(out, "ALTER TABLE "+table+" DROP CONSTRAINT "+d.quote(name))
			} else {
				out = append(out, "DROP INDEX "+p.Driver.QualifiedName(schema, name))
			}
		case model.DriverSQLite:
			if ix.Primary {
				return nil, model.Errf(model.ErrInvalid, "SQLite cannot drop a primary key")
			}
			out = append(out, "DROP INDEX "+p.Driver.QualifiedName(schema, name))
		}
	}
	for _, c := range ch.DropColumns {
		if !known[c] {
			return nil, model.Errf(model.ErrInvalid, "unknown column %q", c)
		}
		out = append(out, "ALTER TABLE "+table+" DROP COLUMN "+d.quote(c))
	}
	olds := make([]string, 0, len(ch.RenameColumns))
	for o := range ch.RenameColumns {
		olds = append(olds, o)
	}
	sort.Strings(olds)
	for _, o := range olds {
		n := strings.TrimSpace(ch.RenameColumns[o])
		if !known[o] {
			return nil, model.Errf(model.ErrInvalid, "unknown column %q", o)
		}
		if n == "" || n == o {
			continue
		}
		out = append(out, "ALTER TABLE "+table+" RENAME COLUMN "+d.quote(o)+" TO "+d.quote(n))
	}
	for _, c := range ch.AddColumns {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return nil, model.Errf(model.ErrInvalid, "new column needs a name")
		}
		if known[name] {
			return nil, model.Errf(model.ErrInvalid, "column %q already exists", name)
		}
		if !validType(c.Type) {
			return nil, model.Errf(model.ErrInvalid, "invalid column type %q", c.Type)
		}
		s := "ALTER TABLE " + table + " ADD COLUMN " + d.quote(name) + " " + strings.TrimSpace(c.Type)
		if !c.Nullable {
			s += " NOT NULL"
		}
		if c.Default != nil && strings.TrimSpace(*c.Default) != "" {
			if !validDefault(*c.Default) {
				return nil, model.Errf(model.ErrInvalid, "invalid default expression %q", *c.Default)
			}
			s += " DEFAULT " + strings.TrimSpace(*c.Default)
		}
		if c.After != "" && d.driver == model.DriverMySQL {
			if !known[c.After] {
				return nil, model.Errf(model.ErrInvalid, "unknown column %q", c.After)
			}
			s += " AFTER " + d.quote(c.After)
		}
		out = append(out, s)
	}
	for i := range out {
		out[i] += ";"
	}
	return out, nil
}

// ApplyStructure runs the DDL. Postgres and SQLite run it in one transaction;
// MySQL DDL auto-commits, so it runs statement by statement and stops at the first
// failure (earlier statements stay applied; the error says so).
func ApplyStructure(ctx context.Context, p *conn.Pool, stmts []string) (model.ApplyResult, error) {
	res := model.ApplyResult{FailedIndex: -1, Statements: stmts, InsertedPKs: []map[string]any{}}
	if p.ReadOnly() {
		return res, model.Errf(model.ErrReadOnly, "connection %q is read-only", p.Config.Name)
	}
	start := time.Now()
	finish := func(i int, err error) (model.ApplyResult, error) {
		res.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			res.FailedIndex, res.Executed, res.Error = i, i, err.Error()
			if errors.Is(err, context.Canceled) {
				res.Error = "canceled"
			}
			return res, nil
		}
		res.OK, res.Executed = true, len(stmts)
		return res, nil
	}
	if p.Config.Driver == model.DriverMySQL {
		for i, s := range stmts {
			if _, err := p.DB.ExecContext(ctx, strings.TrimSuffix(s, ";")); err != nil {
				if i > 0 {
					err = fmt.Errorf("%w (MySQL DDL is not transactional: the %d statement(s) before this one were applied)", err, i)
				}
				return finish(i, err)
			}
		}
		return finish(0, nil)
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return res, model.Wrap(model.ErrDB, err)
	}
	for i, s := range stmts {
		if _, err := tx.ExecContext(ctx, strings.TrimSuffix(s, ";")); err != nil {
			_ = tx.Rollback()
			return finish(i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return finish(len(stmts)-1, err)
	}
	return finish(0, nil)
}
