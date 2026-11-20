package services

import (
	"context"
	"strings"
	"time"

	"dbviewer/internal/ai"
	"dbviewer/internal/model"
)

// CompleteSQL suggests the text that continues the SQL console at the cursor
// (inline "ghost text"): req.Before / req.After are the editor text around the
// cursor. Models with Ollama's "insert" capability (qwen2.5-coder, …) get a
// fill-in-the-middle request with the schema as a comment block; other models a
// short chat prompt with the cursor marked. Returns Text "" when the model has
// nothing useful. Called while the user types: cancel the promise when the editor
// changes (Ollama stops generating).
func (s *AIService) CompleteSQL(ctx context.Context, req model.AICompleteRequest) (model.AICompleteResult, error) {
	return ret(s.completeSQL(ctx, req))
}

func (s *AIService) completeSQL(ctx context.Context, req model.AICompleteRequest) (model.AICompleteResult, error) {
	start := time.Now()
	if err := checkModel(req.AISettings); err != nil {
		return model.AICompleteResult{}, err
	}
	if strings.TrimSpace(req.Before) == "" {
		return model.AICompleteResult{Model: req.Model}, nil
	}
	cl, err := ai.NewClient(req.BaseURL)
	if err != nil {
		return model.AICompleteResult{}, err
	}
	p, err := s.c.pool(ctx, req.ConnID)
	if err != nil {
		return model.AICompleteResult{}, err
	}
	driver := p.Driver.Name()
	in := ai.SplitCompletion(driver, req.Before, req.After)
	if strings.TrimSpace(in.Before) == "" && strings.TrimSpace(in.After) == "" {
		return model.AICompleteResult{Model: req.Model}, nil
	}
	infos, err := s.tableInfos(ctx, p, req.Schema)
	if err != nil {
		return model.AICompleteResult{}, err
	}
	picked := ai.PickCompletionTables(in, infos)
	schemaText, names := ai.CompletionSchemaText(picked, in)
	in.Names = map[string]string{}
	for _, t := range picked {
		in.Names[strings.ToLower(t.Name)] = t.Name
		for _, c := range t.Columns {
			in.Names[strings.ToLower(c.Name)] = c.Name
		}
	}
	res := model.AICompleteResult{Model: req.Model, Tables: names}
	opts := map[string]any{
		"temperature": 0.1,
		"num_ctx":     ai.NumCtx,
		"num_predict": ai.CompleteMaxTokens,
		"stop":        []string{"\n\n", ";"},
	}
	// stream and stop as soon as the reply went past what is shown (one clause / line)
	gctx, stop := context.WithCancel(ctx)
	defer stop()
	stopped := false
	onText := func(text string) {
		if _, complete := ai.CompletionText(text, in); complete && !stopped {
			stopped = true
			stop()
		}
	}
	var reply string
	if s.hasCap(ctx, cl, req.Model, "insert") {
		res.Mode = "fim"
		prefix, suffix := ai.FIMPrompt(driver, schemaText, in)
		if suffix == "" {
			suffix = "\n" // an empty suffix turns the request into a plain completion
		}
		var stats ai.GenerateStats
		reply, stats, err = cl.Generate(gctx, ai.GenerateRequest{Model: req.Model, Prompt: prefix, Suffix: suffix, KeepAlive: req.KeepAlive, Options: opts}, onText)
		res.LoadMs = stats.LoadDuration.Milliseconds()
	} else {
		res.Mode = "chat"
		chat := ai.ChatRequest{Model: req.Model, Messages: ai.CompleteMessages(driver, schemaText, in), KeepAlive: req.KeepAlive, Options: opts}
		if s.supportsThink(ctx, cl, req.Model) {
			off := false
			chat.Think = &off
		}
		var stats ai.ChatStats
		reply, stats, err = cl.Chat(gctx, chat, func(content, _ string) { onText(content) })
		res.LoadMs = stats.LoadDuration.Milliseconds()
	}
	if err != nil && stopped && ctx.Err() == nil {
		err = nil // stopped on purpose
	}
	if err != nil {
		return model.AICompleteResult{}, wrap(err)
	}
	res.Raw = reply
	res.Text = ai.CleanCompletion(reply, in)
	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}
