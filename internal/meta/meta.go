// Package meta caches schema metadata (tables + columns) per connection and
// builds the search index used by the sidebar speed search and autocomplete.
package meta

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"dbviewer/internal/conn"
	"dbviewer/internal/model"
)

type entry struct {
	tables  []model.Table
	columns map[string][]model.Column
	builtAt time.Time
}

type loadCall struct {
	done chan struct{}
	e    *entry
	err  error
}

type Cache struct {
	mu       sync.Mutex
	entries  map[string]*entry    // key: connID + "\x00" + schema
	inflight map[string]*loadCall // single-flight loads per key
	missing  map[string]time.Time // key + "\x00" + table -> when a lookup missed
}

// missTTL: an unknown table does not trigger another full reload within this time.
const missTTL = 10 * time.Second

func NewCache() *Cache {
	return &Cache{entries: map[string]*entry{}, inflight: map[string]*loadCall{}, missing: map[string]time.Time{}}
}

func key(connID, schema string) string { return connID + "\x00" + schema }

// Invalidate drops cached metadata of a connection (schema "" = all schemas).
func (c *Cache) Invalidate(connID, schema string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if strings.HasPrefix(k, connID+"\x00") && (schema == "" || k == key(connID, schema)) {
			delete(c.entries, k)
		}
	}
	for k := range c.missing {
		if strings.HasPrefix(k, connID+"\x00") {
			delete(c.missing, k)
		}
	}
}

func (c *Cache) load(ctx context.Context, p *conn.Pool, schema string, refresh bool) (*entry, error) {
	schema = p.Schema(schema)
	k := key(p.Config.ID, schema)
	c.mu.Lock()
	e := c.entries[k]
	if e != nil && !refresh {
		c.mu.Unlock()
		return e, nil
	}
	// single flight: concurrent callers (sidebar, grid, search) share one load
	if call := c.inflight[k]; call != nil {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.e, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &loadCall{done: make(chan struct{})}
	c.inflight[k] = call
	c.mu.Unlock()

	// the load must not depend on the first caller's cancellation
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	call.e, call.err = c.fetch(lctx, p, schema)
	c.mu.Lock()
	if call.err == nil {
		c.entries[k] = call.e
	}
	delete(c.inflight, k)
	c.mu.Unlock()
	close(call.done)
	return call.e, call.err
}

func (c *Cache) fetch(ctx context.Context, p *conn.Pool, schema string) (*entry, error) {
	tables, err := p.Driver.Tables(ctx, p.DB, schema)
	if err != nil {
		return nil, model.Wrap(model.ErrDB, err)
	}
	cols, err := p.Driver.Columns(ctx, p.DB, schema)
	if err != nil {
		return nil, model.Wrap(model.ErrDB, err)
	}
	return &entry{tables: tables, columns: cols, builtAt: time.Now()}, nil
}

// TableKind returns the cached kind (table/view/...) of a table, "" if unknown.
func (c *Cache) TableKind(ctx context.Context, p *conn.Pool, schema, table string) string {
	tables, err := c.Tables(ctx, p, schema, false)
	if err != nil {
		return ""
	}
	for _, t := range tables {
		if t.Name == table {
			return t.Kind
		}
	}
	return ""
}

func (c *Cache) Tables(ctx context.Context, p *conn.Pool, schema string, refresh bool) ([]model.Table, error) {
	e, err := c.load(ctx, p, schema, refresh)
	if err != nil {
		return nil, err
	}
	return e.tables, nil
}

// Columns returns the columns of one table (not_found if unknown).
func (c *Cache) Columns(ctx context.Context, p *conn.Pool, schema, table string, refresh bool) ([]model.Column, error) {
	e, err := c.load(ctx, p, schema, refresh)
	if err != nil {
		return nil, err
	}
	cols, ok := e.columns[table]
	if !ok && !refresh {
		// the table may have been created after caching; reload, but not again
		// for the same name within missTTL
		mk := key(p.Config.ID, p.Schema(schema)) + "\x00" + table
		c.mu.Lock()
		at, seen := c.missing[mk]
		c.mu.Unlock()
		if !seen || time.Since(at) > missTTL {
			if e, err = c.load(ctx, p, schema, true); err != nil {
				return nil, err
			}
			cols, ok = e.columns[table]
			if !ok {
				c.mu.Lock()
				c.missing[mk] = time.Now()
				c.mu.Unlock()
			}
		}
	}
	if !ok {
		return nil, model.Errf(model.ErrNotFound, "table %s.%s not found", p.Schema(schema), table)
	}
	return cols, nil
}

// Index builds the compact search index.
func (c *Cache) Index(ctx context.Context, p *conn.Pool, schema string, refresh bool) (model.SchemaIndex, error) {
	e, err := c.load(ctx, p, schema, refresh)
	if err != nil {
		return model.SchemaIndex{}, err
	}
	idx := model.SchemaIndex{ConnID: p.Config.ID, Schema: p.Schema(schema), BuiltAt: e.builtAt.UTC().Format(time.RFC3339),
		Tables: make([]model.SchemaIndexTable, 0, len(e.tables))}
	for _, t := range e.tables {
		it := model.SchemaIndexTable{Name: t.Name, Kind: t.Kind, Columns: []string{}, Types: []string{}}
		for _, col := range e.columns[t.Name] {
			it.Columns = append(it.Columns, col.Name)
			it.Types = append(it.Types, col.Type)
		}
		idx.Tables = append(idx.Tables, it)
	}
	sort.Slice(idx.Tables, func(i, j int) bool { return idx.Tables[i].Name < idx.Tables[j].Name })
	return idx, nil
}

// Cached returns the cached index entries of a connection without loading anything.
func (c *Cache) Cached(connID string) map[string][]model.Table {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string][]model.Table{}
	for k, e := range c.entries {
		if strings.HasPrefix(k, connID+"\x00") {
			out[strings.TrimPrefix(k, connID+"\x00")] = e.tables
		}
	}
	return out
}
