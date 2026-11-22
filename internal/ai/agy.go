package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dbviewer/internal/model"
)

const (
	// ProviderAgy runs prompts through the Antigravity CLI (`agy`).
	ProviderAgy = "agy"
	// ProviderOllama runs prompts through a local Ollama server.
	ProviderOllama = "ollama"
	// DefaultAgyModel is the default Antigravity CLI model (Hydrogen).
	DefaultAgyModel = "gemini-4-hydrogen-medium"
)

// AgyClient runs AI prompts and model discovery through the Antigravity CLI (`agy`).
type AgyClient struct {
	// Binary overrides the path to `agy` (empty = auto-detect).
	Binary string
}

// NewAgyClient returns an Antigravity CLI client.
func NewAgyClient() *AgyClient {
	return &AgyClient{}
}

// FindAgyBinary locates the `agy` executable, checking PATH and common user/Homebrew
// install directories (macOS .app bundles launch with a minimal PATH).
func FindAgyBinary() (string, error) {
	if p, err := exec.LookPath("agy"); err == nil {
		return p, nil
	}
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "agy"),
			filepath.Join(home, "bin", "agy"),
			filepath.Join(home, ".antigravity", "bin", "agy"),
		)
	}
	candidates = append(candidates,
		"/opt/homebrew/bin/agy",
		"/usr/local/bin/agy",
	)
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", model.Errf(model.ErrNotConnected, "Antigravity CLI (agy) was not found. Install it and ensure `agy` is available (e.g. ~/.local/bin/agy).")
}

func (c *AgyClient) binary() (string, error) {
	if strings.TrimSpace(c.Binary) != "" {
		return c.Binary, nil
	}
	return FindAgyBinary()
}

// agyEnv returns the current environment with common bin dirs added to PATH so
// GUI-launched macOS bundles can resolve tools used by `agy`.
func agyEnv() []string {
	env := os.Environ()
	var extra []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		extra = append(extra, filepath.Join(home, ".local", "bin"))
	}
	extra = append(extra, "/opt/homebrew/bin", "/usr/local/bin")
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			cur := kv[len("PATH="):]
			for _, dir := range extra {
				if !strings.Contains(cur, dir) {
					cur = dir + string(os.PathListSeparator) + cur
				}
			}
			env[i] = "PATH=" + cur
			return env
		}
	}
	return append(env, "PATH="+strings.Join(extra, string(os.PathListSeparator)))
}

// agyWorkDir returns an empty scratch directory so `agy` does not treat a
// project repository as workspace context during direct SQL/text generation.
func agyWorkDir() string {
	dir := filepath.Join(os.TempDir(), "dbviewer-agy")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// Models lists the available models reported by `agy models`.
func (c *AgyClient) Models(ctx context.Context) ([]model.AIModel, error) {
	bin, err := c.binary()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "models")
	cmd.Dir = agyWorkDir()
	cmd.Env = agyEnv()
	configureAgyCmd(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, model.Errf(model.ErrNotConnected, "Antigravity CLI (agy models) timed out")
			}
			return nil, model.Errf(model.ErrCanceled, "canceled")
		}
		msg := agyErrorLine(stderr.String(), stdout.String(), err.Error())
		return nil, model.Errf(model.ErrNotConnected, "Antigravity CLI: %s", msg)
	}
	return ParseAgyModels(stdout.String()), nil
}

// ParseAgyModels parses the stdout of `agy models` (lines of "<id>\t<label>" or "<id>  <label>").
func ParseAgyModels(output string) []model.AIModel {
	var list []model.AIModel
	seen := map[string]bool{}
	for _, raw := range strings.Split(output, "\n") {
		// Strip spinner updates that overwrite via carriage return.
		if idx := strings.LastIndexByte(raw, '\r'); idx >= 0 {
			raw = raw[idx+1:]
		}
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var id, label string
		if tab := strings.IndexByte(line, '\t'); tab >= 0 {
			id = strings.TrimSpace(line[:tab])
			label = strings.TrimSpace(line[tab+1:])
		} else {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			id = fields[0]
			if len(fields) > 1 {
				label = strings.TrimSpace(line[len(id):])
			}
		}
		if id == "" || strings.HasPrefix(id, "Fetching") || strings.ContainsAny(id, " \t") || seen[id] {
			continue
		}
		seen[id] = true
		if label == "" {
			label = id
		}
		list = append(list, model.AIModel{Name: id, Label: label})
	}
	return list
}

// FormatAgyPrompt combines chat messages into a single direct-answer prompt for `agy --print`.
func FormatAgyPrompt(msgs []Message, retry bool) string {
	var b strings.Builder
	b.WriteString("IMPORTANT: Answer directly in plain text only. Do NOT call any tools, do NOT inspect or read any files, do NOT run any shell commands. Everything you need is provided below.\n\n")
	for _, m := range msgs {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		switch m.Role {
		case "system":
			b.WriteString("Instructions:\n")
			b.WriteString(content)
			b.WriteString("\n\n")
		case "assistant":
			b.WriteString("Assistant:\n")
			b.WriteString(content)
			b.WriteString("\n\n")
		default:
			b.WriteString(content)
			b.WriteString("\n\n")
		}
	}
	if retry {
		b.WriteString("Provide an alternative answer if appropriate.\n")
	}
	return strings.TrimSpace(b.String())
}

type agyStreamEvent struct {
	Event      string `json:"event"`
	StepUpdate *struct {
		StepType  string `json:"step_type"`
		State     string `json:"state"`
		TextDelta string `json:"text_delta"`
	} `json:"step_update"`
	Result *struct {
		Status   string `json:"status"`
		Response string `json:"response"`
		Error    string `json:"error"`
	} `json:"result"`
}

// Chat runs a prompt through `agy --print` in streaming NDJSON mode.
// onChunk receives the accumulated content so far and whether the model is still thinking.
func (c *AgyClient) Chat(ctx context.Context, modelName string, msgs []Message, retry bool, onChunk func(content string, thinking bool)) (string, error) {
	bin, err := c.binary()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = DefaultAgyModel
	}
	prompt := FormatAgyPrompt(msgs, retry)
	args := []string{
		"--model=" + modelName,
		"--output-format=stream-json",
		"--disable-slash-commands",
		"--print=" + prompt,
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = agyWorkDir()
	cmd.Env = agyEnv()
	configureAgyCmd(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", model.Wrap(model.ErrInternal, err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", model.Errf(model.ErrNotConnected, "Antigravity CLI: %v", err)
	}

	reply, streamErr := parseAgyStream(stdout, onChunk)
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return reply, model.Errf(model.ErrCanceled, "canceled")
	}
	if streamErr != nil {
		return reply, streamErr
	}
	if waitErr != nil {
		msg := agyErrorLine(stderr.String(), waitErr.Error())
		if isAgyModelError(msg) {
			return reply, model.Errf(model.ErrNotFound, "%s. Pick an available model in Settings → AI.", msg)
		}
		return reply, model.Errf(model.ErrInternal, "Antigravity CLI: %s", msg)
	}
	return reply, nil
}

func parseAgyStream(r io.Reader, onChunk func(content string, thinking bool)) (string, error) {
	var content strings.Builder
	var nonJSON []string
	var finalResp string

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' {
			nonJSON = append(nonJSON, string(line))
			continue
		}
		var ev agyStreamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch ev.Event {
		case "step_update":
			if ev.StepUpdate == nil {
				continue
			}
			if ev.StepUpdate.StepType == "agent_response" {
				if ev.StepUpdate.TextDelta != "" {
					content.WriteString(ev.StepUpdate.TextDelta)
					if onChunk != nil {
						onChunk(content.String(), false)
					}
				} else if ev.StepUpdate.State == "ACTIVE" && content.Len() == 0 && onChunk != nil {
					onChunk("", true)
				}
			}
		case "result":
			if ev.Result == nil {
				continue
			}
			if strings.EqualFold(ev.Result.Status, "ERROR") || ev.Result.Error != "" {
				msg := firstNonEmptyLine(ev.Result.Error, "request failed")
				if isAgyModelError(msg) {
					return content.String(), model.Errf(model.ErrNotFound, "%s. Pick an available model in Settings → AI.", msg)
				}
				return content.String(), model.Errf(model.ErrInternal, "Antigravity CLI: %s", msg)
			}
			if ev.Result.Response != "" {
				finalResp = ev.Result.Response
			}
		}
	}
	if err := sc.Err(); err != nil {
		return content.String(), model.Wrap(model.ErrInternal, fmt.Errorf("reading Antigravity CLI response: %w", err))
	}
	out := content.String()
	if out == "" && finalResp != "" {
		out = finalResp
		if onChunk != nil {
			onChunk(out, false)
		}
	}
	if out == "" && len(nonJSON) > 0 {
		msg := firstNonEmptyLine(strings.Join(nonJSON, "\n"))
		if isAgyModelError(msg) {
			return "", model.Errf(model.ErrNotFound, "%s. Pick an available model in Settings → AI.", msg)
		}
		return "", model.Errf(model.ErrInternal, "Antigravity CLI: %s", msg)
	}
	return out, nil
}

func isAgyModelError(msg string) bool {
	l := strings.ToLower(msg)
	return strings.Contains(l, "invalid model selection") || strings.Contains(l, "not recognized as a known model")
}

func agyErrorLine(texts ...string) string {
	var fallback string
	for _, t := range texts {
		for _, raw := range strings.Split(t, "\n") {
			if idx := strings.LastIndexByte(raw, '\r'); idx >= 0 {
				raw = raw[idx+1:]
			}
			s := strings.TrimSpace(raw)
			if s == "" || strings.HasPrefix(s, "Fetching available models") {
				continue
			}
			low := strings.ToLower(s)
			if strings.Contains(low, "error") || strings.Contains(low, "failed") || strings.Contains(low, "invalid") || strings.Contains(low, "not authenticated") || strings.Contains(low, "login") {
				return truncateText(s, 300)
			}
			fallback = s
		}
	}
	if fallback != "" {
		return truncateText(fallback, 300)
	}
	return firstNonEmptyLine(texts...)
}

func firstNonEmptyLine(texts ...string) string {
	for _, t := range texts {
		for _, line := range strings.Split(t, "\n") {
			s := strings.TrimSpace(line)
			if s != "" {
				return truncateText(s, 300)
			}
		}
	}
	return ""
}
