// Package export streams table/query data to CSV, TSV, JSON, SQL or Markdown files.
package export

import (
	"bufio"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"dbviewer/internal/changes"
	"dbviewer/internal/conn"
	"dbviewer/internal/model"
	"dbviewer/internal/query"
	"dbviewer/internal/sqltok"
)

// Formats supported by Export.
var Formats = []string{"csv", "tsv", "json", "sql", "markdown"}

// Extension returns the file extension for a format.
func Extension(format string) string {
	switch format {
	case "markdown":
		return "md"
	}
	return format
}

// Export writes the request's source to req.Path (already resolved; never empty
// here). Rows are streamed from the database to the file; nothing is buffered
// beyond the current row. The file is written to "<path>.part" and renamed on
// success. cols are the table metadata for page sources (nil for SQL).
func Export(ctx context.Context, p *conn.Pool, req model.ExportRequest, cols []model.Column, tableKind string) (model.ExportResult, error) {
	start := time.Now()
	res := model.ExportResult{Path: req.Path}
	if !validFormat(req.Format) {
		return res, model.Errf(model.ErrInvalid, "unknown format %q (csv, tsv, json, sql, markdown)", req.Format)
	}
	var q string
	var args []any
	tableName := req.SQLTable
	switch {
	case req.Page != nil:
		if p.ReadOnly() {
			if err := query.ReadOnlyCheckFragment(p.Config.Driver, req.Page.Where); err != nil {
				return res, err
			}
			if err := query.ReadOnlyCheckFragment(p.Config.Driver, req.Page.OrderBy); err != nil {
				return res, err
			}
		}
		var err error
		if q, args, _, err = query.BuildSelect(p, *req.Page, cols, 0, 0, tableKind); err != nil {
			return res, err
		}
		if tableName == "" {
			tableName = req.Page.Table
		}
	case strings.TrimSpace(req.SQL) != "":
		// Export is a read by nature, on every connection: the statement must
		// classify as a read and runs in a read-only transaction.
		stmts := sqltok.Split(p.Config.Driver, req.SQL)
		if len(stmts) != 1 {
			return res, model.Errf(model.ErrInvalid, "export needs exactly one SELECT statement")
		}
		cl := sqltok.Classify(p.Config.Driver, stmts[0].Tokens)
		if cl.ReadOnly != "" || !cl.ReturnsRows || sqltok.Tokenize(p.Config.Driver, req.SQL).Unterminated {
			why := cl.ReadOnly
			if why == "" {
				why = "the statement does not return rows"
			}
			return res, model.Errf(model.ErrInvalid, "export runs read statements only: %s", why)
		}
		q = req.SQL[stmts[0].Start:stmts[0].End]
		if tableName == "" {
			tableName = "query_result"
		}
	default:
		return res, model.Errf(model.ErrInvalid, "export needs a page or sql source")
	}

	var n, written int64
	read := func(fn func(conn.Querier) error) error { return p.Read(ctx, true, fn) }
	if req.Page == nil && req.Schema != "" {
		read = func(fn func(conn.Querier) error) error { return readInSchema(ctx, p, req.Schema, fn) }
	}
	err := read(func(qr conn.Querier) error {
		rows, err := qr.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var rcols []model.ResultColumn
		if req.Page != nil {
			rcols = query.ResultColumns(cols)
		} else {
			types, err := rows.ColumnTypes()
			if err != nil {
				return err
			}
			for _, t := range types {
				rcols = append(rcols, model.ResultColumn{Name: t.Name(), Type: strings.ToLower(t.DatabaseTypeName()), Kind: p.Driver.Kind(t.DatabaseTypeName())})
			}
		}
		// column subset / order
		sel := make([]int, 0, len(rcols))
		if len(req.Columns) > 0 {
			pos := map[string]int{}
			for i, c := range rcols {
				pos[c.Name] = i
			}
			for _, name := range req.Columns {
				i, ok := pos[name]
				if !ok {
					return model.Errf(model.ErrInvalid, "unknown column %q", name)
				}
				sel = append(sel, i)
			}
		} else {
			for i := range rcols {
				sel = append(sel, i)
			}
		}
		kinds := make([]string, len(rcols))
		for i, c := range rcols {
			kinds[i] = c.Kind
			if req.Page != nil && i < len(cols) {
				kinds[i] = query.EncodeKind(cols[i])
			} else if strings.EqualFold(c.Type, "float4") {
				kinds[i] = query.EncodeKind(model.Column{DataType: "float4"})
			}
		}
		part := req.Path + ".part"
		f, err := os.Create(part)
		if err != nil {
			return model.Wrap(model.ErrInvalid, err)
		}
		cw := &countWriter{w: f}
		bw := bufio.NewWriterSize(cw, 256*1024)
		w := newWriter(req.Format, bw, p.Config.Driver, p.Driver.QuoteIdent(tableName), rcols, sel, req.IncludeHeader)
		n, err = stream(ctx, rows, rcols, kinds, w)
		if err == nil {
			err = bw.Flush()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(part, req.Path)
		}
		if err != nil {
			os.Remove(part)
			return err
		}
		written = cw.n
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return res, model.Errf(model.ErrCanceled, "export canceled")
		}
		return res, model.Wrap(model.ErrDB, err)
	}
	res.Rows, res.Bytes = n, written
	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}

// readInSchema runs fn like p.Read(ctx, true, fn), with schema as the default
// schema for unqualified names (SQL sources, the console tab's schema). Postgres:
// SET LOCAL search_path inside the (always rolled back) read-only transaction.
// MySQL: USE on a dedicated connection that is discarded afterwards, so the
// pool's connections keep their database. SQLite has a single schema namespace.
func readInSchema(ctx context.Context, p *conn.Pool, schema string, fn func(conn.Querier) error) error {
	switch p.Config.Driver {
	case model.DriverPostgres:
		return p.Read(ctx, true, func(qr conn.Querier) error {
			ex, ok := qr.(interface {
				ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
			})
			if !ok {
				return fmt.Errorf("cannot set the schema")
			}
			if _, err := ex.ExecContext(ctx, "SET LOCAL search_path TO "+p.Driver.QuoteIdent(schema)); err != nil {
				return err
			}
			return fn(qr)
		})
	case model.DriverMySQL:
		c, err := p.DB.Conn(ctx)
		if err != nil {
			return err
		}
		defer func() {
			_ = c.Raw(func(any) error { return driver.ErrBadConn }) // never reuse: USE changed it
			_ = c.Close()
		}()
		if _, err := c.ExecContext(ctx, p.Driver.UseSchema(schema)); err != nil {
			return err
		}
		tx, err := c.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		return fn(tx)
	}
	return p.Read(ctx, true, fn)
}

func validFormat(f string) bool {
	for _, x := range Formats {
		if x == f {
			return true
		}
	}
	return false
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

func stream(ctx context.Context, rows *sql.Rows, cols []model.ResultColumn, kinds []string, w rowWriter) (int64, error) {
	if err := w.begin(); err != nil {
		return 0, err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	enc := make([]any, len(cols))
	var n int64
	for rows.Next() {
		if n%1000 == 0 && ctx.Err() != nil {
			return n, ctx.Err()
		}
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		for i, v := range vals {
			enc[i] = query.Encode(v, kinds[i], true)
		}
		if err := w.row(enc); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	return n, w.end()
}

type rowWriter interface {
	begin() error
	row(vals []any) error
	end() error
}

func newWriter(format string, w *bufio.Writer, driver, table string, cols []model.ResultColumn, sel []int, header bool) rowWriter {
	switch format {
	case "csv", "tsv":
		cw := csv.NewWriter(w)
		if format == "tsv" {
			cw.Comma = '\t'
		}
		return &csvWriter{w: cw, cols: cols, sel: sel, header: header}
	case "json":
		return &jsonWriter{w: w, cols: cols, sel: sel}
	case "sql":
		return &sqlWriter{w: w, cols: cols, sel: sel, driver: driver, table: table}
	default:
		return &mdWriter{w: w, cols: cols, sel: sel}
	}
}

// text renders a value as plain text (csv/tsv/markdown). NULL -> "" (csv) / "NULL" (md).
func text(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", true
	case string:
		return x, false
	case bool:
		if x {
			return "true", false
		}
		return "false", false
	case int64:
		return strconv.FormatInt(x, 10), false
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), false
	case query.Binary:
		return "0x" + x.Hex, false
	case query.LongText:
		return x.Text, false
	}
	return fmt.Sprint(v), false
}

type csvWriter struct {
	w      *csv.Writer
	cols   []model.ResultColumn
	sel    []int
	header bool
	rec    []string
}

func (c *csvWriter) begin() error {
	c.rec = make([]string, len(c.sel))
	if !c.header {
		return nil
	}
	for i, j := range c.sel {
		c.rec[i] = c.cols[j].Name
	}
	return c.w.Write(c.rec)
}

func (c *csvWriter) row(vals []any) error {
	for i, j := range c.sel {
		c.rec[i], _ = text(vals[j])
	}
	return c.w.Write(c.rec)
}

func (c *csvWriter) end() error {
	c.w.Flush()
	return c.w.Error()
}

type jsonWriter struct {
	w     *bufio.Writer
	cols  []model.ResultColumn
	sel   []int
	first bool
	keys  [][]byte
}

func (j *jsonWriter) begin() error {
	j.first = true
	for _, i := range j.sel {
		k, _ := json.Marshal(j.cols[i].Name)
		j.keys = append(j.keys, k)
	}
	_, err := j.w.WriteString("[")
	return err
}

func (j *jsonWriter) row(vals []any) error {
	if !j.first {
		j.w.WriteString(",")
	}
	j.first = false
	j.w.WriteString("\n  {")
	for n, i := range j.sel {
		if n > 0 {
			j.w.WriteString(", ")
		}
		j.w.Write(j.keys[n])
		j.w.WriteString(": ")
		if err := j.value(vals[i], j.cols[i].Kind); err != nil {
			return err
		}
	}
	_, err := j.w.WriteString("}")
	return err
}

func (j *jsonWriter) value(v any, kind string) error {
	var b []byte
	var err error
	switch x := v.(type) {
	case string:
		switch {
		case kind == model.KindJSON && json.Valid([]byte(x)):
			b = []byte(x) // embed JSON documents as JSON
		case (kind == model.KindDecimal || kind == model.KindInt) && json.Valid([]byte(x)) && isNumber(x):
			b = []byte(x) // exact decimal / big int as a JSON number
		default:
			b, err = json.Marshal(x)
		}
	case query.Binary:
		b, err = json.Marshal("0x" + x.Hex)
	case query.LongText:
		b, err = json.Marshal(x.Text)
	default:
		b, err = json.Marshal(x)
	}
	if err != nil {
		return err
	}
	_, err = j.w.Write(b)
	return err
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil && !strings.ContainsAny(s, "xXnN")
}

func (j *jsonWriter) end() error {
	if j.first {
		_, err := j.w.WriteString("]\n")
		return err
	}
	_, err := j.w.WriteString("\n]\n")
	return err
}

type sqlWriter struct {
	w      *bufio.Writer
	cols   []model.ResultColumn
	sel    []int
	driver string
	table  string
	head   string
}

func (s *sqlWriter) begin() error {
	names := make([]string, len(s.sel))
	for i, j := range s.sel {
		names[i] = quoteFor(s.driver, s.cols[j].Name)
	}
	s.head = "INSERT INTO " + s.table + " (" + strings.Join(names, ", ") + ") VALUES ("
	return nil
}

func quoteFor(driver, n string) string {
	if driver == model.DriverMySQL {
		return "`" + strings.ReplaceAll(n, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(n, `"`, `""`) + `"`
}

func (s *sqlWriter) row(vals []any) error {
	s.w.WriteString(s.head)
	for n, i := range s.sel {
		if n > 0 {
			s.w.WriteString(", ")
		}
		v := vals[i]
		switch x := v.(type) {
		case query.Binary:
			v = map[string]any{"$type": "binary", "hex": x.Hex}
		case query.LongText:
			v = x.Text
		}
		c := model.Column{Name: s.cols[i].Name, Kind: s.cols[i].Kind, Type: s.cols[i].Type}
		s.w.WriteString(changes.Literal(s.driver, v, c))
	}
	_, err := s.w.WriteString(");\n")
	return err
}

func (s *sqlWriter) end() error { return nil }

type mdWriter struct {
	w    *bufio.Writer
	cols []model.ResultColumn
	sel  []int
}

func mdEscape(s string) string {
	return strings.NewReplacer("|", `\|`, "\r\n", "<br>", "\n", "<br>", "\r", "<br>").Replace(s)
}

func (m *mdWriter) begin() error {
	var h, sep []string
	for _, i := range m.sel {
		h = append(h, mdEscape(m.cols[i].Name))
		switch m.cols[i].Kind {
		case model.KindInt, model.KindFloat, model.KindDecimal:
			sep = append(sep, "---:")
		default:
			sep = append(sep, "---")
		}
	}
	_, err := m.w.WriteString("| " + strings.Join(h, " | ") + " |\n| " + strings.Join(sep, " | ") + " |\n")
	return err
}

func (m *mdWriter) row(vals []any) error {
	cells := make([]string, len(m.sel))
	for n, i := range m.sel {
		t, null := text(vals[i])
		if null {
			t = "NULL"
		}
		cells[n] = mdEscape(t)
	}
	_, err := m.w.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	return err
}

func (m *mdWriter) end() error { return nil }
