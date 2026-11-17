package services

import (
	"context"

	"dbviewer/internal/model"
	"dbviewer/internal/query"
)

// QueryService: SQL console execution, cancel, history.
type QueryService struct{ c *Core }

func NewQueryService(c *Core) *QueryService { return &QueryService{c: c} }

// RunQuery runs one or more ';'-separated statements and returns one result per
// executed statement. Execution stops at the first failing statement, whose
// result has `error` set (the call itself still resolves). Each statement is
// recorded in history. Runs with the same sessionId share one DB session
// (USE/SET/BEGIN carry over; runs of one session are serialized). Cancel with
// CancelQuery(queryId) or by cancelling the returned promise. On read-only
// connections only read statements are accepted (others fail with
// "read_only: ..."), and the DB session itself is read-only.
func (s *QueryService) RunQuery(ctx context.Context, req model.QueryRequest) ([]model.QueryResult, error) {
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return nil, err
	}
	res, err := s.c.Runner.Run(ctx, p, req, func(h model.QueryHistory) { _ = s.c.Store.AddHistory(h) })
	return ret(res, err)
}

// CancelQuery cancels a running RunQuery/FindInDatabase by its client id.
// Returns false when nothing with that id is running.
func (s *QueryService) CancelQuery(queryID string) bool {
	return s.c.Runner.Cancel(queryID)
}

// CloseSession releases the DB session kept for a console tab (see
// QueryRequest.sessionId). An open transaction in it is rolled back.
func (s *QueryService) CloseSession(sessionID string) error {
	s.c.Runner.CloseSession(sessionID)
	return nil
}

// SplitStatements splits a script into statements (for "run statement at cursor").
// driver: postgres | mysql | sqlite. Offsets are JS string indexes.
func (s *QueryService) SplitStatements(driver, sql string) []model.StatementRange {
	r := query.Split(driver, sql)
	if r == nil {
		return []model.StatementRange{}
	}
	return r
}

// History returns executed statements, newest first. connID "" = all connections;
// search filters on SQL text; limit default 200.
func (s *QueryService) History(connID, search string, limit int) ([]model.QueryHistory, error) {
	h, err := s.c.Store.History(connID, search, limit)
	return ret(h, err)
}

// ClearHistory deletes history of a connection ("" = all).
func (s *QueryService) ClearHistory(connID string) error {
	return wrap(s.c.Store.ClearHistory(connID))
}
