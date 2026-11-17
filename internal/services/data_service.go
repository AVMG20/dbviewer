package services

import (
	"context"
	"path/filepath"
	"strings"

	"dbviewer/internal/changes"
	"dbviewer/internal/export"
	"dbviewer/internal/model"
	"dbviewer/internal/query"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// DataService: table pages, staged changes, copy-as-SQL, export, find in database.
type DataService struct{ c *Core }

func NewDataService(c *Core) *DataService { return &DataService{c: c} }

// FetchPage returns one page of a table. Without orderBy the rows are ordered by
// the primary key. total is -1 unless countTotal is set or the last page was reached.
// result.source is always set (editing target); columns carry pk/fk info.
func (s *DataService) FetchPage(ctx context.Context, req model.PageRequest) (model.QueryResult, error) {
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return model.QueryResult{}, err
	}
	if p.ReadOnly() {
		if err := query.ReadOnlyCheckFragment(p.Config.Driver, req.Where); err != nil {
			return model.QueryResult{}, err
		}
		if err := query.ReadOnlyCheckFragment(p.Config.Driver, req.OrderBy); err != nil {
			return model.QueryResult{}, err
		}
	}
	cols, err := s.c.Meta.Columns(ctx, p, req.Schema, req.Table, false)
	if err != nil {
		return model.QueryResult{}, wrap(err)
	}
	r, err := query.FetchPage(ctx, p, req, cols, s.c.Meta.TableKind(ctx, p, req.Schema, req.Table))
	r.EstimatedTotal = -1
	if err == nil && strings.TrimSpace(req.Where) == "" && len(req.ColumnFilters) == 0 {
		if r.Total >= 0 {
			r.EstimatedTotal = r.Total
		} else if tables, terr := s.c.Meta.Tables(ctx, p, req.Schema, false); terr == nil {
			for _, t := range tables {
				if t.Name == req.Table {
					r.EstimatedTotal = t.EstRows
				}
			}
		}
	}
	return ret(r, err)
}

// CountRows counts rows matching where + columnFilters (limit/offset ignored).
// Exact; for a quick estimate use Table.estRows.
func (s *DataService) CountRows(ctx context.Context, req model.PageRequest) (int64, error) {
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return 0, err
	}
	if p.ReadOnly() {
		if err := query.ReadOnlyCheckFragment(p.Config.Driver, req.Where); err != nil {
			return 0, err
		}
	}
	cols, err := s.c.Meta.Columns(ctx, p, req.Schema, req.Table, false)
	if err != nil {
		return 0, wrap(err)
	}
	n, err := query.CountRows(ctx, p, req, cols)
	return ret(n, err)
}

// CellValue loads the full (untruncated) value of one cell, identified by PK.
// Same encoding as rows, but never truncated.
func (s *DataService) CellValue(ctx context.Context, req model.CellRequest) (any, error) {
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return nil, err
	}
	cols, err := s.c.Meta.Columns(ctx, p, req.Schema, req.Table, false)
	if err != nil {
		return nil, wrap(err)
	}
	v, err := changes.CellValue(ctx, p, req, cols)
	return v, wrap(err)
}

// PreviewChanges returns the SQL that ApplyChanges would run (literals inlined),
// in order: deletes, updates, inserts.
func (s *DataService) PreviewChanges(ctx context.Context, cs model.ChangeSet) ([]string, error) {
	p, err := s.c.pool(ctx, cs.ConnID)
	if err != nil {
		return nil, err
	}
	cols, err := s.c.Meta.Columns(ctx, p, cs.Schema, cs.Table, false)
	if err != nil {
		return nil, wrap(err)
	}
	out, err := changes.Preview(ctx, p, cs, cols)
	return ret(out, err)
}

// ApplyChanges runs the change set in one transaction with bound parameters.
// Errors (Go): read_only, no_primary_key, not_connected, invalid_argument.
// Database failures, and UPDATE/DELETE statements that do not hit exactly one row,
// are returned as ApplyResult{ok:false, failedIndex, error} after a full rollback.
func (s *DataService) ApplyChanges(ctx context.Context, cs model.ChangeSet) (model.ApplyResult, error) {
	p, err := s.c.pool(ctx, cs.ConnID)
	if err != nil {
		return model.ApplyResult{FailedIndex: -1}, err
	}
	if p.ReadOnly() {
		return model.ApplyResult{FailedIndex: -1}, model.Errf(model.ErrReadOnly, "connection %q is read-only", p.Config.Name)
	}
	cols, err := s.c.Meta.Columns(ctx, p, cs.Schema, cs.Table, false)
	if err != nil {
		return model.ApplyResult{FailedIndex: -1}, wrap(err)
	}
	r, err := changes.Apply(ctx, p, cs, cols)
	return ret(r, err)
}

// CopyAsSQL renders rows as INSERT or UPDATE statements in the connection's dialect.
// table may be any name (console results); when it is a known table its column
// types drive the literal formatting and its PK the UPDATE WHERE clause.
func (s *DataService) CopyAsSQL(ctx context.Context, req model.CopySQLRequest) (string, error) {
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return "", err
	}
	var cols []model.Column
	if tables, err := s.c.Meta.Tables(ctx, p, req.Schema, false); err == nil {
		for _, t := range tables {
			if t.Name == req.Table {
				cols, _ = s.c.Meta.Columns(ctx, p, req.Schema, req.Table, false)
			}
		}
	}
	out, err := changes.RowsAsSQL(p, req, cols)
	return ret(out, err)
}

// Export streams a table (page source: where/orderBy/columnFilters apply, limit
// and offset are ignored) or a single SELECT to a file. Rows are streamed, never
// loaded into memory. path "" shows a native save dialog (desktop only; browser
// mode rejects with "unsupported"); a cancelled dialog => {cancelled: true}.
// Cancel a running export by cancelling the promise.
func (s *DataService) Export(ctx context.Context, req model.ExportRequest) (model.ExportResult, error) {
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return model.ExportResult{}, err
	}
	if req.Format == "" {
		req.Format = "csv"
	}
	var cols []model.Column
	if req.Page != nil {
		if req.Page.ConnID == "" {
			req.Page.ConnID = req.ConnID
		}
		if cols, err = s.c.Meta.Columns(ctx, p, req.Page.Schema, req.Page.Table, false); err != nil {
			return model.ExportResult{}, wrap(err)
		}
	}
	if req.Path == "" {
		name := "export"
		if req.Page != nil {
			name = req.Page.Table
		} else if req.SQLTable != "" {
			name = req.SQLTable
		}
		path, err := saveFileDialog("Export", name+"."+export.Extension(req.Format))
		if err != nil {
			return model.ExportResult{}, wrap(err)
		}
		if path == "" {
			return model.ExportResult{Cancelled: true}, nil
		}
		req.Path = path
	}
	if !filepath.IsAbs(req.Path) {
		return model.ExportResult{}, model.Errf(model.ErrInvalid, "export path must be absolute")
	}
	kind := ""
	if req.Page != nil {
		kind = s.c.Meta.TableKind(ctx, p, req.Page.Schema, req.Page.Table)
	}
	r, err := export.Export(ctx, p, req, cols, kind)
	return ret(r, err)
}

func saveFileDialog(title, filename string) (string, error) {
	if serverMode {
		return "", model.Errf(model.ErrUnsupported, "native file dialogs are not available in browser mode; enter the path instead")
	}
	app := application.Get()
	if app == nil {
		return "", model.Errf(model.ErrInternal, "no application")
	}
	p, err := app.Dialog.SaveFile().SetMessage(title).SetFilename(filename).CanCreateDirectories(true).PromptForSingleSelection()
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "cancel") {
		return "", nil
	}
	return p, err
}

// FindInDatabase searches a value across the text columns of the selected tables
// (all tables and views of the schema when req.tables is empty), at most
// limitPerTable rows per table (default 50, max 1000). Emits "find:progress"
// after each table. Cancel with QueryService.CancelQuery(searchId) or by
// cancelling the promise; the matches found so far are discarded.
func (s *DataService) FindInDatabase(ctx context.Context, req model.FindRequest) ([]model.FindResult, error) {
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return nil, err
	}
	tables, err := s.c.Meta.Tables(ctx, p, req.Schema, false)
	if err != nil {
		return nil, wrap(err)
	}
	cols := map[string][]model.Column{}
	for _, t := range tables {
		if c, err := s.c.Meta.Columns(ctx, p, req.Schema, t.Name, false); err == nil {
			cols[t.Name] = c
		}
	}
	ctx, done := s.c.Runner.TrackConn(ctx, req.SearchID, req.ConnID)
	defer done()
	out, err := query.Find(ctx, p, req, tables, cols, func(pr model.FindProgress) { emit(EventFindProgress, pr) })
	return ret(out, err)
}
