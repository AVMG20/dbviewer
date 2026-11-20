// Package ai talks to a local Ollama server and builds the prompts for writing SQL.
package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dbviewer/internal/model"
)

// DefaultBaseURL is where Ollama listens by default.
const DefaultBaseURL = "http://localhost:11434"

// Client is a minimal Ollama API client.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient normalizes baseURL ("" = default, scheme optional).
func NewClient(baseURL string) (*Client, error) {
	u := strings.TrimSpace(baseURL)
	if u == "" {
		u = DefaultBaseURL
	}
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" || (pu.Scheme != "http" && pu.Scheme != "https") {
		return nil, model.Errf(model.ErrInvalid, "%q is not a valid Ollama URL", baseURL)
	}
	return &Client{BaseURL: strings.TrimRight(u, "/"), HTTP: &http.Client{}}, nil
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the subset of /api/chat used here.
type ChatRequest struct {
	Model     string         `json:"model"`
	Messages  []Message      `json:"messages"`
	Stream    bool           `json:"stream"`
	Think     *bool          `json:"think,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
}

type chatChunk struct {
	Message struct {
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	Done         bool   `json:"done"`
	LoadDuration int64  `json:"load_duration"`
	Error        string `json:"error"`
}

// ChatStats are reported by the final chunk.
type ChatStats struct {
	LoadDuration time.Duration
}

func (c *Client) unavailable(err error) error {
	var ne net.Error
	var oe *net.OpError
	if errors.As(err, &oe) || (errors.As(err, &ne) && ne.Timeout()) || strings.Contains(err.Error(), "connection refused") {
		return model.Errf(model.ErrNotConnected, "Ollama is not running at %s. Start the Ollama app (or run `ollama serve`) and try again.", c.BaseURL)
	}
	return model.Wrap(model.ErrInternal, err)
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, model.Wrap(model.ErrInternal, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return nil, model.Wrap(model.ErrInternal, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, model.Errf(model.ErrCanceled, "canceled")
		}
		return nil, c.unavailable(err)
	}
	if res.StatusCode >= 300 {
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if res.StatusCode == http.StatusNotFound && strings.Contains(msg, "not found") {
			return nil, model.Errf(model.ErrNotFound, "%s. Pick an installed model in Settings, or run `ollama pull` first.", msg)
		}
		return nil, model.Errf(model.ErrInternal, "Ollama: %s", msg)
	}
	return res, nil
}

// Models lists the installed models.
func (c *Client) Models(ctx context.Context) ([]model.AIModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := c.do(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Models []struct {
			Name    string `json:"name"`
			Size    int64  `json:"size"`
			Details struct {
				Family        string `json:"family"`
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, model.Wrap(model.ErrInternal, err)
	}
	list := make([]model.AIModel, 0, len(out.Models))
	for _, m := range out.Models {
		list = append(list, model.AIModel{Name: m.Name, SizeBytes: m.Size, ParameterSize: m.Details.ParameterSize, Family: m.Details.Family})
	}
	return list, nil
}

// Capabilities returns the model's capabilities ("completion", "thinking", …).
func (c *Client) Capabilities(ctx context.Context, name string) ([]string, error) {
	res, err := c.do(ctx, http.MethodPost, "/api/show", map[string]any{"model": name})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, model.Wrap(model.ErrInternal, err)
	}
	return out.Capabilities, nil
}

// NumCtx is the context size used for every request. Load requests must use the
// same value: Ollama reloads the model when num_ctx changes.
const NumCtx = 8192

// Options returns the request options (temperature + the load-relevant ones).
func Options(temperature float64) map[string]any {
	return map[string]any{"temperature": temperature, "num_ctx": NumCtx, "num_predict": 1024}
}

// SetKeepAlive loads the model (keepAlive > 0) or unloads it (keepAlive "0").
// Loading uses NumCtx, so a following Chat does not reload the model.
func (c *Client) SetKeepAlive(ctx context.Context, name, keepAlive string) error {
	body := map[string]any{"model": name, "keep_alive": keepAlive}
	if keepAlive != "0" {
		body["options"] = map[string]any{"num_ctx": NumCtx}
	}
	res, err := c.do(ctx, http.MethodPost, "/api/generate", body)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return res.Body.Close()
}

// Chat streams a chat completion; onChunk gets the accumulated content and
// thinking text after every chunk. Returns the whole content.
func (c *Client) Chat(ctx context.Context, req ChatRequest, onChunk func(content, thinking string)) (string, ChatStats, error) {
	req.Stream = true
	res, err := c.do(ctx, http.MethodPost, "/api/chat", req)
	if err != nil {
		return "", ChatStats{}, err
	}
	defer res.Body.Close()
	var content, thinking strings.Builder
	var stats ChatStats
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ch chatChunk
		if err := json.Unmarshal(line, &ch); err != nil {
			continue
		}
		if ch.Error != "" {
			return content.String(), stats, model.Errf(model.ErrInternal, "Ollama: %s", ch.Error)
		}
		content.WriteString(ch.Message.Content)
		thinking.WriteString(ch.Message.Thinking)
		if onChunk != nil && (ch.Message.Content != "" || ch.Message.Thinking != "") {
			onChunk(content.String(), thinking.String())
		}
		if ch.Done {
			stats.LoadDuration = time.Duration(ch.LoadDuration)
			break
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return content.String(), stats, model.Errf(model.ErrCanceled, "canceled")
		}
		return content.String(), stats, model.Wrap(model.ErrInternal, fmt.Errorf("reading the Ollama response: %w", err))
	}
	return content.String(), stats, nil
}

// GenerateRequest is the subset of /api/generate used for completions. Suffix
// makes it a fill-in-the-middle request (models with the "insert" capability).
type GenerateRequest struct {
	Model     string         `json:"model"`
	Prompt    string         `json:"prompt"`
	Suffix    string         `json:"suffix,omitempty"`
	Raw       bool           `json:"raw,omitempty"`
	Stream    bool           `json:"stream"`
	Think     *bool          `json:"think,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
}

// GenerateStats are reported by a finished request.
type GenerateStats struct {
	LoadDuration       time.Duration
	PromptEvalCount    int
	PromptEvalDuration time.Duration
	EvalCount          int
	EvalDuration       time.Duration
}

// Generate streams one /api/generate request; onChunk (optional) gets the text so
// far after every chunk. Returns the whole text.
func (c *Client) Generate(ctx context.Context, req GenerateRequest, onChunk func(text string)) (string, GenerateStats, error) {
	req.Stream = true
	res, err := c.do(ctx, http.MethodPost, "/api/generate", req)
	if err != nil {
		return "", GenerateStats{}, err
	}
	defer res.Body.Close()
	var text strings.Builder
	var stats GenerateStats
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ch struct {
			Response           string `json:"response"`
			Done               bool   `json:"done"`
			Error              string `json:"error"`
			LoadDuration       int64  `json:"load_duration"`
			PromptEvalCount    int    `json:"prompt_eval_count"`
			PromptEvalDuration int64  `json:"prompt_eval_duration"`
			EvalCount          int    `json:"eval_count"`
			EvalDuration       int64  `json:"eval_duration"`
		}
		if err := json.Unmarshal(line, &ch); err != nil {
			continue
		}
		if ch.Error != "" {
			return text.String(), stats, model.Errf(model.ErrInternal, "Ollama: %s", ch.Error)
		}
		text.WriteString(ch.Response)
		if onChunk != nil && ch.Response != "" {
			onChunk(text.String())
		}
		if ch.Done {
			stats = GenerateStats{
				LoadDuration:       time.Duration(ch.LoadDuration),
				PromptEvalCount:    ch.PromptEvalCount,
				PromptEvalDuration: time.Duration(ch.PromptEvalDuration),
				EvalCount:          ch.EvalCount,
				EvalDuration:       time.Duration(ch.EvalDuration),
			}
			break
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return text.String(), stats, model.Errf(model.ErrCanceled, "canceled")
		}
		return text.String(), stats, model.Wrap(model.ErrInternal, fmt.Errorf("reading the Ollama response: %w", err))
	}
	return text.String(), stats, nil
}
