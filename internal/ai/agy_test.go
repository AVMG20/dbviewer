package ai

import (
	"errors"
	"strings"
	"testing"

	"dbviewer/internal/model"
)

func TestParseAgyModels(t *testing.T) {
	raw := "\r⠋ Fetching available models...\rgemini-4-neon-high\tGemini 4 Neon (High)\n" +
		"gemini-4-hydrogen-medium\tGemini 4 Hydrogen (Medium)\n" +
		"gemini-4-hydrogen-medium\tGemini 4 Hydrogen (Medium)\n" + // duplicate ignored
		"claude-sonnet-5-5-medium  Claude Sonnet 5.5 (Medium)\n" +
		"\n"
	got := ParseAgyModels(raw)
	if len(got) != 3 {
		t.Fatalf("got %d models: %+v", len(got), got)
	}
	if got[0].Name != "gemini-4-neon-high" || got[0].Label != "Gemini 4 Neon (High)" {
		t.Errorf("model 0: %+v", got[0])
	}
	if got[1].Name != "gemini-4-hydrogen-medium" || got[1].Label != "Gemini 4 Hydrogen (Medium)" {
		t.Errorf("model 1: %+v", got[1])
	}
	if got[2].Name != "claude-sonnet-5-5-medium" || got[2].Label != "Claude Sonnet 5.5 (Medium)" {
		t.Errorf("model 2: %+v", got[2])
	}
}

func TestFormatAgyPrompt(t *testing.T) {
	p := FormatAgyPrompt([]Message{
		{Role: "system", Content: "Write SQLite SQL."},
		{Role: "user", Content: "Count orders."},
	}, false)
	if !strings.Contains(p, "Do NOT call any tools") || !strings.Contains(p, "Write SQLite SQL.") || !strings.Contains(p, "Count orders.") {
		t.Fatalf("unexpected prompt:\n%s", p)
	}
	pRetry := FormatAgyPrompt([]Message{{Role: "user", Content: "Count orders."}}, true)
	if !strings.Contains(pRetry, "alternative answer") {
		t.Errorf("expected retry hint:\n%s", pRetry)
	}
}

func TestParseAgyStream(t *testing.T) {
	stream := strings.Join([]string{
		`{"event":"init","conversation_id":"c1"}`,
		`{"event":"step_update","step_update":{"step_type":"user_input","state":"DONE"}}`,
		"{\"event\":\"step_update\",\"step_update\":{\"step_type\":\"agent_response\",\"state\":\"ACTIVE\",\"text_delta\":\"```sql\\nSELECT \"}}",
		"{\"event\":\"step_update\",\"step_update\":{\"step_type\":\"agent_response\",\"state\":\"DONE\",\"text_delta\":\"1;\\n```\"}}",
		"{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"```sql\\nSELECT 1;\\n```\"}}",
	}, "\n")
	var chunks []string
	reply, err := parseAgyStream(strings.NewReader(stream), func(content string, _ bool) {
		chunks = append(chunks, content)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply != "```sql\nSELECT 1;\n```" {
		t.Errorf("reply = %q", reply)
	}
	if len(chunks) != 2 || chunks[0] != "```sql\nSELECT " || chunks[1] != reply {
		t.Errorf("chunks = %#v", chunks)
	}
}

func TestParseAgyStreamErrors(t *testing.T) {
	stream := `{"event":"result","result":{"status":"ERROR","error":"invalid model selection (--model \"bad\"): model bad is not recognized as a known model\nAvailable models:\n  Gemini 4 Hydrogen (Medium)"}}`
	_, err := parseAgyStream(strings.NewReader(stream), nil)
	var ae *model.AppError
	if !errors.As(err, &ae) || ae.Code != model.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	stream2 := `{"event":"result","result":{"status":"ERROR","error":"authentication required"}}`
	_, err = parseAgyStream(strings.NewReader(stream2), nil)
	if !errors.As(err, &ae) || ae.Code != model.ErrInternal {
		t.Fatalf("expected ErrInternal, got %v", err)
	}
}
