package model

// AIModel is one model installed in the local Ollama server (AIService.Models).
type AIModel struct {
	Name          string `json:"name"`
	SizeBytes     int64  `json:"sizeBytes"`
	ParameterSize string `json:"parameterSize"` // e.g. "4.0B", "" unknown
	Family        string `json:"family"`
}

// AISettings says which Ollama server and model to use.
type AISettings struct {
	// BaseURL of the Ollama server; "" = http://localhost:11434.
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	// KeepAlive: how long Ollama keeps the model loaded after a request
	// ("0" unloads right away, "1m", "5m", "30m"); "" = Ollama's default.
	KeepAlive string `json:"keepAlive"`
}

// AISQLRequest asks for one SQL statement (AIService.GenerateSQL).
type AISQLRequest struct {
	AISettings
	// RequestID tags the ai:chunk events of this request.
	RequestID string `json:"requestId"`
	ConnID    string `json:"connId"`
	Schema    string `json:"schema"` // "" = default schema
	// Prompt: what the user asked for.
	Prompt string `json:"prompt"`
	// SQL: the query to change ("" = write a new query).
	SQL string `json:"sql"`
	// Context: the rest of the editor (tables it references are preferred).
	Context string `json:"context"`
	// Retry: ask for a different answer than last time (higher temperature).
	Retry bool `json:"retry"`
}

// AISQLResult is the generated statement.
type AISQLResult struct {
	SQL string `json:"sql"`
	// Tables: the tables whose schema was given to the model.
	Tables []string `json:"tables"`
	// TotalTables: tables in the schema.
	TotalTables int    `json:"totalTables"`
	Model       string `json:"model"`
	DurationMs  int64  `json:"durationMs"`
	// LoadMs: time spent loading the model into memory (0 when it was loaded).
	LoadMs int64 `json:"loadMs"`
}

// AIChunk is streamed while GenerateSQL runs (event "ai:chunk").
type AIChunk struct {
	RequestID string `json:"requestId"`
	// SQL so far (the whole text, not a delta).
	SQL string `json:"sql"`
	// Thinking: the model is still reasoning (no SQL yet).
	Thinking bool `json:"thinking"`
	// Text so far of a plain-language answer (ExplainSQL, ExplainPlan); SQL is "" then.
	Text string `json:"text"`
}

// AITaskRequest is the input of the AI tasks; each task uses the fields it names.
type AITaskRequest struct {
	AISettings
	// RequestID tags the ai:chunk events of this request.
	RequestID string `json:"requestId"`
	ConnID    string `json:"connId"`
	Schema    string `json:"schema"` // "" = default schema
	// SQL: the statement to fix / convert / explain / name; ExplainPlan: the explained statement.
	SQL string `json:"sql"`
	// Error: the database error of the failed statement (FixSQL).
	Error string `json:"error"`
	// Target dialect for ConvertSQL: "mysql", "postgres" or "sqlite".
	Target string `json:"target"`
	// Plan: the EXPLAIN output as tab-separated text with a header line (ExplainPlan).
	Plan string `json:"plan"`
	// Table to filter (FilterRows) and what the user asked for in plain language.
	Table  string `json:"table"`
	Prompt string `json:"prompt"`
	// Retry: ask for a different answer than last time (higher temperature).
	Retry bool `json:"retry"`
}

// AITextResult is a plain-language answer (ExplainSQL, ExplainPlan, NameQuery).
type AITextResult struct {
	Text string `json:"text"`
	// Tables: the tables whose schema was given to the model.
	Tables     []string `json:"tables"`
	Model      string   `json:"model"`
	DurationMs int64    `json:"durationMs"`
	LoadMs     int64    `json:"loadMs"`
}

// AIFilterResult is a WHERE (and optional ORDER BY) for one table (FilterRows).
type AIFilterResult struct {
	// Where: the condition without the WHERE keyword ("" = no condition).
	Where string `json:"where"`
	// OrderBy: the sort without ORDER BY ("" = the request did not ask for sorting).
	OrderBy string `json:"orderBy"`
	// Values: the distinct values of low-cardinality columns that were given to the model.
	Values     map[string][]string `json:"values"`
	Model      string              `json:"model"`
	DurationMs int64               `json:"durationMs"`
	LoadMs     int64               `json:"loadMs"`
}

// AICompleteRequest asks for an inline suggestion at the cursor of the SQL console
// (AIService.CompleteSQL). Model is the suggestion model.
type AICompleteRequest struct {
	AISettings
	ConnID string `json:"connId"`
	Schema string `json:"schema"` // "" = default schema
	// Before / After: the editor text before and after the cursor (the frontend
	// sends at most ~4000 / ~1500 characters).
	Before string `json:"before"`
	After  string `json:"after"`
}

// AICompleteResult is the suggestion ("" = none).
type AICompleteResult struct {
	Text string `json:"text"`
	// Mode: "fim" (fill-in-the-middle, models with the insert capability) or "chat".
	Mode  string `json:"mode"`
	Model string `json:"model"`
	// Tables: the tables whose schema was given to the model.
	Tables     []string `json:"tables"`
	DurationMs int64    `json:"durationMs"`
	LoadMs     int64    `json:"loadMs"`
	// Raw: the model's reply before clean-up (for debugging).
	Raw string `json:"raw"`
}
