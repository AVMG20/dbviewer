package services

import (
	"context"
	"strings"

	"dbviewer/internal/changes"
	"dbviewer/internal/meta"
	"dbviewer/internal/model"
)

// SchemaService: metadata (databases, schemas, tables, columns, structure) and
// the search index.
type SchemaService struct{ c *Core }

func NewSchemaService(c *Core) *SchemaService { return &SchemaService{c: c} }

// Databases lists databases on the server (sqlite: attached databases).
func (s *SchemaService) Databases(ctx context.Context, connID string) ([]string, error) {
	p, err := s.c.pool(ctx, connID)
	if err != nil {
		return nil, err
	}
	d, err := p.Driver.Databases(ctx, p.DB)
	return ret(d, model.Wrap(model.ErrDB, err))
}

// Schemas lists schemas of the current database (mysql: databases; sqlite: main + attached).
func (s *SchemaService) Schemas(ctx context.Context, connID string) ([]model.SchemaInfo, error) {
	p, err := s.c.pool(ctx, connID)
	if err != nil {
		return nil, err
	}
	d, err := p.Driver.Schemas(ctx, p.DB)
	return ret(d, model.Wrap(model.ErrDB, err))
}

// Tables lists tables and views of a schema ("" = default), sorted by name.
// Cached; refresh=true reloads tables and columns from the database.
func (s *SchemaService) Tables(ctx context.Context, connID, schema string, refresh bool) ([]model.Table, error) {
	p, err := s.c.pool(ctx, connID)
	if err != nil {
		return nil, err
	}
	t, err := s.c.Meta.Tables(ctx, p, schema, refresh)
	return ret(t, err)
}

// Columns returns the columns of one table/view (cached, from the schema bulk load).
func (s *SchemaService) Columns(ctx context.Context, connID, schema, table string) ([]model.Column, error) {
	p, err := s.c.pool(ctx, connID)
	if err != nil {
		return nil, err
	}
	c, err := s.c.Meta.Columns(ctx, p, schema, table, false)
	return ret(c, err)
}

// Indexes returns the indexes of a table (primary first).
func (s *SchemaService) Indexes(ctx context.Context, connID, schema, table string) ([]model.Index, error) {
	p, err := s.c.pool(ctx, connID)
	if err != nil {
		return nil, err
	}
	i, err := p.Driver.Indexes(ctx, p.DB, p.Schema(schema), table)
	return ret(i, model.Wrap(model.ErrDB, err))
}

// ForeignKeys returns outgoing foreign keys of a table.
func (s *SchemaService) ForeignKeys(ctx context.Context, connID, schema, table string) ([]model.ForeignKey, error) {
	p, err := s.c.pool(ctx, connID)
	if err != nil {
		return nil, err
	}
	f, err := p.Driver.ForeignKeys(ctx, p.DB, p.Schema(schema), table)
	return ret(f, model.Wrap(model.ErrDB, err))
}

// ReferencedBy returns incoming foreign keys (other tables referencing this one).
func (s *SchemaService) ReferencedBy(ctx context.Context, connID, schema, table string) ([]model.ForeignKey, error) {
	p, err := s.c.pool(ctx, connID)
	if err != nil {
		return nil, err
	}
	f, err := p.Driver.ReferencedBy(ctx, p.DB, p.Schema(schema), table)
	return ret(f, model.Wrap(model.ErrDB, err))
}

// TableStructure returns everything the Structure tab shows in one call.
// Indexes/FKs/triggers that cannot be read (permissions) come back empty; a DDL
// that cannot be built comes back as an SQL comment with the reason.
func (s *SchemaService) TableStructure(ctx context.Context, connID, schema, table string) (model.TableStructure, error) {
	p, err := s.c.pool(ctx, connID)
	if err != nil {
		return model.TableStructure{}, err
	}
	sc := p.Schema(schema)
	var out model.TableStructure
	tables, err := s.c.Meta.Tables(ctx, p, schema, false)
	if err != nil {
		return out, wrap(err)
	}
	found := false
	for _, t := range tables {
		if t.Name == table {
			out.Table, found = t, true
		}
	}
	if !found {
		if tables, err = s.c.Meta.Tables(ctx, p, schema, true); err == nil {
			for _, t := range tables {
				if t.Name == table {
					out.Table, found = t, true
				}
			}
		}
	}
	if !found {
		return out, model.Errf(model.ErrNotFound, "table %s.%s not found", sc, table)
	}
	if out.Columns, err = s.c.Meta.Columns(ctx, p, schema, table, false); err != nil {
		return out, wrap(err)
	}
	if out.Table.Kind == "table" || out.Table.Kind == "materialized_view" {
		out.Indexes, _ = p.Driver.Indexes(ctx, p.DB, sc, table)
	}
	if out.Table.Kind == "table" {
		out.ForeignKeys, _ = p.Driver.ForeignKeys(ctx, p.DB, sc, table)
		out.ReferencedBy, _ = p.Driver.ReferencedBy(ctx, p.DB, sc, table)
	}
	out.Triggers, _ = p.Driver.Triggers(ctx, p.DB, sc, table)
	if out.DDL, err = p.Driver.DDL(ctx, p.DB, sc, table); err != nil {
		out.DDL = "-- DDL not available: " + err.Error()
	}
	return ret(out, nil)
}

// DDL returns the CREATE statement(s) of a table or view (with indexes, comments
// and triggers where the engine keeps them separately).
func (s *SchemaService) DDL(ctx context.Context, connID, schema, table string) (string, error) {
	p, err := s.c.pool(ctx, connID)
	if err != nil {
		return "", err
	}
	d, err := p.Driver.DDL(ctx, p.DB, p.Schema(schema), table)
	return ret(d, model.Wrap(model.ErrDB, err))
}

// SearchSchema returns every table/view with its column names for one schema
// ("" = default). The frontend fuzzy-matches on it (sidebar speed search, Cmd+K,
// SQL autocomplete). Cached; refresh=true reloads.
func (s *SchemaService) SearchSchema(ctx context.Context, connID, schema string, refresh bool) (model.SchemaIndex, error) {
	p, err := s.c.pool(ctx, connID)
	if err != nil {
		return model.SchemaIndex{}, err
	}
	idx, err := s.c.Meta.Index(ctx, p, schema, refresh)
	return ret(idx, err)
}

// SearchObjects fuzzy-searches tables/views, columns and saved queries across the
// project's connections that are currently connected (their default schema plus
// any schema already loaded). Not-connected connections are skipped (no implicit
// connects from the palette). limit default 50.
func (s *SchemaService) SearchObjects(ctx context.Context, projectID, query string, limit int) ([]model.SearchHit, error) {
	conns, err := s.c.Store.Connections(projectID)
	if err != nil {
		return nil, wrap(err)
	}
	statuses, _ := s.c.Conns.Statuses()
	connected := map[string]bool{}
	for _, st := range statuses {
		connected[st.ConnID] = st.Connected
	}
	var inputs []meta.SearchInput
	for _, c := range conns {
		if !connected[c.ID] {
			continue
		}
		p, err := s.c.Conns.Get(ctx, c.ID)
		if err != nil {
			continue
		}
		schemas := map[string]bool{p.DefaultSchema: true}
		for sc := range s.c.Meta.Cached(c.ID) {
			schemas[sc] = true
		}
		for sc := range schemas {
			if idx, err := s.c.Meta.Index(ctx, p, sc, false); err == nil {
				inputs = append(inputs, meta.SearchInput{ConnID: c.ID, Index: idx})
			}
		}
	}
	saved, _ := s.c.Store.SavedQueries(projectID)
	return ret(meta.Search(query, inputs, saved, limit), nil)
}

func (s *SchemaService) structureStatements(ctx context.Context, change model.StructureChange) ([]string, error) {
	p, err := s.c.pool(ctx, change.ConnID)
	if err != nil {
		return nil, err
	}
	cols, err := s.c.Meta.Columns(ctx, p, change.Schema, change.Table, true)
	if err != nil {
		return nil, wrap(err)
	}
	var idx []model.Index
	if len(change.DropIndexes) > 0 {
		if idx, err = p.Driver.Indexes(ctx, p.DB, p.Schema(change.Schema), change.Table); err != nil {
			return nil, model.Wrap(model.ErrDB, err)
		}
	}
	return changes.StructureStatements(p, change, cols, idx)
}

// PreviewStructureChanges returns the DDL statements for a staged structure change
// (order: drop indexes, drop columns, rename columns, add columns). The preview is
// exactly what ApplyStructureChanges runs.
func (s *SchemaService) PreviewStructureChanges(ctx context.Context, change model.StructureChange) ([]string, error) {
	st, err := s.structureStatements(ctx, change)
	return ret(st, err)
}

// ApplyStructureChanges runs the DDL (read-only connections refused). Postgres and
// SQLite: one transaction. MySQL: DDL auto-commits, statements run one by one and
// stop at the first failure (see ApplyResult.error). Metadata caches are refreshed.
func (s *SchemaService) ApplyStructureChanges(ctx context.Context, change model.StructureChange) (model.ApplyResult, error) {
	p, err := s.c.pool(ctx, change.ConnID)
	if err != nil {
		return model.ApplyResult{FailedIndex: -1}, err
	}
	if p.ReadOnly() {
		return model.ApplyResult{FailedIndex: -1}, model.Errf(model.ErrReadOnly, "connection %q is read-only", p.Config.Name)
	}
	stmts, err := s.structureStatements(ctx, change)
	if err != nil {
		return model.ApplyResult{FailedIndex: -1}, wrap(err)
	}
	if len(change.ExpectedStatements) > 0 && strings.Join(change.ExpectedStatements, "\n") != strings.Join(stmts, "\n") {
		return model.ApplyResult{FailedIndex: -1, Statements: stmts}, model.Errf(model.ErrInvalid,
			"the table changed since the preview; review the new statements and confirm again")
	}
	res, err := changes.ApplyStructure(ctx, p, stmts)
	s.c.Meta.Invalidate(change.ConnID, p.Schema(change.Schema))
	return ret(res, err)
}
