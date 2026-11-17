// Package model holds every type that crosses the Wails boundary.
// Wails generates frontend/wailsjs/go/models.ts (namespace `model`) from these.
//
// Conventions:
//   - JSON field names are camelCase.
//   - Timestamps are strings (RFC 3339 for app-state times).
//   - IDs of app-state objects are opaque strings.
//   - "schema" everywhere means: MySQL database name, Postgres schema name, SQLite "main"
//     (or an attached database). An empty schema means the connection's default schema.
package model

// ---------------------------------------------------------------------------
// Enumerations (string typed so they serialize as plain strings)

// Driver identifiers.
const (
	DriverPostgres = "postgres"
	DriverMySQL    = "mysql"
	DriverSQLite   = "sqlite"
)

// Connection colors.
const (
	ColorNone   = "none"
	ColorGreen  = "green"
	ColorYellow = "yellow"
	ColorOrange = "orange"
	ColorRed    = "red"
	ColorBlue   = "blue"
	ColorPurple = "purple"
)

// Password modes.
const (
	PasswordSave = "save" // stored in the app's secrets file (empty password = nothing stored)
	PasswordAsk  = "ask"  // asked on every connect, passed to Connect()
)

// SSL modes (Postgres naming; MySQL maps disable/prefer/require/verify-ca/verify-full).
const (
	SSLDisable    = "disable"
	SSLPrefer     = "prefer"
	SSLRequire    = "require"
	SSLVerifyCA   = "verify-ca"
	SSLVerifyFull = "verify-full"
)

// ---------------------------------------------------------------------------
// App state

type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sortOrder"`
}

type SSHTunnel struct {
	Enabled bool   `json:"enabled"`
	Host    string `json:"host"`
	Port    int    `json:"port"` // 0 = ~/.ssh/config Port, else 22
	// Host may be a ~/.ssh/config Host alias (HostName, User, Port, IdentityFile,
	// IdentitiesOnly, IdentityAgent and ProxyJump are applied; form fields win).
	// User "" = config User, else the OS user.
	User string `json:"user"`
	// "auto" (default: agent + ~/.ssh/config IdentityFile + default key files,
	// like OpenSSH; "agent" and "" are accepted as aliases) | "key" | "password"
	AuthMethod string `json:"authMethod"`
	KeyPath    string `json:"keyPath"`
	// Write-only secrets (stored in the secrets file). nil = keep the stored value,
	// "" = clear it, other = set it. Never returned by the backend.
	Password      *string `json:"password,omitempty"`
	KeyPassphrase *string `json:"keyPassphrase,omitempty"`
	// Read-only: whether a password / passphrase is stored.
	HasPassword      bool `json:"hasPassword"`
	HasKeyPassphrase bool `json:"hasKeyPassphrase"`
}

type ConnectionConfig struct {
	ID        string `json:"id"` // empty on create
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Driver    string `json:"driver"` // postgres | mysql | sqlite

	// Network (postgres/mysql). Socket wins over Host/Port when set.
	// Postgres: Socket is the socket *directory* (e.g. /var/run/postgresql).
	// MySQL: Socket is the socket file path.
	Host   string `json:"host"`
	Port   int    `json:"port"` // 0 = driver default (5432 / 3306)
	Socket string `json:"socket"`
	User   string `json:"user"`

	// Password handling. PasswordMode is "save" or "ask".
	// Password is write-only: nil = keep stored value, "" = clear, other = set.
	// The backend never returns it; HasPassword tells whether one was saved.
	PasswordMode string  `json:"passwordMode"`
	Password     *string `json:"password,omitempty"`
	HasPassword  bool    `json:"hasPassword"`

	Database      string `json:"database"`      // postgres/mysql: database to connect to (mysql: optional)
	FilePath      string `json:"filePath"`      // sqlite: database file
	DefaultSchema string `json:"defaultSchema"` // postgres: e.g. "public"; mysql: defaults to Database
	SSLMode       string `json:"sslMode"`       // see SSL* constants, "" = driver default
	// Extra driver params appended to the DSN (e.g. {"charset":"utf8mb4"}).
	Params map[string]string `json:"params"`
	SSH    SSHTunnel         `json:"ssh"`

	Color     string `json:"color"` // none|green|yellow|orange|red|blue|purple
	ReadOnly  bool   `json:"readOnly"`
	SortOrder int    `json:"sortOrder"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type TestResult struct {
	ServerVersion string `json:"serverVersion"`
	LatencyMs     int64  `json:"latencyMs"`
	Message       string `json:"message"`
}

// ConnectionStatus is returned by Connect/ConnectionStatuses and pushed with the
// "conn:status" event whenever it changes.
type ConnectionStatus struct {
	ConnID        string `json:"connId"`
	Connected     bool   `json:"connected"`
	Connecting    bool   `json:"connecting"`
	ServerVersion string `json:"serverVersion"` // e.g. "MySQL 8.4.3", "PostgreSQL 17.2", "SQLite 3.50.1"
	Database      string `json:"database"`      // current database
	DefaultSchema string `json:"defaultSchema"` // schema used when a call passes ""
	ReadOnly      bool   `json:"readOnly"`      // effective read-only (config flag)
	Error         string `json:"error"`         // last connect error, "" if fine
}

// HostKeyPrompt is pushed with the "ssh:hostkey" event (and described in the
// host_key_unknown error) when an SSH host is contacted for the first time.
type HostKeyPrompt struct {
	ConnID      string `json:"connId"` // "" for an unsaved config (TestConnection)
	Host        string `json:"host"`   // "host:port", pass to TrustHostKey
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"` // SHA256:...
}

// SSHKeyPrompt is pushed with the "ssh:passphrase" event (and described in the
// passphrase_required error) when the server accepts an encrypted key file whose
// passphrase is not known.
type SSHKeyPrompt struct {
	ConnID      string `json:"connId"`
	KeyPath     string `json:"keyPath"` // absolute path; pass to ProvideKeyPassphrase
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
}

// SSHAuthAttempt is one authentication method/key tried during an SSH login.
type SSHAuthAttempt struct {
	Method string `json:"method"` // agent | key | password
	Key    string `json:"key"`    // "~/.ssh/id_ed25519", "ED25519 SHA256:…" (agent key), "" (password)
	// accepted | rejected | not tried | encrypted, no passphrase | passphrase required |
	// not found | unsupported key type | no keys | unavailable: … | error: …
	Result string `json:"result"`
}

// SSHTestResult is returned by TestSSH (tunnel only, no database login).
type SSHTestResult struct {
	OK                 bool             `json:"ok"`         // SSH login succeeded
	AuthMethod         string           `json:"authMethod"` // "agent: ED25519 SHA256:…", "key: ~/.ssh/id_ed25519", "password"
	User               string           `json:"user"`       // SSH user actually used (form, ~/.ssh/config or OS user)
	Address            string           `json:"address"`    // host:port actually dialled (after ~/.ssh/config HostName/Port)
	ProxyJump          string           `json:"proxyJump"`  // jump host used, "" if none
	ServerVersion      string           `json:"serverVersion"`
	HostKeyFingerprint string           `json:"hostKeyFingerprint"` // "ssh-ed25519 SHA256:…"
	Tried              []SSHAuthAttempt `json:"tried"`
	DBReachable        bool             `json:"dbReachable"` // direct-tcpip to the database endpoint opened
	DBEndpoint         string           `json:"dbEndpoint"`  // host:port or socket path, as seen from the SSH host
	DBError            string           `json:"dbError,omitempty"`
	DurationMs         int64            `json:"durationMs"`
}

// FindProgress is pushed with the "find:progress" event during FindInDatabase.
type FindProgress struct {
	SearchID string `json:"searchId"`
	Table    string `json:"table"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Hits     int    `json:"hits"` // tables with matches so far
}

type QueryHistory struct {
	ID         int64  `json:"id"`
	ConnID     string `json:"connId"`
	SQL        string `json:"sql"`
	RanAt      string `json:"ranAt"` // RFC 3339
	DurationMs int64  `json:"durationMs"`
	RowCount   int64  `json:"rowCount"` // rows returned or affected, -1 unknown
	Error      string `json:"error"`
}

type SavedQuery struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	ConnID    string `json:"connId"` // optional: "" = project-wide
	Name      string `json:"name"`
	SQL       string `json:"sql"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type AppInfo struct {
	Version  string `json:"version"`
	DataDir  string `json:"dataDir"`
	Platform string `json:"platform"` // darwin | linux | windows
}

// ---------------------------------------------------------------------------
// .env import

type EnvImportResult struct {
	Config     ConnectionConfig  `json:"config"` // prefilled, ID empty; Password set when found
	Found      map[string]string `json:"found"`  // DB_* keys that were used (DB_PASSWORD masked as "••••")
	Warnings   []string          `json:"warnings"`
	SourcePath string            `json:"sourcePath"`
	Cancelled  bool              `json:"cancelled"` // file dialog cancelled
}

// ---------------------------------------------------------------------------
// Metadata

type SchemaInfo struct {
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
	IsSystem  bool   `json:"isSystem"` // information_schema, pg_catalog, mysql, sys, ...
}

type Table struct {
	Schema    string `json:"schema"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`      // table | view | materialized_view
	EstRows   int64  `json:"estRows"`   // estimate from statistics, -1 unknown
	SizeBytes int64  `json:"sizeBytes"` // -1 unknown
	Comment   string `json:"comment"`
}

type FKRef struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Column string `json:"column"`
}

type Column struct {
	Name          string  `json:"name"`
	Ordinal       int     `json:"ordinal"`  // 1-based
	Type          string  `json:"type"`     // full display type, e.g. "varchar(255)", "decimal(20,4)", "int unsigned"
	DataType      string  `json:"dataType"` // base type lowercased, e.g. "varchar", "decimal", "int"
	Kind          string  `json:"kind"`     // value kind, see Kind* constants
	Nullable      bool    `json:"nullable"`
	Default       *string `json:"default,omitempty"` // raw SQL default expression, nil = none
	PK            bool    `json:"pk"`
	PKOrdinal     int     `json:"pkOrdinal"` // 1-based position in PK, 0 = not in PK
	AutoIncrement bool    `json:"autoIncrement"`
	Generated     bool    `json:"generated"`
	Comment       string  `json:"comment"`
	FK            *FKRef  `json:"fk,omitempty"` // single-column FK target (first one if several)
	// CastType (backend only): type used to cast bound text values (postgres:
	// the type without length/precision so the assignment enforces limits).
	CastType string `json:"-"`
}

// Value kinds (Column.Kind / ResultColumn.Kind): tells the UI how to render/edit.
const (
	KindInt      = "int"
	KindFloat    = "float"
	KindDecimal  = "decimal" // sent as string to keep precision
	KindBool     = "bool"
	KindString   = "string"
	KindText     = "text" // long text (TEXT/CLOB)
	KindJSON     = "json"
	KindDate     = "date"
	KindTime     = "time"
	KindDateTime = "datetime"
	KindBinary   = "binary"
	KindUUID     = "uuid"
	KindOther    = "other"
)

type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"` // expressions appear verbatim
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
	Method  string   `json:"method"` // btree, hash, gin, FULLTEXT, ...
	Where   string   `json:"where"`  // partial index predicate (postgres)
	// Constraint: the index backs a PRIMARY KEY / UNIQUE / EXCLUDE constraint of
	// the same name (postgres); dropping it means dropping the constraint.
	Constraint bool `json:"constraint"`
}

type ForeignKey struct {
	Name       string   `json:"name"`
	Schema     string   `json:"schema"`
	Table      string   `json:"table"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnUpdate   string   `json:"onUpdate"`
	OnDelete   string   `json:"onDelete"`
}

type Trigger struct {
	Name       string `json:"name"`
	Timing     string `json:"timing"` // BEFORE | AFTER | INSTEAD OF
	Event      string `json:"event"`  // INSERT | UPDATE | DELETE (comma separated if several)
	Definition string `json:"definition"`
}

// TableStructure is everything the Structure tab shows, in one call.
type TableStructure struct {
	Table        Table        `json:"table"`
	Columns      []Column     `json:"columns"`
	Indexes      []Index      `json:"indexes"`
	ForeignKeys  []ForeignKey `json:"foreignKeys"`  // outgoing
	ReferencedBy []ForeignKey `json:"referencedBy"` // incoming FKs from other tables
	Triggers     []Trigger    `json:"triggers"`
	DDL          string       `json:"ddl"`
}

// SchemaIndex is the compact table+column list used by the frontend's fuzzy speed
// search and SQL autocomplete. One per (connection, schema); cached in the backend.
type SchemaIndex struct {
	ConnID  string             `json:"connId"`
	Schema  string             `json:"schema"`
	Tables  []SchemaIndexTable `json:"tables"`
	BuiltAt string             `json:"builtAt"` // RFC 3339
}

type SchemaIndexTable struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`    // table | view | materialized_view
	Columns []string `json:"columns"` // in ordinal order
	Types   []string `json:"types"`   // display types, parallel to Columns
}

// SearchHit is one match of a table/column/saved-query search
// (SearchObjects, command palette, sidebar speed search).
type SearchHit struct {
	ConnID  string  `json:"connId"`
	Schema  string  `json:"schema"`
	Table   string  `json:"table"`  // "" for saved queries
	Column  string  `json:"column"` // set when the match was on a column name
	Kind    string  `json:"kind"`   // table | view | materialized_view | column | saved_query
	SavedID string  `json:"savedId"`
	Label   string  `json:"label"` // display text, e.g. "sales_order · customer_email"
	Score   float64 `json:"score"`
	// JS string (UTF-16) offsets into Label of matched characters (for highlighting).
	Positions []int `json:"positions"`
}

// ---------------------------------------------------------------------------
// Data / query results

type ResultColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // display type (from metadata for tables, driver type name for queries)
	Kind     string `json:"kind"` // see Kind* constants
	Nullable bool   `json:"nullable"`
	PK       bool   `json:"pk"`
	FK       *FKRef `json:"fk,omitempty"`
}

// QueryResult is one result set (or one executed statement).
//
// Row cells are encoded as (see CONTRACT.md "Value encoding"):
//
//	null                  SQL NULL
//	string                text, decimal, date/time, json text, uuid, big ints (> 2^53)
//	number                ints within ±2^53, floats
//	boolean               bool
//	{"$type":"binary", "hex": "...", "size": N, "truncated": bool}
//	{"$type":"text", "text": "...", "size": N, "truncated": true}   (long text cut at MaxCellBytes)
type QueryResult struct {
	QueryID      string         `json:"queryId"`
	Index        int            `json:"index"`     // statement index within a RunQuery call
	Statement    string         `json:"statement"` // the SQL that ran
	Columns      []ResultColumn `json:"columns"`
	Rows         [][]any        `json:"rows"`
	HasResultSet bool           `json:"hasResultSet"` // false for INSERT/UPDATE/DDL
	RowsAffected int64          `json:"rowsAffected"` // -1 when not applicable
	Total        int64          `json:"total"`        // total matching rows when known, -1 unknown
	// EstimatedTotal: FetchPage without where/filters: the table's row estimate
	// from statistics (cheap, may be stale); -1 unknown. Use CountRows for exact.
	EstimatedTotal int64  `json:"estimatedTotal"`
	Offset         int64  `json:"offset"`    // offset of Rows[0]
	HasMore        bool   `json:"hasMore"`   // more rows exist beyond this page / maxRows
	Truncated      bool   `json:"truncated"` // result was cut at maxRows (console) or cells truncated
	DurationMs     int64  `json:"durationMs"`
	Error          string `json:"error"` // per-statement error (RunQuery); "" on success
	// Set when the result maps 1:1 to a single table with a PK (FetchPage always;
	// RunQuery when detectable), so the grid can allow editing.
	Source *TableRef `json:"source,omitempty"`
}

type TableRef struct {
	ConnID string `json:"connId"`
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

// ColumnFilter is one per-column filter from the grid's filter popover.
// Values are strings; the backend binds them as parameters (never interpolated).
type ColumnFilter struct {
	Column string `json:"column"`
	// eq | ne | gt | gte | lt | lte | contains | not_contains | starts_with | ends_with |
	// like | not_like | is_null | not_null | in | not_in | between
	Op     string   `json:"op"`
	Value  string   `json:"value"`  // single-value ops
	Values []string `json:"values"` // in / not_in (n values), between (2 values)
}

type PageRequest struct {
	ConnID        string         `json:"connId"`
	Schema        string         `json:"schema"`
	Table         string         `json:"table"`
	Where         string         `json:"where"`   // raw SQL, without the WHERE keyword
	OrderBy       string         `json:"orderBy"` // raw SQL, without ORDER BY
	Limit         int            `json:"limit"`   // default 500, max 10000
	Offset        int64          `json:"offset"`
	ColumnFilters []ColumnFilter `json:"columnFilters"` // AND-ed with Where
	CountTotal    bool           `json:"countTotal"`    // also run COUNT(*) (may be slow on big tables)
}

type QueryRequest struct {
	ConnID  string `json:"connId"`
	QueryID string `json:"queryId"` // client generated (crypto.randomUUID()), used by CancelQuery
	SQL     string `json:"sql"`     // one or more statements separated by ';'
	Schema  string `json:"schema"`  // default schema/database for this run, "" = connection default
	MaxRows int    `json:"maxRows"` // per result set, default 1000, 0 = default
	// SessionID: pass the console tab id. Runs with the same SessionID share one DB
	// session so SET/USE/BEGIN carry over between runs (phase B; until then each
	// run gets a fresh pooled session). Release with CloseSession(sessionId).
	SessionID string `json:"sessionId"`
}

type StatementRange struct {
	Start int    `json:"start"` // UTF-16 offset (JS string index), inclusive
	End   int    `json:"end"`   // exclusive, excludes the trailing ';'
	SQL   string `json:"sql"`
	// Classification (sqltok.Inspect), for the console's safety checks and tab names.
	// Kind: main keyword, lowercase ("select", "update", …; WITH: the statement after the CTEs).
	Kind string `json:"kind"`
	// Writes: changes data or the schema (DML, DDL, GRANT, CALL, …); not SET / USE / BEGIN / COMMIT.
	Writes bool `json:"writes"`
	// Table: first table the statement references, unquoted ("" = none).
	Table string `json:"table"`
	// NoWhere: UPDATE / DELETE without WHERE (or LIMIT / MySQL inner JOIN): affects every row.
	NoWhere bool `json:"noWhere"`
	// TargetSchema / TargetTable: the one plain table of a NoWhere statement (unquoted, as
	// stored; schema "" = default), so its row count can be shown. Empty when not that simple.
	TargetSchema string `json:"targetSchema"`
	TargetTable  string `json:"targetTable"`
}

// CellRequest loads the full value of a truncated cell.
type CellRequest struct {
	ConnID string         `json:"connId"`
	Schema string         `json:"schema"`
	Table  string         `json:"table"`
	PK     map[string]any `json:"pk"`
	Column string         `json:"column"`
}

// ---------------------------------------------------------------------------
// Changes (staged grid edits)

// Values in Set/Values/PK use the same encoding as result cells, plus
// {"$type":"default"} for the DEFAULT keyword. Binary values are sent as
// {"$type":"binary","hex":"..."}.
type RowUpdate struct {
	PK  map[string]any `json:"pk"`
	Set map[string]any `json:"set"`
}

type RowInsert struct {
	Values map[string]any `json:"values"` // omitted columns get their DB default
}

type RowDelete struct {
	PK map[string]any `json:"pk"`
}

type ChangeSet struct {
	ConnID  string      `json:"connId"`
	Schema  string      `json:"schema"`
	Table   string      `json:"table"`
	Updates []RowUpdate `json:"updates"`
	Inserts []RowInsert `json:"inserts"`
	Deletes []RowDelete `json:"deletes"`
}

// ApplyResult reports a transactional apply. Statements are in execution order
// (same order as PreviewChanges): deletes, then updates, then inserts.
type ApplyResult struct {
	OK           bool     `json:"ok"`
	Statements   []string `json:"statements"`
	Executed     int      `json:"executed"`    // statements executed before commit/failure
	FailedIndex  int      `json:"failedIndex"` // index into Statements, -1 if none failed
	Error        string   `json:"error"`
	RowsAffected int64    `json:"rowsAffected"`
	DurationMs   int64    `json:"durationMs"`
	// Primary keys of inserted rows in insert order (when the driver can report them).
	InsertedPKs []map[string]any `json:"insertedPks"`
}

// StructureChange is a staged DDL change from the Structure tab. Statements are
// generated in this order: drop indexes, drop columns, rename columns, add columns.
type StructureChange struct {
	ConnID        string            `json:"connId"`
	Schema        string            `json:"schema"`
	Table         string            `json:"table"`
	DropColumns   []string          `json:"dropColumns"`
	RenameColumns map[string]string `json:"renameColumns"` // old -> new
	DropIndexes   []string          `json:"dropIndexes"`
	AddColumns    []ColumnDef       `json:"addColumns"`
	// ExpectedStatements: pass the statements PreviewStructureChanges returned.
	// ApplyStructureChanges refuses to run when the regenerated DDL differs (the
	// table changed after the preview). Empty = no check.
	ExpectedStatements []string `json:"expectedStatements,omitempty"`
}

// ColumnDef describes a column to add.
type ColumnDef struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // SQL type as typed by the user, e.g. "varchar(64)", "numeric(12,2)"
	Nullable bool   `json:"nullable"`
	// Raw SQL default expression ("'abc'", "0", "CURRENT_TIMESTAMP"); nil/"" = none.
	Default *string `json:"default,omitempty"`
	After   string  `json:"after"` // mysql only: position after this column ("" = last)
}

// ---------------------------------------------------------------------------
// Export / copy

type ExportRequest struct {
	ConnID string `json:"connId"`
	// Exactly one source: a table page request (Limit/Offset ignored -> whole filtered table)
	// or a SQL query (single SELECT).
	Page *PageRequest `json:"page,omitempty"`
	SQL  string       `json:"sql"`
	// csv | tsv | json | sql | markdown
	Format string `json:"format"`
	// Output path; "" = show a save dialog.
	Path          string   `json:"path"`
	IncludeHeader bool     `json:"includeHeader"` // csv/tsv
	SQLTable      string   `json:"sqlTable"`      // table name used in INSERT statements (sql format)
	Columns       []string `json:"columns"`       // subset/order of columns, empty = all
	// Schema: default schema for unqualified names of a SQL source ("" = the connection's
	// default), like QueryRequest.Schema. Ignored for page sources.
	Schema string `json:"schema"`
}

type ExportResult struct {
	Path       string `json:"path"`
	Rows       int64  `json:"rows"`
	Bytes      int64  `json:"bytes"`
	DurationMs int64  `json:"durationMs"`
	Cancelled  bool   `json:"cancelled"` // save dialog cancelled
}

// CopySQLRequest renders rows as INSERT/UPDATE statements in the connection's dialect
// (for "Copy as SQL INSERT / UPDATE").
type CopySQLRequest struct {
	ConnID  string   `json:"connId"`
	Schema  string   `json:"schema"`
	Table   string   `json:"table"`
	Kind    string   `json:"kind"` // insert | update
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`   // cell encoding as in QueryResult
	PKCols  []string `json:"pkCols"` // update: columns used in WHERE
}

// ---------------------------------------------------------------------------
// Find in database (Cmd+Shift+F)

type FindRequest struct {
	ConnID        string   `json:"connId"`
	SearchID      string   `json:"searchId"` // client generated; cancel with CancelQuery(searchId)
	Schema        string   `json:"schema"`
	Tables        []string `json:"tables"` // empty = all tables in schema
	Term          string   `json:"term"`
	Mode          string   `json:"mode"` // contains | exact | starts_with
	CaseSensitive bool     `json:"caseSensitive"`
	LimitPerTable int      `json:"limitPerTable"` // default 50
}

type FindResult struct {
	Schema         string      `json:"schema"`
	Table          string      `json:"table"`
	MatchedColumns []string    `json:"matchedColumns"`
	Result         QueryResult `json:"result"`
}
