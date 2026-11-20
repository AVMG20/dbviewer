package services

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"dbviewer/internal/ai"
	"dbviewer/internal/conn"
	"dbviewer/internal/model"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// EventAIChunk carries a model.AIChunk while AIService.GenerateSQL streams.
const EventAIChunk = "ai:chunk"

func init() {
	application.RegisterEvent[model.AIChunk](EventAIChunk)
}

// AIService writes SQL with a local model through Ollama.
type AIService struct {
	c *Core

	mu   sync.Mutex
	caps map[string][]string // baseURL + model -> capabilities ("completion", "thinking", "insert", …)
}

func NewAIService(c *Core) *AIService { return &AIService{c: c, caps: map[string][]string{}} }

// Models lists the models installed in Ollama at baseURL ("" = localhost).
// Rejects with not_connected when Ollama is not running.
func (s *AIService) Models(ctx context.Context, baseURL string) ([]model.AIModel, error) {
	cl, err := ai.NewClient(baseURL)
	if err != nil {
		return nil, err
	}
	list, err := cl.Models(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return ret(list, nil)
}

// Warm loads the model (the AI prompt calls it on open, so the model is ready by
// the time the user has typed). It stays loaded for settings.KeepAlive, at least 2m.
// Rejects like GenerateSQL when Ollama is not running or the model is missing.
func (s *AIService) Warm(ctx context.Context, settings model.AISettings) error {
	if settings.Model == "" {
		return nil
	}
	cl, err := ai.NewClient(settings.BaseURL)
	if err != nil {
		return err
	}
	keep := "2m"
	if d, err := time.ParseDuration(settings.KeepAlive); err == nil && d > 2*time.Minute {
		keep = settings.KeepAlive
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return wrap(cl.SetKeepAlive(ctx, settings.Model, keep))
}

// Unload frees the model's memory right away.
func (s *AIService) Unload(ctx context.Context, settings model.AISettings) error {
	if settings.Model == "" {
		return nil
	}
	cl, err := ai.NewClient(settings.BaseURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wrap(cl.SetKeepAlive(ctx, settings.Model, "0"))
}

// hasCap reports whether the model has a capability ("thinking", "insert"; cached
// per server and model). Unknown (Ollama unreachable) = false, not cached.
func (s *AIService) hasCap(ctx context.Context, cl *ai.Client, name, capability string) bool {
	key := cl.BaseURL + "\x00" + name
	s.mu.Lock()
	caps, ok := s.caps[key]
	s.mu.Unlock()
	if !ok {
		var err error
		caps, err = cl.Capabilities(ctx, name)
		if err != nil {
			return false
		}
		s.mu.Lock()
		s.caps[key] = caps
		s.mu.Unlock()
	}
	for _, c := range caps {
		if c == capability {
			return true
		}
	}
	return false
}

// supportsThink reports whether the model accepts the "think" flag (cached).
func (s *AIService) supportsThink(ctx context.Context, cl *ai.Client, name string) bool {
	return s.hasCap(ctx, cl, name, "thinking")
}

// GenerateSQL writes (req.SQL == "") or changes (req.SQL set) one SQL statement
// for the request, using the schema of the connection. Emits ai:chunk events with
// the SQL so far while the model writes. Cancel by cancelling the promise.
func (s *AIService) GenerateSQL(ctx context.Context, req model.AISQLRequest) (model.AISQLResult, error) {
	return ret(s.generateSQL(ctx, req))
}

func (s *AIService) generateSQL(ctx context.Context, req model.AISQLRequest) (model.AISQLResult, error) {
	start := time.Now()
	if strings.TrimSpace(req.Prompt) == "" {
		return model.AISQLResult{}, model.Errf(model.ErrInvalid, "describe the query you want")
	}
	if err := checkModel(req.AISettings); err != nil {
		return model.AISQLResult{}, err
	}
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return model.AISQLResult{}, err
	}
	infos, err := s.tableInfos(ctx, p, req.Schema)
	if err != nil {
		return model.AISQLResult{}, err
	}
	editorCtx := req.Context
	if len(editorCtx) > ai.ContextChars {
		editorCtx = editorCtx[:ai.ContextChars]
	}
	picked := ai.PickTables(req.Prompt, req.SQL, editorCtx, infos)
	schemaText, names := ai.SchemaText(picked, req.Prompt)
	msgs := ai.Messages(p.Driver.Name(), req.Prompt, req.SQL, schemaText, len(infos), len(names), time.Now())
	sql, stats, err := s.chatSQL(ctx, req.AISettings, req.RequestID, msgs, req.Retry)
	if err != nil {
		return model.AISQLResult{}, err
	}
	return model.AISQLResult{
		SQL:         sql,
		Tables:      names,
		TotalTables: len(infos),
		Model:       req.Model,
		DurationMs:  time.Since(start).Milliseconds(),
		LoadMs:      stats.LoadDuration.Milliseconds(),
	}, nil
}

func checkModel(set model.AISettings) error {
	if set.Model == "" {
		return model.Errf(model.ErrInvalid, "no model selected: pick one in Settings (AI)")
	}
	return nil
}

// tableInfos returns every table of the schema with its columns.
func (s *AIService) tableInfos(ctx context.Context, p *conn.Pool, schema string) ([]ai.TableInfo, error) {
	tables, columns, err := s.c.Meta.Snapshot(ctx, p, schema)
	if err != nil {
		return nil, wrap(err)
	}
	infos := make([]ai.TableInfo, 0, len(tables))
	for _, t := range tables {
		infos = append(infos, ai.TableInfo{Name: t.Name, Kind: t.Kind, Comment: t.Comment, Rows: t.EstRows, Columns: columns[t.Name]})
	}
	return infos, nil
}

// referenced returns the tables of infos that sql refers to, in schema order.
func referenced(sql string, infos []ai.TableInfo) []*ai.TableInfo {
	refs := ai.ReferencedTables(sql)
	var out []*ai.TableInfo
	for i := range infos {
		if refs[strings.ToLower(infos[i].Name)] {
			out = append(out, &infos[i])
		}
	}
	return out
}

// chat runs one chat completion with the settings' model; onContent gets the reply so far.
func (s *AIService) chat(ctx context.Context, set model.AISettings, msgs []ai.Message, retry bool, onContent func(content string, thinking bool)) (string, ai.ChatStats, error) {
	if err := checkModel(set); err != nil {
		return "", ai.ChatStats{}, err
	}
	cl, err := ai.NewClient(set.BaseURL)
	if err != nil {
		return "", ai.ChatStats{}, err
	}
	temp := 0.1
	if retry {
		temp = 0.8
	}
	chat := ai.ChatRequest{Model: set.Model, Messages: msgs, KeepAlive: set.KeepAlive, Options: ai.Options(temp)}
	if s.supportsThink(ctx, cl, set.Model) {
		off := false
		chat.Think = &off
	}
	reply, stats, err := cl.Chat(ctx, chat, func(content, thinking string) {
		if onContent != nil {
			onContent(content, content == "" && thinking != "")
		}
	})
	return reply, stats, wrap(err)
}

// chatSQL runs a chat whose reply is SQL in a code block; streams it as ai:chunk.
func (s *AIService) chatSQL(ctx context.Context, set model.AISettings, requestID string, msgs []ai.Message, retry bool) (string, ai.ChatStats, error) {
	var last string
	reply, stats, err := s.chat(ctx, set, msgs, retry, func(content string, thinking bool) {
		sql, inThink := ai.ExtractSQL(content)
		inThink = inThink || thinking
		if sql == last && !inThink {
			return
		}
		last = sql
		emit(EventAIChunk, model.AIChunk{RequestID: requestID, SQL: sql, Thinking: inThink})
	})
	if err != nil {
		return "", stats, err
	}
	sql, _ := ai.ExtractSQL(reply)
	if sql == "" {
		return "", stats, model.Errf(model.ErrInternal, "the model did not return a query: %s", truncate(strings.TrimSpace(reply), 300))
	}
	return sql, stats, nil
}

// chatText runs a chat whose reply is prose; streams it as ai:chunk (text).
func (s *AIService) chatText(ctx context.Context, set model.AISettings, requestID string, msgs []ai.Message, retry bool) (string, ai.ChatStats, error) {
	reply, stats, err := s.chat(ctx, set, msgs, retry, func(content string, thinking bool) {
		if requestID == "" {
			return
		}
		text := ai.StripThinking(content)
		emit(EventAIChunk, model.AIChunk{RequestID: requestID, Text: text, Thinking: thinking || (text == "" && strings.Contains(content, "<think>"))})
	})
	if err != nil {
		return "", stats, err
	}
	text := ai.StripThinking(reply)
	if text == "" {
		return "", stats, model.Errf(model.ErrInternal, "the model returned an empty answer")
	}
	return text, stats, nil
}

// FixSQL corrects a statement that failed: req.SQL, req.Error (the database error),
// with the schema of the tables the statement references (and related ones).
// Streams ai:chunk events like GenerateSQL.
func (s *AIService) FixSQL(ctx context.Context, req model.AITaskRequest) (model.AISQLResult, error) {
	return ret(s.fixSQL(ctx, req))
}

func (s *AIService) fixSQL(ctx context.Context, req model.AITaskRequest) (model.AISQLResult, error) {
	start := time.Now()
	if strings.TrimSpace(req.SQL) == "" {
		return model.AISQLResult{}, model.Errf(model.ErrInvalid, "no statement to fix")
	}
	if err := checkModel(req.AISettings); err != nil {
		return model.AISQLResult{}, err
	}
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return model.AISQLResult{}, err
	}
	infos, err := s.tableInfos(ctx, p, req.Schema)
	if err != nil {
		return model.AISQLResult{}, err
	}
	// the error message names the bad column / table: its words find the right tables
	picked := ai.PickTables(req.Error, req.SQL, "", infos)
	schemaText, names := ai.SchemaText(picked, req.SQL+" "+req.Error)
	msgs := ai.FixMessages(p.Driver.Name(), req.SQL, req.Error, schemaText, time.Now())
	sql, stats, err := s.chatSQL(ctx, req.AISettings, req.RequestID, msgs, req.Retry)
	if err != nil {
		return model.AISQLResult{}, err
	}
	return model.AISQLResult{SQL: sql, Tables: names, TotalTables: len(infos), Model: req.Model, DurationMs: time.Since(start).Milliseconds(), LoadMs: stats.LoadDuration.Milliseconds()}, nil
}

// ConvertSQL rewrites req.SQL (written for the connection's database) for the
// req.Target dialect ("mysql", "postgres", "sqlite"). Streams ai:chunk events.
func (s *AIService) ConvertSQL(ctx context.Context, req model.AITaskRequest) (model.AISQLResult, error) {
	return ret(s.convertSQL(ctx, req))
}

func (s *AIService) convertSQL(ctx context.Context, req model.AITaskRequest) (model.AISQLResult, error) {
	start := time.Now()
	if strings.TrimSpace(req.SQL) == "" {
		return model.AISQLResult{}, model.Errf(model.ErrInvalid, "no SQL to convert")
	}
	switch req.Target {
	case model.DriverMySQL, model.DriverPostgres, model.DriverSQLite:
	default:
		return model.AISQLResult{}, model.Errf(model.ErrInvalid, "unknown target dialect %q", req.Target)
	}
	from, err := s.driverOf(req.ConnID)
	if err != nil {
		return model.AISQLResult{}, err
	}
	msgs := ai.ConvertMessages(from, req.Target, req.SQL)
	sql, stats, err := s.chatSQL(ctx, req.AISettings, req.RequestID, msgs, req.Retry)
	if err != nil {
		return model.AISQLResult{}, err
	}
	return model.AISQLResult{SQL: sql, Model: req.Model, DurationMs: time.Since(start).Milliseconds(), LoadMs: stats.LoadDuration.Milliseconds()}, nil
}

// driverOf returns the driver name of a saved connection (without connecting).
func (s *AIService) driverOf(connID string) (string, error) {
	cfg, err := s.c.Store.Connection(connID)
	if err != nil {
		return "", wrap(err)
	}
	return cfg.Driver, nil
}

// ExplainSQL explains req.SQL in 3–5 plain sentences, with the schema of the
// tables it references. Streams ai:chunk events with text.
func (s *AIService) ExplainSQL(ctx context.Context, req model.AITaskRequest) (model.AITextResult, error) {
	return ret(s.explainSQL(ctx, req))
}

func (s *AIService) explainSQL(ctx context.Context, req model.AITaskRequest) (model.AITextResult, error) {
	start := time.Now()
	if strings.TrimSpace(req.SQL) == "" {
		return model.AITextResult{}, model.Errf(model.ErrInvalid, "no statement to explain")
	}
	if err := checkModel(req.AISettings); err != nil {
		return model.AITextResult{}, err
	}
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return model.AITextResult{}, err
	}
	infos, err := s.tableInfos(ctx, p, req.Schema)
	if err != nil {
		return model.AITextResult{}, err
	}
	schemaText, names := ai.SchemaText(referenced(req.SQL, infos), req.SQL)
	text, stats, err := s.chatText(ctx, req.AISettings, req.RequestID, ai.ExplainMessages(p.Driver.Name(), req.SQL, schemaText), req.Retry)
	if err != nil {
		return model.AITextResult{}, err
	}
	return model.AITextResult{Text: text, Tables: names, Model: req.Model, DurationMs: time.Since(start).Milliseconds(), LoadMs: stats.LoadDuration.Milliseconds()}, nil
}

// ExplainPlan reviews an EXPLAIN result (req.Plan, tab-separated with a header
// line) of req.SQL: problems and suggested CREATE INDEX statements, using the row
// estimates and existing indexes of the tables involved. Reads metadata only;
// nothing is run. Streams ai:chunk events with text.
func (s *AIService) ExplainPlan(ctx context.Context, req model.AITaskRequest) (model.AITextResult, error) {
	return ret(s.explainPlan(ctx, req))
}

func (s *AIService) explainPlan(ctx context.Context, req model.AITaskRequest) (model.AITextResult, error) {
	start := time.Now()
	if strings.TrimSpace(req.Plan) == "" {
		return model.AITextResult{}, model.Errf(model.ErrInvalid, "no EXPLAIN output")
	}
	if err := checkModel(req.AISettings); err != nil {
		return model.AITextResult{}, err
	}
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return model.AITextResult{}, err
	}
	infos, err := s.tableInfos(ctx, p, req.Schema)
	if err != nil {
		return model.AITextResult{}, err
	}
	var tables []ai.PlanTable
	var names []string
	used := ai.Words(req.SQL)
	for _, t := range referenced(req.SQL, infos) {
		if len(tables) >= 8 {
			break
		}
		pt := ai.PlanTable{Name: t.Name, Rows: t.Rows}
		ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
		pt.Indexes, _ = p.Driver.Indexes(ictx, p.DB, p.Schema(req.Schema), t.Name)
		cancel()
		// only the columns the query mentions: fewer distractions for a small model
		for _, c := range t.Columns {
			if used[strings.ToLower(c.Name)] {
				pt.Columns = append(pt.Columns, c.Name)
			}
		}
		tables = append(tables, pt)
		names = append(names, t.Name)
	}
	sql := strings.TrimSpace(req.SQL)
	msgs := ai.PlanMessages(p.Driver.Name(), sql, req.Plan, ai.PlanTablesText(tables))
	text, stats, err := s.chatText(ctx, req.AISettings, req.RequestID, msgs, req.Retry)
	if err != nil {
		return model.AITextResult{}, err
	}
	return model.AITextResult{Text: text, Tables: names, Model: req.Model, DurationMs: time.Since(start).Milliseconds(), LoadMs: stats.LoadDuration.Milliseconds()}, nil
}

// NameQuery suggests a short title (2–5 words) for req.SQL, e.g. "Paid orders per
// month" (saved query names, console tab titles). Not streamed.
func (s *AIService) NameQuery(ctx context.Context, req model.AITaskRequest) (model.AITextResult, error) {
	start := time.Now()
	if strings.TrimSpace(req.SQL) == "" {
		return ret(model.AITextResult{}, model.Errf(model.ErrInvalid, "no statement to name"))
	}
	reply, stats, err := s.chat(ctx, req.AISettings, ai.NameMessages(req.SQL), req.Retry, nil)
	if err != nil {
		return ret(model.AITextResult{}, err)
	}
	title := ai.ExtractTitle(reply)
	if title == "" {
		return ret(model.AITextResult{}, model.Errf(model.ErrInternal, "the model returned no name"))
	}
	return ret(model.AITextResult{Text: title, Model: req.Model, DurationMs: time.Since(start).Milliseconds(), LoadMs: stats.LoadDuration.Milliseconds()}, nil)
}

// FilterRows turns a plain-language request (req.Prompt) into a WHERE condition
// (and an ORDER BY when the request asks for a sort) for req.Table. Before asking
// the model it reads the distinct values (at most ai.MaxDistinct) of up to four
// short text columns the request likely refers to (status, state, type, …), in a
// read-only transaction with a short timeout, so the model compares with values
// that exist. Not streamed.
func (s *AIService) FilterRows(ctx context.Context, req model.AITaskRequest) (model.AIFilterResult, error) {
	return ret(s.filterRows(ctx, req))
}

func (s *AIService) filterRows(ctx context.Context, req model.AITaskRequest) (model.AIFilterResult, error) {
	start := time.Now()
	if strings.TrimSpace(req.Prompt) == "" {
		return model.AIFilterResult{}, model.Errf(model.ErrInvalid, "describe the rows you want")
	}
	if req.Table == "" {
		return model.AIFilterResult{}, model.Errf(model.ErrInvalid, "table is required")
	}
	if err := checkModel(req.AISettings); err != nil {
		return model.AIFilterResult{}, err
	}
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return model.AIFilterResult{}, err
	}
	cols, err := s.c.Meta.Columns(ctx, p, req.Schema, req.Table, false)
	if err != nil {
		return model.AIFilterResult{}, wrap(err)
	}
	info := ai.TableInfo{Name: req.Table, Columns: cols}
	candidates := ai.FilterCandidates(cols, req.Prompt, 4)
	values := s.distinctValues(ctx, p, req.Schema, req.Table, candidates)
	var valueCols []string
	for _, c := range candidates {
		if len(values[c.Name]) > 0 {
			valueCols = append(valueCols, c.Name)
		}
	}
	msgs := ai.FilterMessages(p.Driver.Name(), req.Table, ai.TableText(&info), ai.ValuesText(valueCols, values), req.Prompt, time.Now())
	reply, stats, err := s.chat(ctx, req.AISettings, msgs, req.Retry, nil)
	if err != nil {
		return model.AIFilterResult{}, err
	}
	where, orderBy, err := ai.ExtractFilter(p.Driver.Name(), reply)
	if err != nil {
		return model.AIFilterResult{}, model.Errf(model.ErrInternal, "%s", err.Error())
	}
	return model.AIFilterResult{
		Where: where, OrderBy: orderBy, Values: values, Model: req.Model,
		DurationMs: time.Since(start).Milliseconds(), LoadMs: stats.LoadDuration.Milliseconds(),
	}, nil
}

// distinctValues reads up to ai.MaxDistinct distinct non-NULL values of each
// column (columns with more, or with long values, are left out). Read-only
// transaction, 1.5 s per column and 3 s in total; failures are ignored.
func (s *AIService) distinctValues(ctx context.Context, p *conn.Pool, schema, table string, cols []model.Column) map[string][]string {
	out := map[string][]string{}
	if len(cols) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	qt := p.Driver.QualifiedName(p.Schema(schema), table)
	for _, c := range cols {
		qc := p.Driver.QuoteIdent(c.Name)
		hint := ""
		if p.Config.Driver == model.DriverMySQL {
			hint = "/*+ MAX_EXECUTION_TIME(1500) */ "
		}
		q := fmt.Sprintf("SELECT %sDISTINCT %s FROM %s WHERE %s IS NOT NULL LIMIT %d", hint, qc, qt, qc, ai.MaxDistinct+1)
		cctx, ccancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		var vals []string
		err := p.Read(cctx, true, func(db conn.Querier) error {
			if p.Config.Driver == model.DriverPostgres {
				if rows, err := db.QueryContext(cctx, "SET LOCAL statement_timeout = 1500"); err == nil {
					rows.Close()
				}
			}
			rows, err := db.QueryContext(cctx, q)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var v sql.NullString
				if err := rows.Scan(&v); err != nil {
					return err
				}
				vals = append(vals, v.String)
			}
			return rows.Err()
		})
		ccancel()
		// one value says nothing about the column (and tempts the model to filter on it)
		if err != nil || len(vals) < 2 || len(vals) > ai.MaxDistinct {
			continue
		}
		long := false
		for _, v := range vals {
			if len(v) > 60 {
				long = true
			}
		}
		if !long {
			sort.Strings(vals)
			out[c.Name] = vals
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
