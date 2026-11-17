package conn

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"dbviewer/internal/model"
)

// Querier is satisfied by *sql.DB, *sql.Conn and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Read runs fn on the pool. On read-only connections, or when readOnlyTx is set,
// fn runs inside a read-only transaction (BEGIN READ ONLY / START TRANSACTION READ
// ONLY) that is always rolled back, so nothing fn runs can write or outlive the
// call (e.g. set_config()). SQLite has no read-only transactions: read-only
// connections open the file read-only; for a forced read on a writable file the
// connection is switched to PRAGMA query_only for the duration of fn.
func (p *Pool) Read(ctx context.Context, readOnlyTx bool, fn func(q Querier) error) error {
	if !readOnlyTx && !p.ReadOnly() {
		return fn(p.DB)
	}
	if p.Config.Driver == model.DriverSQLite && !p.ReadOnly() {
		c, err := p.DB.Conn(ctx)
		if err != nil {
			return err
		}
		ok := false
		defer func() {
			if !ok {
				_ = c.Raw(func(any) error { return driver.ErrBadConn })
			}
			c.Close()
		}()
		if _, err := c.ExecContext(ctx, "PRAGMA query_only = 1"); err != nil {
			return err
		}
		tx, err := c.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		ferr := fn(tx)
		_ = tx.Rollback()
		if _, err := c.ExecContext(context.Background(), "PRAGMA query_only = 0"); err == nil {
			ok = true
		}
		return ferr
	}
	tx, err := p.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: p.Config.Driver != model.DriverSQLite})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}
