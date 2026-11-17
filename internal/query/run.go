package query

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"dbviewer/internal/conn"
	"dbviewer/internal/model"
	"dbviewer/internal/sqltok"
)

const (
	DefaultMaxRows     = 1000
	MaxConsoleRows     = 50000
	sessionIdleTimeout = 30 * time.Minute
)

// session is a DB connection pinned to a console tab, so USE/SET/BEGIN carry
// over between runs. All fields are guarded by mu.
type session struct {
	mu       sync.Mutex
	id       string
	connID   string
	db       *sql.DB // pool the conn belongs to (a reconnect replaces the pool)
	conn     *sql.Conn
	mysqlID  int64
	lastUsed time.Time
}

type running struct {
	cancel    context.CancelFunc
	connID    string
	pool      *conn.Pool
	mysqlID   int64
	requested bool
}

// Runner executes console queries, keeps console sessions and tracks running
// queries for cancellation.
type Runner struct {
	mu       sync.Mutex
	running  map[string]*running
	sessions map[string]*session
}

func NewRunner() *Runner {
	r := &Runner{running: map[string]*running{}, sessions: map[string]*session{}}
	go r.janitor()
	return r
}

func (r *Runner) janitor() {
	for range time.Tick(time.Minute) {
		r.mu.Lock()
		var idle []*session
		for id, s := range r.sessions {
			if s.mu.TryLock() {
				if time.Since(s.lastUsed) > sessionIdleTimeout {
					idle = append(idle, s)
					delete(r.sessions, id)
				}
				s.mu.Unlock()
			}
		}
		r.mu.Unlock()
		for _, s := range idle {
			s.mu.Lock()
			discard(s.conn)
			s.conn = nil
			s.mu.Unlock()
		}
	}
}

// discard closes a conn without returning it to the pool (it may hold an open
// transaction or changed session settings).
func discard(c *sql.Conn) {
	if c == nil {
		return
	}
	_ = c.Raw(func(any) error { return driver.ErrBadConn })
	_ = c.Close()
}

// Track registers a cancellable context under id (used by FindInDatabase).
func (r *Runner) Track(ctx context.Context, id string) (context.Context, func()) {
	return r.TrackConn(ctx, id, "")
}

// TrackConn is Track for work on a connection (cancelled when it closes).
func (r *Runner) TrackConn(ctx context.Context, id, connID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	if id == "" {
		id = "anon-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	q := &running{cancel: cancel, connID: connID}
	r.mu.Lock()
	r.running[id] = q
	r.mu.Unlock()
	return ctx, func() {
		cancel()
		r.mu.Lock()
		if r.running[id] == q {
			delete(r.running, id)
		}
		r.mu.Unlock()
	}
}

func (r *Runner) setRunningConn(id string, p *conn.Pool, mysqlID int64) {
	r.mu.Lock()
	if q := r.running[id]; q != nil {
		q.pool, q.mysqlID = p, mysqlID
	}
	r.mu.Unlock()
}

func (r *Runner) cancelRequested(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.running[id]
	return q != nil && q.requested
}

// Cancel cancels a running query; false if it is not running. MySQL queries are
// stopped with KILL QUERY (of our own connection) so the console session
// survives; others via context (Postgres sends a cancel request).
func (r *Runner) Cancel(id string) bool {
	r.mu.Lock()
	q := r.running[id]
	var pool *conn.Pool
	var mysqlID int64
	var cancel context.CancelFunc
	if q != nil {
		q.requested = true
		pool, mysqlID, cancel = q.pool, q.mysqlID, q.cancel
	}
	r.mu.Unlock()
	if q == nil {
		return false
	}
	if pool != nil && mysqlID > 0 && killQuery(pool, mysqlID) == nil {
		return true
	}
	cancel()
	return true
}

// killQuery stops the statement running on our own MySQL connection id.
func killQuery(p *conn.Pool, mysqlID int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.DB.ExecContext(ctx, "KILL QUERY "+strconv.FormatInt(mysqlID, 10))
	return err
}

// CloseSession discards a console session (an open transaction is rolled back).
func (r *Runner) CloseSession(id string) {
	r.mu.Lock()
	s := r.sessions[id]
	delete(r.sessions, id)
	r.mu.Unlock()
	if s != nil {
		s.mu.Lock()
		discard(s.conn)
		s.conn = nil
		s.mu.Unlock()
	}
}

// CloseConnection cancels the running queries of a connection and discards its
// console sessions (disconnect, reconfigure, database switch). It does not wait
// for a long query to finish on its own.
func (r *Runner) CloseConnection(connID string) {
	r.mu.Lock()
	var cancels []context.CancelFunc
	for _, q := range r.running {
		if q.connID == connID {
			q.requested = true
			cancels = append(cancels, q.cancel)
		}
	}
	var ids []string
	for id, s := range r.sessions {
		if s.connID == connID {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	for _, id := range ids {
		r.CloseSession(id)
	}
}

// acquire returns a locked session. sessionID "" gives a throw-away session.
// schema is applied once, when the session's connection is created.
func (r *Runner) acquire(ctx context.Context, p *conn.Pool, sessionID, schema string) (*session, func(), error) {
	var s *session
	if sessionID != "" {
		r.mu.Lock()
		s = r.sessions[sessionID]
		if s == nil || s.connID != p.Config.ID {
			if old := s; old != nil {
				go func() {
					old.mu.Lock()
					discard(old.conn)
					old.conn = nil
					old.mu.Unlock()
				}()
			}
			s = &session{id: sessionID, connID: p.Config.ID, lastUsed: time.Now()}
			r.sessions[sessionID] = s
		}
		r.mu.Unlock()
	} else {
		s = &session{connID: p.Config.ID, lastUsed: time.Now()}
	}
	s.mu.Lock()
	s.lastUsed = time.Now()
	if s.conn != nil && s.db != p.DB {
		discard(s.conn) // pool was replaced (reconnect / database switch)
		s.conn = nil
	}
	if s.conn == nil {
		cctx, ccancel := context.WithTimeout(ctx, 15*time.Second)
		c, err := p.DB.Conn(cctx)
		ccancel()
		if err != nil {
			s.mu.Unlock()
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return nil, nil, model.Errf(model.ErrDB, "all connections of this connection are busy; close some console tabs or wait for running queries")
			}
			return nil, nil, model.Wrap(model.ErrDB, err)
		}
		s.conn, s.db, s.mysqlID = c, p.DB, 0
		if p.Config.Driver == model.DriverMySQL {
			_ = c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&s.mysqlID)
		}
		if schema != "" {
			if use := p.Driver.UseSchema(schema); use != "" {
				if _, err := c.ExecContext(ctx, use); err != nil {
					discard(c)
					s.conn = nil
					s.mu.Unlock()
					return nil, nil, model.Wrap(model.ErrDB, err)
				}
			}
		}
	}
	release := func() {
		s.lastUsed = time.Now()
		if sessionID == "" {
			discard(s.conn)
			s.conn = nil
		}
		s.mu.Unlock()
	}
	return s, release, nil
}

// HistoryFunc records one executed statement.
type HistoryFunc func(model.QueryHistory)

func beginReadOnly(driver string) string {
	switch driver {
	case model.DriverPostgres:
		return "BEGIN READ ONLY"
	case model.DriverMySQL:
		return "START TRANSACTION READ ONLY"
	}
	return "BEGIN"
}

// Run executes every statement in req.SQL on the request's session, stopping at
// the first error. It returns the results gathered so far; the failing statement
// has Error set. A canceled statement gets Error "canceled: ...".
//
// On read-only connections each statement must classify as a read (sqltok) and
// then runs inside BEGIN READ ONLY ... ROLLBACK, so even a misclassified
// statement cannot write or change the session.
func (r *Runner) Run(ctx context.Context, p *conn.Pool, req model.QueryRequest, hist HistoryFunc) ([]model.QueryResult, error) {
	driverName := p.Config.Driver
	stmts := splitClassified(driverName, req.SQL)
	if len(stmts) == 0 {
		return []model.QueryResult{}, model.Errf(model.ErrInvalid, "no SQL to run")
	}
	maxRows := req.MaxRows
	if maxRows <= 0 {
		maxRows = DefaultMaxRows
	}
	if maxRows > MaxConsoleRows {
		maxRows = MaxConsoleRows
	}
	unterminated := sqltok.Tokenize(driverName, req.SQL).Unterminated
	ctx, done := r.TrackConn(ctx, req.QueryID, p.Config.ID)
	defer done()
	qid := req.QueryID

	s, release, err := r.acquire(ctx, p, req.SessionID, req.Schema)
	if err != nil {
		return nil, err
	}
	defer release()
	if qid != "" {
		r.setRunningConn(qid, p, s.mysqlID)
	}

	out := make([]model.QueryResult, 0, len(stmts))
	for i, st := range stmts {
		var res model.QueryResult
		if p.ReadOnly() {
			why := st.class.ReadOnly
			if why == "" && unterminated && i == len(stmts)-1 {
				why = "unterminated string, identifier or comment"
			}
			if why != "" {
				res = emptyResult(st.sql)
				res.Error = model.ErrReadOnly + ": connection is read-only: " + why
			} else {
				res = r.runReadOnly(ctx, p, s, st, maxRows)
			}
		} else {
			res = runOne(ctx, p, s, st, maxRows)
		}
		if s.conn != nil && res.Error != "" && (isBadConn(res.Error) || (ctx.Err() != nil && driverName == model.DriverMySQL)) {
			discard(s.conn) // broken session (the driver closed it): start fresh next time
			s.conn = nil
		}
		if qid != "" && r.cancelRequested(qid) && (res.Error != "" || st.class.ReturnsRows) {
			// A failed or row-returning statement hit by the cancel is reported as
			// canceled (MySQL KILL QUERY makes SLEEP() return 1 instead of failing).
			// A write that completed before the cancel took effect keeps its result.
			res.Error = model.ErrCanceled + ": query canceled"
			res.Rows, res.HasResultSet = [][]any{}, false
		}
		if res.Error == "" && s.conn != nil && driverName == model.DriverPostgres &&
			(st.class.First == "discard" || st.class.First == "deallocate") {
			// cached prepared statements are gone: the driver's cache is stale
			discard(s.conn)
			s.conn = nil
		}
		res.QueryID, res.Index = qid, i
		if hist != nil {
			n := res.RowsAffected
			if res.HasResultSet {
				n = int64(len(res.Rows))
			}
			hist(model.QueryHistory{ConnID: p.Config.ID, SQL: st.sql, DurationMs: res.DurationMs, RowCount: n, Error: res.Error})
		}
		out = append(out, res)
		if res.Error != "" || s.conn == nil {
			break
		}
		if qid != "" && r.cancelRequested(qid) {
			break // do not start the next statement after a cancel
		}
	}
	return out, nil
}

func emptyResult(stmt string) model.QueryResult {
	return model.QueryResult{Statement: stmt, Rows: [][]any{}, Columns: []model.ResultColumn{}, RowsAffected: -1, Total: -1, EstimatedTotal: -1}
}

// runReadOnly runs one statement inside a read-only transaction that is always
// rolled back.
func (r *Runner) runReadOnly(ctx context.Context, p *conn.Pool, s *session, st statement, maxRows int) model.QueryResult {
	if _, err := s.conn.ExecContext(ctx, beginReadOnly(p.Config.Driver)); err != nil {
		res := emptyResult(st.sql)
		res.Error = err.Error()
		return res
	}
	res := runOne(ctx, p, s, st, maxRows)
	if s.conn != nil {
		if _, err := s.conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			discard(s.conn)
			s.conn = nil
		}
	}
	return res
}

func isBadConn(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "bad connection") || strings.Contains(m, "connection is already closed") ||
		strings.Contains(m, "invalid connection") || strings.Contains(m, "conn closed") || strings.Contains(m, "conn busy")
}

func runOne(ctx context.Context, p *conn.Pool, s *session, st statement, maxRows int) model.QueryResult {
	start := time.Now()
	res := emptyResult(st.sql)
	fail := func(err error) model.QueryResult {
		res.DurationMs = time.Since(start).Milliseconds()
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			res.Error = model.ErrCanceled + ": query canceled"
		} else {
			res.Error = err.Error()
		}
		return res
	}
	c := s.conn
	if !st.class.ReturnsRows {
		r, err := c.ExecContext(ctx, st.sql)
		if err != nil {
			return fail(err)
		}
		if n, err := r.RowsAffected(); err == nil {
			res.RowsAffected = n
		}
		res.DurationMs = time.Since(start).Milliseconds()
		return res
	}
	stmtCtx, stmtCancel := context.WithCancel(ctx)
	defer stmtCancel()
	rows, err := c.QueryContext(stmtCtx, st.sql)
	if err != nil {
		return fail(err)
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		rows.Close()
		return fail(err)
	}
	if len(types) == 0 {
		// statement produced no result set after all (e.g. Postgres ANALYZE)
		rows.Close()
		if err := rows.Err(); err != nil {
			return fail(err)
		}
		res.DurationMs = time.Since(start).Milliseconds()
		return res
	}
	res.HasResultSet = true
	kinds := make([]string, len(types))
	for i, t := range types {
		k := p.Driver.Kind(t.DatabaseTypeName())
		kinds[i] = k
		if t.DatabaseTypeName() == "FLOAT4" {
			kinds[i] = kindFloat4
		}
		nullable, _ := t.Nullable()
		res.Columns = append(res.Columns, model.ResultColumn{Name: t.Name(), Type: strings.ToLower(t.DatabaseTypeName()), Kind: k, Nullable: nullable})
	}
	res.Rows, res.HasMore, err = scanRows(rows, kinds, maxRows)
	if err != nil {
		rows.Close()
		return fail(err)
	}
	if res.HasMore {
		// Stop the statement before closing, otherwise the driver reads (and
		// discards) the rest of a possibly huge result.
		if p.Config.Driver == model.DriverMySQL && s.mysqlID > 0 {
			_ = killQuery(p, s.mysqlID)
		} else if p.Config.Driver == model.DriverPostgres {
			stmtCancel()
		}
	}
	rows.Close()
	res.Truncated = res.HasMore
	if !res.HasMore {
		if err := rows.Err(); err != nil {
			return fail(err)
		}
		res.Total = int64(len(res.Rows))
	}
	res.DurationMs = time.Since(start).Milliseconds()
	return res
}
