package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbviewer/internal/model"
)

// TestAILive runs GenerateSQL against a real Ollama with the SQLite dummy database.
// Opt-in: DBVIEWER_OLLAMA_MODEL=qwen3:4b-instruct go test ./internal/services -run TestAILive -v
func TestAILive(t *testing.T) {
	mdl := os.Getenv("DBVIEWER_OLLAMA_MODEL")
	if mdl == "" {
		t.Skip("set DBVIEWER_OLLAMA_MODEL to run against a local Ollama")
	}
	c := testCore(t)
	ctx := context.Background()
	ps := NewProjectService(c)
	projects, _ := ps.Projects()
	saved, err := NewConnectionService(c).SaveConnection(model.ConnectionConfig{
		ProjectID: projects[0].ID, Name: "sqlite", Driver: model.DriverSQLite, ReadOnly: true,
		FilePath: filepath.Join(repoRoot(), "testdata", "dummy.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ai := NewAIService(c)
	settings := model.AISettings{Model: mdl, KeepAlive: "0"}

	if err := ai.Warm(ctx, settings); err != nil {
		t.Fatalf("warm: %v", err)
	}
	qs := NewQueryService(c)
	run := func(prompt, sql string) model.AISQLResult {
		t.Helper()
		res, err := ai.GenerateSQL(ctx, model.AISQLRequest{AISettings: model.AISettings{Model: mdl, KeepAlive: "2m"}, ConnID: saved.ID, Prompt: prompt, SQL: sql})
		if err != nil {
			t.Fatalf("%s: %v", prompt, err)
		}
		t.Logf("%q (%d ms, load %d ms, tables %v of %d):\n%s", prompt, res.DurationMs, res.LoadMs, res.Tables, res.TotalTables, res.SQL)
		out, err := qs.RunQuery(ctx, model.QueryRequest{ConnID: saved.ID, SQL: res.SQL, MaxRows: 5})
		if err != nil || len(out) == 0 || out[0].Error != "" {
			t.Errorf("generated SQL does not run: %v %+v", err, out)
		}
		return res
	}
	r := run("orders from last week with the customer email", "")
	if r.LoadMs > 300 {
		t.Errorf("model reloaded after Warm (load %d ms)", r.LoadMs)
	}
	if !strings.Contains(strings.ToLower(r.SQL), "sales_order") {
		t.Errorf("expected sales_order in %s", r.SQL)
	}
	r2 := run("also show the order total and sort by it, highest first", r.SQL)
	if !strings.Contains(strings.ToLower(r2.SQL), "order by") {
		t.Errorf("expected ORDER BY in %s", r2.SQL)
	}
	run("top 5 products by quantity ordered", "")

	// errors: missing model, Ollama not running
	_, err = ai.GenerateSQL(ctx, model.AISQLRequest{AISettings: model.AISettings{Model: "no-such-model:1b"}, ConnID: saved.ID, Prompt: "x"})
	if ae := (*model.AppError)(nil); err == nil || !asApp(err, &ae) || ae.Code != model.ErrNotFound {
		t.Errorf("missing model: %v", err)
	}
	_, err = ai.Models(ctx, "http://127.0.0.1:1")
	if ae := (*model.AppError)(nil); err == nil || !asApp(err, &ae) || ae.Code != model.ErrNotConnected {
		t.Errorf("not running: %v", err)
	}
	if err := ai.Unload(ctx, settings); err != nil {
		t.Errorf("unload: %v", err)
	}
}

func asApp(err error, ae **model.AppError) bool { return errors.As(err, ae) }

// TestAITasksLive runs the smaller AI tasks (fix, convert, explain, plan review, naming,
// table filters) against a real Ollama with the SQLite dummy database.
// Opt-in: DBVIEWER_OLLAMA_MODEL=qwen3:4b-instruct go test ./internal/services -run TestAITasksLive -v
func TestAITasksLive(t *testing.T) {
	mdl := os.Getenv("DBVIEWER_OLLAMA_MODEL")
	if mdl == "" {
		t.Skip("set DBVIEWER_OLLAMA_MODEL to run against a local Ollama")
	}
	c := testCore(t)
	ctx := context.Background()
	projects, _ := NewProjectService(c).Projects()
	saved, err := NewConnectionService(c).SaveConnection(model.ConnectionConfig{
		ProjectID: projects[0].ID, Name: "sqlite", Driver: model.DriverSQLite, ReadOnly: true,
		FilePath: filepath.Join(repoRoot(), "testdata", "dummy.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ai := NewAIService(c)
	qs := NewQueryService(c)
	set := model.AISettings{Model: mdl, KeepAlive: "2m"}
	if err := ai.Warm(ctx, set); err != nil {
		t.Fatalf("warm: %v", err)
	}
	runs := func(sql string) string {
		t.Helper()
		out, err := qs.RunQuery(ctx, model.QueryRequest{ConnID: saved.ID, SQL: sql, MaxRows: 5})
		if err != nil {
			return err.Error()
		}
		if len(out) == 0 {
			return "no result"
		}
		return out[0].Error
	}
	req := func(r model.AITaskRequest) model.AITaskRequest {
		r.AISettings = set
		r.ConnID = saved.ID
		return r
	}

	// 1. fix: misspelled column and a MySQL-only function
	for _, bad := range []string{
		"SELECT increment_id, grand_totl FROM sales_order WHERE status = 'complete' ORDER BY created_at DESC LIMIT 10",
		"SELECT DATE_FORMAT(created_at, '%Y-%m') AS month, COUNT(*) FROM sales_order GROUP BY month",
		"SELECT c.email, COUNT(*) FROM customer_entity c JOIN sales_order o ON o.customer_id = c.id GROUP BY c.email",
	} {
		dbErr := runs(bad)
		if dbErr == "" {
			t.Fatalf("expected %q to fail", bad)
		}
		res, err := ai.FixSQL(ctx, req(model.AITaskRequest{SQL: bad, Error: dbErr}))
		if err != nil {
			t.Fatalf("fix: %v", err)
		}
		t.Logf("FIX (%d ms, tables %v)\n  %s\n  error: %s\n  -> %s", res.DurationMs, res.Tables, bad, dbErr, res.SQL)
		if e := runs(res.SQL); e != "" {
			t.Errorf("fixed SQL still fails: %s", e)
		}
	}

	// 2. filter
	for _, prompt := range []string{
		"orders last week over 100 euro, not cancelled",
		"pending orders of guests, newest first",
		"orders from customers with a gmail address that have a coupon",
		"complete orders in 2024 with more than 3 items, biggest first",
	} {
		res, err := ai.FilterRows(ctx, req(model.AITaskRequest{Table: "sales_order", Prompt: prompt}))
		if err != nil {
			t.Errorf("filter %q: %v", prompt, err)
			continue
		}
		t.Logf("FILTER %q (%d ms)\n  WHERE %s\n  ORDER BY %s\n  values: %v", prompt, res.DurationMs, res.Where, res.OrderBy, res.Values)
		sql := "SELECT * FROM sales_order"
		if res.Where != "" {
			sql += " WHERE " + res.Where
		}
		if res.OrderBy != "" {
			sql += " ORDER BY " + res.OrderBy
		}
		if e := runs(sql); e != "" {
			t.Errorf("filter does not run: %s", e)
		}
	}
	if res, err := ai.FilterRows(ctx, req(model.AITaskRequest{Table: "sales_order", Prompt: "not cancelled"})); err != nil || !strings.Contains(res.Where, "'canceled'") {
		t.Errorf("expected the real value 'canceled': %+v %v", res, err)
	}

	// 3. explain
	q := "SELECT c.email, COUNT(o.entity_id) AS orders, SUM(o.grand_total) AS revenue FROM customer_entity c JOIN sales_order o ON o.customer_id = c.entity_id WHERE o.status = 'complete' AND o.created_at >= date('now', '-1 year') GROUP BY c.email HAVING COUNT(o.entity_id) > 2 ORDER BY revenue DESC LIMIT 20"
	ex, err := ai.ExplainSQL(ctx, req(model.AITaskRequest{SQL: q}))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	t.Logf("EXPLAIN (%d ms, tables %v):\n%s", ex.DurationMs, ex.Tables, ex.Text)

	// 4. plan review
	for _, slow := range []string{
		"SELECT * FROM sales_order WHERE customer_firstname = 'Anna' AND grand_total > 100 ORDER BY updated_at DESC",
		"SELECT c.email, COUNT(*) AS orders FROM customer_entity c JOIN sales_order o ON o.customer_id = c.entity_id GROUP BY c.email ORDER BY orders DESC LIMIT 20",
	} {
		reviewPlan(t, ctx, ai, qs, req, saved.ID, slow)
	}

	// 5. names + convert
	for _, s := range []string{q, "SELECT strftime('%Y-%m', created_at) AS month, SUM(grand_total) FROM sales_order WHERE status = 'complete' GROUP BY month", "DELETE FROM quote WHERE updated_at < date('now', '-30 days')"} {
		n, err := ai.NameQuery(ctx, req(model.AITaskRequest{SQL: s}))
		if err != nil {
			t.Fatalf("name: %v", err)
		}
		t.Logf("NAME (%d ms): %q", n.DurationMs, n.Text)
	}
	for _, target := range []string{model.DriverMySQL, model.DriverPostgres} {
		cv, err := ai.ConvertSQL(ctx, req(model.AITaskRequest{SQL: "SELECT strftime('%Y-%m', created_at) AS month, IFNULL(coupon_code, '-') AS coupon, SUM(grand_total) AS total\nFROM sales_order\nWHERE created_at >= date('now', '-90 days')\nGROUP BY 1, 2\nORDER BY total DESC\nLIMIT 10", Target: target}))
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		t.Logf("CONVERT to %s (%d ms):\n%s", target, cv.DurationMs, cv.SQL)
	}
}

func reviewPlan(t *testing.T, ctx context.Context, ai *AIService, qs *QueryService, req func(model.AITaskRequest) model.AITaskRequest, connID, slow string) {
	t.Helper()
	plan, err := qs.RunQuery(ctx, model.QueryRequest{ConnID: connID, SQL: "EXPLAIN QUERY PLAN " + slow, MaxRows: 100})
	if err != nil || plan[0].Error != "" {
		t.Fatalf("explain query plan: %v %+v", err, plan)
	}
	var pb strings.Builder
	for i, col := range plan[0].Columns {
		if i > 0 {
			pb.WriteString("\t")
		}
		pb.WriteString(col.Name)
	}
	for _, row := range plan[0].Rows {
		pb.WriteString("\n")
		for i, v := range row {
			if i > 0 {
				pb.WriteString("\t")
			}
			pb.WriteString(strings.TrimSpace(strings.ReplaceAll(fmt.Sprint(v), "\n", " ")))
		}
	}
	pr, err := ai.ExplainPlan(ctx, req(model.AITaskRequest{SQL: slow, Plan: pb.String()}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	t.Logf("PLAN (%d ms):\n%s\n---\n%s", pr.DurationMs, pb.String(), pr.Text)
}
