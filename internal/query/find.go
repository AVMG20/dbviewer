package query

import (
	"context"
	"strconv"
	"strings"
	"time"

	"dbviewer/internal/conn"
	"dbviewer/internal/model"
)

// FindProgress is reported after each searched table.
type FindProgress = model.FindProgress

func searchable(k string) bool {
	switch k {
	case model.KindString, model.KindText, model.KindJSON, model.KindUUID, model.KindOther:
		return true
	}
	return false
}

// Find searches a term in the text columns of the given tables, at most
// LimitPerTable rows per table. Tables are searched one by one; the context
// cancels the search. Per-table errors are skipped.
func Find(ctx context.Context, p *conn.Pool, req model.FindRequest, tables []model.Table, cols map[string][]model.Column, progress func(FindProgress)) ([]model.FindResult, error) {
	term := req.Term
	if strings.TrimSpace(term) == "" {
		return nil, model.Errf(model.ErrInvalid, "search term is empty")
	}
	limit := req.LimitPerTable
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	mode := req.Mode
	if mode == "" {
		mode = "contains"
	}
	d := p.Driver
	schema := p.Schema(req.Schema)
	want := map[string]bool{}
	for _, t := range req.Tables {
		want[t] = true
	}
	var list []model.Table
	for _, t := range tables {
		if len(want) == 0 || want[t.Name] {
			list = append(list, t)
		}
	}
	out := []model.FindResult{}
	for i, t := range list {
		if ctx.Err() != nil {
			return out, model.Errf(model.ErrCanceled, "search canceled")
		}
		tcols := cols[t.Name]
		var conds []string
		var args []any
		var matched []model.Column
		for _, c := range tcols {
			if !searchable(c.Kind) {
				continue
			}
			matched = append(matched, c)
			cond, arg := findCond(p.Config.Driver, d.QuoteIdent(c.Name), d.Placeholder(len(args)+1), mode, term, req.CaseSensitive)
			conds = append(conds, cond)
			args = append(args, arg)
		}
		if len(conds) > 0 {
			sel := make([]string, len(tcols))
			for j, c := range tcols {
				sel[j] = SelectExpr(p, c)
			}
			q := "SELECT " + strings.Join(sel, ", ") + " FROM " + d.QualifiedName(schema, t.Name) +
				" WHERE " + strings.Join(conds, " OR ") + " LIMIT " + strconv.Itoa(limit+1)
			start := time.Now()
			kinds := make([]string, len(tcols))
			for j, c := range tcols {
				kinds[j] = EncodeKind(c)
			}
			var data [][]any
			var more bool
			err := p.Read(ctx, false, func(qr conn.Querier) error {
				rows, err := qr.QueryContext(ctx, q, args...)
				if err != nil {
					return err
				}
				defer rows.Close()
				data, more, err = scanRows(rows, kinds, limit)
				return err
			})
			{
				if err == nil && len(data) > 0 {
					res := model.QueryResult{
						Statement: q, Columns: ResultColumns(tcols), Rows: data, HasResultSet: true, RowsAffected: -1,
						Total: -1, EstimatedTotal: -1, HasMore: more, DurationMs: time.Since(start).Milliseconds(),
						Source: &model.TableRef{ConnID: p.Config.ID, Schema: schema, Table: t.Name},
					}
					if !more {
						res.Total = int64(len(data))
					}
					out = append(out, model.FindResult{Schema: schema, Table: t.Name, MatchedColumns: matchedColumns(data, tcols, term, mode, req.CaseSensitive), Result: res})
				}
			}
		}
		if progress != nil {
			progress(FindProgress{SearchID: req.SearchID, Table: t.Name, Done: i + 1, Total: len(list), Hits: len(out)})
		}
	}
	return out, nil
}

func findCond(driver, col, ph, mode, term string, cs bool) (string, any) {
	text := col
	if driver == model.DriverPostgres {
		text = "CAST(" + col + " AS TEXT)"
	}
	esc := ` ESCAPE '\'`
	if driver == model.DriverMySQL {
		esc = ` ESCAPE '\\'`
	}
	if mode == "exact" {
		switch {
		case driver == model.DriverMySQL && cs:
			return "BINARY " + text + " = " + ph, term
		case cs:
			return text + " = " + ph, term
		case driver == model.DriverPostgres:
			return "LOWER(" + text + ") = LOWER(" + ph + ")", term
		default:
			return "LOWER(" + text + ") = LOWER(" + ph + ")", term
		}
	}
	pat := likeEscape(term)
	if mode == "starts_with" {
		pat += "%"
	} else {
		pat = "%" + pat + "%"
	}
	switch driver {
	case model.DriverPostgres:
		if cs {
			return text + " LIKE " + ph + esc, pat
		}
		return text + " ILIKE " + ph + esc, pat
	case model.DriverMySQL:
		if cs {
			return text + " LIKE BINARY " + ph + esc, pat
		}
		return text + " LIKE " + ph + esc, pat
	default: // sqlite: LIKE is case-insensitive for ASCII
		if cs {
			if mode == "starts_with" {
				return "instr(" + text + ", " + ph + ") = 1", term
			}
			return "instr(" + text + ", " + ph + ") > 0", term
		}
		return text + " LIKE " + ph + esc, pat
	}
}

func matchedColumns(rows [][]any, cols []model.Column, term, mode string, cs bool) []string {
	needle := term
	if !cs {
		needle = strings.ToLower(term)
	}
	hit := map[int]bool{}
	for _, r := range rows {
		for i, v := range r {
			var s string
			switch x := v.(type) {
			case string:
				s = x
			case LongText:
				s = x.Text
			default:
				continue
			}
			if !cs {
				s = strings.ToLower(s)
			}
			ok := false
			switch mode {
			case "exact":
				ok = s == needle
			case "starts_with":
				ok = strings.HasPrefix(s, needle)
			default:
				ok = strings.Contains(s, needle)
			}
			if ok {
				hit[i] = true
			}
		}
	}
	out := []string{}
	for i, c := range cols {
		if hit[i] {
			out = append(out, c.Name)
		}
	}
	return out
}
