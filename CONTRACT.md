# DB Viewer — contract

This file is the working agreement between the backend engineer (Opus) and the two
frontend agents (Sonnet "shell" and Sonnet "data"). PLAN.md is the product spec;
this file says who owns what, and what the API looks like. When the two disagree on
API details, this file wins.

## 1. Ground rules

- **Frontend agents never edit** `frontend/src/api/`, `frontend/src/types/`,
  `frontend/bindings/`, any Go code, `go.mod`, `Taskfile.yml`, `build/`, `testdata/`.
  If you need an API or shared-type change, add an entry to `NEEDS.md`
  (what, why, proposed signature) and work around it with a local stub meanwhile.
- **Frontend agents never run `npm install`.** All dependencies are installed
  (see §9). Need another package? Put it in NEEDS.md.
- Only edit files you own (§2). If a change in someone else's file is needed,
  ask for it in NEEDS.md.
- All backend calls go through `@/api` (`frontend/src/api/index.ts`), never
  import `frontend/bindings/` directly.
- `npm run build` (in `frontend/`) must pass before you hand back.

## 2. File ownership

| Owner | Files |
|---|---|
| **Opus (backend)** | all Go code (`main.go`, `internal/**`), `go.mod`, `Taskfile.yml`, `build/`, `scripts/`, `testdata/`, `frontend/src/api/`, `frontend/src/types/`, `frontend/bindings/` (generated), `frontend/vite.config.ts`, `frontend/package.json`, `CONTRACT.md` |
| **Sonnet "shell"** | `frontend/src/App.vue`, `frontend/src/main.ts`, `components/sidebar/*`, `components/connection/*`, `components/palette/*`, `components/tabs/*`, `components/statusbar/*`, `stores/projects.ts`, `stores/tabs.ts`, `stores/ui.ts`, `styles/*`, any new `components/common/*` (modals, menus, toasts, icons) |
| **Sonnet "data"** | `components/table/*`, `components/grid/*`, `components/console/*`, `components/structure/*`, `stores/changes.ts`, `lib/*` (clipboard, copy formats, CSV/TSV parse, value formatting, fuzzy helpers for the grid) |

Shared, read-only for the other agent: the store APIs (§6) and `styles/theme.css`
tokens. The data agent may use `components/common/*` once the shell agent creates
them; until then, keep small local components inside your own folders.

The `bindings` directory replaces Wails v2's `wailsjs/`.

## 3. Running

| What | Command |
|---|---|
| Seed dummy DBs | `testdata/seed.sh` (or `wails3 task seed`) — idempotent |
| **Browser dev (for UI work and testing)** | `wails3 task dev:browser` (= `scripts/dev-browser.sh`), open **http://localhost:9245** |
| Desktop dev window | `DBVIEWER_DATA_DIR=testdata/appdata wails3 dev` |
| Frontend type-check + build | `cd frontend && npm run build` |
| Regenerate bindings (after Go API changes) | `wails3 generate bindings -clean=true -ts -i` |
| Go | `go build ./... && go vet ./... && go test ./...` |
| Desktop build | `wails3 build` → `bin/dbviewer` |

Use `/opt/workbench/profile/bin` (node, npm) and `~/go/bin` (wails3) on `PATH`.
On macOS, cgo prints harmless `ld: warning ... built for newer macOS` lines unless
`CGO_CFLAGS/CGO_LDFLAGS=-mmacosx-version-min=12.0` is set (the Taskfile and scripts do).

**Browser dev mode** runs the Go app headless in Wails *server mode* on
`127.0.0.1:9246` and Vite (with HMR) on `127.0.0.1:9245`. Vite proxies `/wails/*`
(bound calls and the events websocket) to the backend, so the page at
http://localhost:9245 (or http://127.0.0.1:9245) has fully working bindings and
events in any browser (Chrome, headless or not). Differences from the desktop app:
- Native dialogs (`ImportEnvFile("")`, `OpenSQLiteFileDialog()`, `Export` with
  `path: ""`) are not available; they reject. Always offer a text fallback: paste
  `.env` text (`ParseEnv`), type a file path, type an export path.
- No traffic lights; the title bar still renders with its left inset.
- Go changes need a restart of the script; frontend changes hot-reload.
- App state lives in `testdata/appdata` (pre-populated with two projects and five
  dummy connections, see testdata/README.md); secrets in `testdata/appdata/secrets.json`.
  Delete the directory to start empty.

Wails logs a "Browser Environment Detected" console warning in the browser; ignore it.

## 4. Backend API

Five services, generated into `frontend/bindings/dbviewer/internal/services/` and
re-exported by `@/api` as `projectApi`, `connectionApi`, `schemaApi`, `dataApi`,
`queryApi` (+ `runQuery` shortcut). Types are re-exported from `@/api` with
null-free arrays/maps (`Project`, `ConnectionConfig`, `QueryResult`, ...).

Conventions:
- Every call returns a `CancellablePromise`. `promise.cancel()` cancels the Go
  `context` (use it for FetchPage / CountRows / RunQuery when a tab closes or a new
  request supersedes the old one). A cancelled call rejects with `code: "canceled"`.
- Arrays and maps in results are never `null` (the backend normalizes them).
- `schema` everywhere = MySQL database name / Postgres schema / SQLite `main`.
  `""` = the connection's default schema (`ConnectionStatus.defaultSchema`).
- Connections with `passwordMode: "save"` connect lazily on first use; you do not
  need to call `Connect` first. `passwordMode: "ask"` connections reject with
  `password_required` until `Connect(id, password)` succeeded.

### Errors

A failing call rejects with an `Error` whose `cause` is `{code, message}` and whose
`message` is `"<code>: <message>"`. Use `apiError(e)` → `{code, message}`.

| code | meaning |
|---|---|
| `not_found` | project / connection / table does not exist |
| `invalid_argument` | bad input (unknown filter op, missing name, unknown column...) |
| `not_connected` | connection is not open |
| `password_required` | `ask` connection, call `Connect(id, password)` first |
| `read_only` | write attempted on a read-only connection |
| `no_primary_key` | edit/delete on a table without PK |
| `canceled` | the call or query was cancelled |
| `keychain` | no longer produced (the Keychain is not used); kept for compatibility |
| `db_error` | database error (message = driver message) |
| `not_implemented` | endpoint exists but is not built yet |
| `unsupported` | not available in this mode (native dialogs in browser dev mode: use a typed path / pasted text) |
| `ssh_error` | SSH tunnel failure (auth, network, key file) |
| `host_key_unknown` | first contact with an SSH host: ask the user, then `TrustHostKey(host, fingerprint)` and retry |
| `host_key_mismatch` | the SSH host key changed: hard failure, never offer to trust |
| `passphrase_required` | an encrypted SSH key accepted by the server needs its passphrase: prompt, `ProvideKeyPassphrase(keyPath, passphrase, remember)`, retry (the key path is in the `ssh:passphrase` event; the message names it too) |
| `internal` | anything else |

Per-statement failures in `RunQuery` and per-transaction failures in
`ApplyChanges` are *not* rejections; they come back in the result (`QueryResult.error`,
`ApplyResult.ok=false`). `statementError(str)` helps parse them.

### Events

| Event | Payload | Helper |
|---|---|---|
| `conn:status` | `ConnectionStatus` | `onConnStatus(cb)` → unsubscribe fn |
| `ssh:hostkey` | `HostKeyPrompt {connId, host, keyType, fingerprint}` | `onSshHostKey(cb)` |
| `ssh:passphrase` | `SSHKeyPrompt {connId, keyPath, keyType, fingerprint}` | `onSshPassphrase(cb)` |
| `find:progress` | `FindProgress {searchId, table, done, total, hits}` | `onFindProgress(cb)` |

Emitted on connect (first with `connecting: true`, then `connected: true`),
disconnect, connect failure (`error` set) and connection config change (disconnect).

### ProjectService (`projectApi`)

| Method | Returns | Notes / status |
|---|---|---|
| `AppInfo()` | `AppInfo {version, dataDir, platform}` | ✅ |
| `Projects()` | `Project[]` | ✅ sorted by sortOrder; there is always ≥ 1 ("Default" created on first run) |
| `SaveProject(p)` | `Project` | ✅ `id: ""` creates |
| `DeleteProject(id)` | `void` | ✅ deletes its connections (+ secrets, history) and saved queries; refuses the last project (`invalid_argument`) |
| `ReorderProjects(ids)` | `void` | ✅ |
| `SavedQueries(projectId)` | `SavedQuery[]` | ✅ `""` = all |
| `SaveQuery(q)` | `SavedQuery` | ✅ `id: ""` creates; `connId` optional |
| `DeleteSavedQuery(id)` | `void` | ✅ |
| `GetUIState(key)` | `string` | ✅ opaque JSON owned by the frontend, `""` if unset |
| `SetUIState(key, value)` | `void` | ✅ `""` deletes. Keys: `tabs:<projectId>`, `ui:activeProject`, `ui:prefs`, `colwidths:<connId>:<schema>.<table>`, `filters:<connId>:<schema>.<table>` |

### ConnectionService (`connectionApi`)

| Method | Returns | Notes / status |
|---|---|---|
| `Connections(projectId)` | `ConnectionConfig[]` | ✅ `""` = all projects |
| `GetConnection(id)` | `ConnectionConfig` | ✅ |
| `SaveConnection(cfg)` | `ConnectionConfig` | ✅ `id: ""` creates. `password`: omit/`undefined` = keep stored, `""` = clear, value = save in the secrets file. Same for `ssh.password`, `ssh.keyPassphrase`. Closes an open pool (emits `conn:status`). Validates driver, color, SQLite path |
| `DeleteConnection(id)` | `void` | ✅ also secrets + history. Close its tabs yourself (`tabs.closeConnection`) |
| `DuplicateConnection(id)` | `ConnectionConfig` | ✅ "name copy", secrets copied |
| `ReorderConnections(projectId, ids)` | `void` | ✅ also moves connections into `projectId` |
| `TestConnection(cfg)` | `TestResult {serverVersion, latencyMs, message}` | ✅ unsaved form state; `cfg.password` if set, else the stored one. Rejects with `db_error` + driver message |
| `Connect(id, password)` | `ConnectionStatus` | ✅ password only used for `ask` mode |
| `Disconnect(id)` | `void` | ✅ |
| `ConnectionStatuses()` | `ConnectionStatus[]` | ✅ one per saved connection |
| `UseDatabase(id, database)` | `ConnectionStatus` | ✅ switch database of an open connection (pg: reconnects to it; mysql: new default schema). Not persisted; console sessions of the connection are closed, caches dropped; emits `conn:status`. SQLite → `invalid_argument` |
| `TestSSH(cfg)` | `SSHTestResult {ok, authMethod, user, address, proxyJump, serverVersion, hostKeyFingerprint, tried: [{method, key, result}], dbReachable, dbEndpoint, dbError?, durationMs}` | ✅ tests only the tunnel of an (unsaved) form config: SSH handshake + login with the same auth chain as Connect, then a direct channel to the database `host:port` / socket from the SSH host, closed at once (no database login). Login problems reject (`ssh_error` with the tried list, `host_key_unknown`, `host_key_mismatch`, `passphrase_required`); an unreachable database endpoint resolves with `dbReachable: false` + `dbError`. `authMethod` e.g. `"agent: ED25519 SHA256:…"`, `"key: ~/.ssh/id_ed25519"`, `"password"` |
| `ProvideKeyPassphrase(keyPath, passphrase, remember)` | `void` | ✅ after `passphrase_required` / `ssh:passphrase`: checked against the key file (`invalid_argument` "wrong passphrase…"), kept for this app session; `remember: true` also saves it in the secrets file (opt-in checkbox "Remember passphrase"). Then retry the call that failed |
| `TrustHostKey(host, fingerprint)` | `void` | ✅ accept an unknown SSH host key (from the `host_key_unknown` error / `ssh:hostkey` event); stored in the app's own `known_hosts` (`~/.ssh/known_hosts` is read, never written). Retry Connect/Test afterwards |
| `ParseConnectionString(text)` | `EnvImportResult {config, found, warnings, sourcePath, cancelled}` | ✅ one paste box for everything: URLs (`mysql://`, `mariadb://`, `postgres://`, `postgresql://`, `pgsql://`, `sqlite://`, `sqlite:///abs`, `file:`; user/password URL-decoded, IPv6 `[::1]`, params `sslmode`/`ssl-mode`/`tls`, `socket`/`unix_socket`, pg `host=/socket/dir`, `schema`/`search_path`; `charset` ignored with a warning, `application_name` ignored), JDBC (`jdbc:mysql:`, `jdbc:mariadb:`, `jdbc:postgresql:` with `user=`/`password=`/`currentSchema=`, `jdbc:sqlite:/path`), Go MySQL DSN (`user:pass@tcp(host:3306)/db?…`, `user@unix(/path.sock)/db`), libpq `host=… port=… dbname=… user=… password='…' sslmode=…`, and `.env` lines/blobs (delegated to `ParseEnv`). Surrounding whitespace/quotes are trimmed. `found.format` = `url` / `jdbc` / `mysql-dsn` / `libpq` / `env`; other `found` keys (driver, host, port, socket, user, password `••••`, database, file, sslMode, schema) summarize what was recognized. Passwords with unescaped `@`, `/`, `#` work (split on the last `@`). Unrecognized input rejects with `invalid_argument` and a message listing the accepted formats. Never saved; the password is never in warnings. Helper: `parseConnectionString(text)` in `@/api` |
| `ParseEnv(text)` | `EnvImportResult {config, found, warnings, sourcePath, cancelled}` | ✅ Laravel `.env` text → prefilled config (not saved). `APP_ENV=production` → red + read-only + warning |
| `ImportEnvFile(path)` | `EnvImportResult` | ✅ `""` → native file dialog (desktop; browser mode rejects with `unsupported`); cancelled dialog → `{cancelled: true}` |
| `OpenSQLiteFileDialog()` | `string` | ✅ native dialog (desktop; browser mode rejects with `unsupported`); `""` when cancelled |

`ConnectionConfig` fields: `id, projectId, name, driver ("postgres"|"mysql"|"sqlite"),
host, port (0 = default), socket (mysql: socket file; postgres: socket dir; wins over
host), user, passwordMode ("save"|"ask"), password? (write-only), hasPassword
(read-only), database, filePath (sqlite), defaultSchema, sslMode
(""|"disable"|"prefer"|"require"|"verify-ca"|"verify-full"), params {k:v},
ssh {enabled, host, port, user, authMethod ("password"|"key"|"agent"), keyPath,
password?, keyPassphrase?, hasPassword, hasKeyPassphrase}, color
("none"|"green"|"yellow"|"orange"|"red"|"blue"|"purple"), readOnly, sortOrder,
createdAt, updatedAt`. Use `projects.blankConnection(driver)` for a new form.
SSH tunnels (postgres/mysql, also to a remote Unix socket). `ssh.authMethod`:
- **`"auto"`** (default; label it **"Default (agent + ~/.ssh)"**; stored `"agent"` and `""`
  mean the same and are normalized to `"auto"` on save). Works like OpenSSH: offers, in
  order and de-duplicated, the agent's keys (agent from `IdentityAgent` in ~/.ssh/config,
  else `SSH_AUTH_SOCK`, else launchd's `SSH_AUTH_SOCK`), the `IdentityFile`s from
  ~/.ssh/config, then ~/.ssh/id_ed25519, id_ecdsa, id_ecdsa_sk, id_ed25519_sk, id_rsa
  (unless `IdentitiesOnly yes`). An encrypted key file whose public key the agent already
  holds is used through the agent; otherwise, when the server accepts it, the call
  rejects with `passphrase_required` and `ssh:passphrase` fires (see
  `ProvideKeyPassphrase`).
- `"key"`: only `ssh.keyPath` (`~` allowed); passphrase from `ssh.keyPassphrase`, the
  stored secret or `ProvideKeyPassphrase`; missing → `passphrase_required`.
- `"password"`: `ssh.password`.

`ssh.host` may be a **~/.ssh/config Host alias**: HostName, User, Port, IdentityFile,
IdentitiesOnly, IdentityAgent and ProxyJump (one hop; more hops → `ssh_error` "not
supported") are applied, with `Host` wildcards/negation and `Include`. Form fields win;
`ssh.port: 0` = config Port, else 22; empty `ssh.user` = config User, else the OS user
(so the form can leave port and user empty). Auth failures are `ssh_error` with what was
tried, e.g. "SSH authentication failed for app@host:22. Tried agent (0 keys),
~/.ssh/id_ed25519 (rejected), ~/.ssh/id_rsa (encrypted, no passphrase)." plus, when the
agent is empty, the hint `ssh-add --apple-use-keychain ~/.ssh/id_ed25519`. No key
material is ever included. Host names in host/socket are resolved on the SSH server
side. Unknown host keys: see `TrustHostKey`.

### SchemaService (`schemaApi`)

| Method | Returns | Notes / status |
|---|---|---|
| `Databases(connId)` | `string[]` | ✅ |
| `Schemas(connId)` | `SchemaInfo[] {name, isDefault, isSystem}` | ✅ mysql: databases; pg: schemas; sqlite: main (+ attached) |
| `Tables(connId, schema, refresh)` | `Table[] {schema, name, kind, estRows, sizeBytes, comment}` | ✅ cached; `kind`: table/view/materialized_view; `estRows`/`sizeBytes` -1 = unknown |
| `Columns(connId, schema, table)` | `Column[]` | ✅ `{name, ordinal, type, dataType, kind, nullable, default?, pk, pkOrdinal, autoIncrement, generated, comment, fk?: {schema, table, column}}` |
| `SearchSchema(connId, schema, refresh)` | `SchemaIndex {connId, schema, builtAt, tables: [{name, kind, columns[], types[]}]}` | ✅ every table/view with column names, for speed search, Cmd+K and autocomplete (~270 tables in the dummy DBs). Cached; `refresh` reloads |
| `Indexes(connId, schema, table)` | `Index[] {name, columns, unique, primary, method, where}` | ✅ primary first; expression parts appear as `(expr)` / `<expression>`; sqlite rowid PKs appear as a synthetic `PRIMARY` |
| `ForeignKeys(connId, schema, table)` | `ForeignKey[] {name, schema, table, columns, refSchema, refTable, refColumns, onUpdate, onDelete}` | ✅ outgoing (multi-column aware). SQLite FKs have synthetic names `<table>_fk_<n>`; `refColumns` entries may be `""` = the referenced table's PK |
| `ReferencedBy(connId, schema, table)` | `ForeignKey[]` | ✅ incoming FKs (other tables pointing here; `table` = the referencing table) |
| `TableStructure(connId, schema, table)` | `TableStructure {table, columns, indexes, foreignKeys, referencedBy, triggers, ddl}` | ✅ one call for the Structure tab; parts that cannot be read come back empty, a failing DDL as `-- DDL not available: ...` |
| `DDL(connId, schema, table)` | `string` | ✅ all engines (pg reconstructed from the catalog: columns, identity/generated, constraints, indexes, comments, triggers; views as CREATE [MATERIALIZED] VIEW) |
| `SearchObjects(projectId, query, limit)` | `SearchHit[] {connId, schema, table, column, kind, savedId, label, score, positions}` | ✅ fzf-style ranking over tables/views, columns (`label` "table · column") and saved queries of the project; only **connected** connections are searched (no implicit connects). `positions` are JS string indexes into `label` for highlighting. limit default 50 |
| `PreviewStructureChanges(change)` | `string[]` | ✅ the exact DDL `ApplyStructureChanges` runs; order: drop indexes, drop columns, rename columns, add columns |
| `ApplyStructureChanges(change)` | `ApplyResult` | ✅ pg/sqlite: one transaction. **MySQL DDL auto-commits**: statements run one by one, stop at the first failure (`error` says how many were applied). Refreshes the metadata cache |

`StructureChange`: `{connId, schema, table, dropColumns: string[], renameColumns: {old: new},
dropIndexes: string[], addColumns: ColumnDef[]}` with `ColumnDef {name, type, nullable, default?,
after}` (`type` as typed, e.g. `varchar(64)`, `numeric(12,2)`, `enum('a','b')`; `default` is a raw
SQL expression like `'abc'`, `0`, `CURRENT_TIMESTAMP`; `after` = MySQL column position).
Types/defaults are validated (no `;`, comments or unbalanced quotes) since DDL cannot be parameterized.

### DataService (`dataApi`)

| Method | Returns | Notes / status |
|---|---|---|
| `FetchPage(req: PageRequest)` | `QueryResult` | ✅ see below |
| `CountRows(req: PageRequest)` | `number` | ✅ where + columnFilters, limit/offset ignored |
| `CellValue(req: CellRequest {connId, schema, table, pk, column})` | `CellValue` | ✅ full untruncated value (text as string, binary as `{$type:"binary", hex}` with all bytes) |
| `PreviewChanges(cs: ChangeSet)` | `string[]` | ✅ SQL for display with literals inlined and quoted per dialect, order: deletes, updates, inserts (same order/count as `ApplyResult.statements`) |
| `ApplyChanges(cs: ChangeSet)` | `ApplyResult {ok, statements, executed, failedIndex, error, rowsAffected, durationMs, insertedPks}` | ✅ one transaction, all values bound as parameters. Every UPDATE/DELETE must hit **exactly one row** by PK, otherwise everything is rolled back (`error`: "UPDATE matched no row ..."). Failure → `ok:false`, `failedIndex` into `statements`. `insertedPks[i]` = PK of the i-th insert (auto-increment / RETURNING) |
| `CopyAsSQL(req: CopySQLRequest {connId, schema, table, kind: "insert"\|"update", columns, rows, pkCols})` | `string` | ✅ one statement per row, `;`-terminated, literals per dialect. `update`: `pkCols` default = the table's PK (`no_primary_key` if none); PK columns must be among `columns`. Unknown table names are fine (console results) |
| `Export(req: ExportRequest)` | `ExportResult {path, rows, bytes, durationMs, cancelled}` | ✅ streamed to `<path>.part`, renamed when done. `format`: csv / tsv / json / sql / markdown. Source: `page` (where/orderBy/columnFilters apply, limit/offset ignored) or `sql` (exactly one SELECT). `columns` = subset/order. `path` must be absolute; `""` → save dialog (desktop; browser → `unsupported`). Cancel by cancelling the promise. NULL → empty (csv/tsv), `null` (json), `NULL` (sql/markdown); binary → `0x…`; json columns embedded as JSON; decimals as exact JSON numbers |
| `FindInDatabase(req: FindRequest)` | `FindResult[] {schema, table, matchedColumns, result}` | ✅ searches text-like columns (string, text, json, uuid, other) of `tables` (all tables/views when empty); `mode` contains / exact / starts_with; case-insensitive unless `caseSensitive`; `limitPerTable` default 50 (max 1000; `result.hasMore` when cut). Emits `find:progress` per table; cancel with `queryApi.CancelQuery(searchId)` |

`PageRequest`: `{connId, schema, table, where (raw SQL, no WHERE keyword), orderBy (raw
SQL, no ORDER BY), limit (default 500, max 10000), offset, columnFilters: [{column, op,
value, values}], countTotal}`. Filter ops: `eq ne gt gte lt lte contains not_contains
starts_with ends_with like not_like is_null not_null in not_in between` (`in`/`not_in`
use `values`, `between` uses `values[0..1]`; values are bound as parameters;
`contains` etc. are case-insensitive). Without `orderBy` the rows are ordered by the
primary key. A bad `where` rejects with `db_error` (show it under the WHERE input).

### QueryService (`queryApi`)

| Method | Returns | Notes / status |
|---|---|---|
| `RunQuery(req: QueryRequest {connId, queryId, sql, schema, maxRows, sessionId})` | `QueryResult[]` | ✅ one result per executed statement, stops at the first failing one (its `error` is set; the call still resolves). `queryId` = `newId()`; `maxRows` default 1000 (`hasMore`/`truncated` when cut). Every statement is added to history. **Pass `sessionId` = console tab id**: runs with the same id share one DB session, so `USE`, `SET`, temp tables and `BEGIN … COMMIT/ROLLBACK` carry over between runs; runs of one session are serialized. Without a sessionId each run gets a throw-away session (an open transaction is rolled back). Idle sessions are closed after 30 min |
| `CancelQuery(queryId)` | `boolean` | ✅ false if not running. The running statement's result gets `error: "canceled: ..."`. MySQL uses `KILL QUERY` and Postgres a cancel request, so the session (and an open transaction) survives |
| `CloseSession(sessionId)` | `void` | ✅ call when a console tab closes; an open transaction is rolled back |
| `SplitStatements(driver, sql)` | `StatementRange[] {start, end, sql}` | ✅ JS string offsets; handles quotes, comments, `$$` (pg), backticks (mysql). Use for "run statement at cursor" |
| `History(connId, search, limit)` | `QueryHistory[] {id, connId, sql, ranAt, durationMs, rowCount, error}` | ✅ newest first; `""` connId = all |
| `ClearHistory(connId)` | `void` | ✅ |

EXPLAIN: run `"EXPLAIN " + statement` through `RunQuery`.

## 5. Results and value encoding

`QueryResult`: `{queryId, index, statement, columns: ResultColumn[], rows: CellValue[][],
hasResultSet, rowsAffected (-1 n/a), total (-1 unknown), offset, hasMore, truncated,
durationMs, error, source?: {connId, schema, table}}`.

- `ResultColumn`: `{name, type, kind, nullable, pk, fk?: {schema, table, column}}`.
  For `FetchPage` these come from table metadata (full types like `decimal(20,4)`,
  pk/fk set). For `RunQuery` they come from the driver (`type` = driver type name,
  no pk/fk).
- `source` is set for `FetchPage` (the editable table). Console results are read-only.
- `total`: FetchPage sets it when `countTotal` was requested or the last page was
  reached; otherwise -1. `estimatedTotal`: without where/filters, the statistics-based
  row estimate (cheap, may be stale; -1 unknown) — show "1–500 of ~2,000" and call
  `CountRows` on demand for the exact number.
- `hasMore`: there are rows after this page.

**Cell values (`CellValue`)** — the UI must distinguish all of these:

| JSON | Meaning | Render |
|---|---|---|
| `null` | SQL NULL | dim italic `NULL` |
| `""` | empty string | empty cell |
| `string` | text, varchar, **decimal/numeric** (exact, e.g. `"545.0000"`), date `"2025-01-31"`, datetime `"2025-01-31 13:45:00"` (+`"+02:00"` for timestamptz), time, json (raw JSON text), uuid, enum, pg arrays (text form), integers beyond ±2^53 | as-is (right-align numeric kinds) |
| `number` | ints within ±2^53, floats | right-aligned |
| `boolean` | bool (pg `boolean`, mysql `tinyint(1)`, sqlite `BOOLEAN`) | `true`/`false` |
| `{"$type":"binary","hex","size","truncated"}` | binary/blob (first 256 bytes as hex) | e.g. `0x83cd53…` dim + size |
| `{"$type":"text","text","size","truncated":true}` | text longer than 8 KB, cut | show `text` + "…"; load the full value with `CellValue` |

Use `column.kind` (`int float decimal bool string text json date time datetime binary
uuid other`) for alignment, editors and copy formats, not the JS type.
Helpers: `isBinary(v)`, `isLongText(v)`.

**Values sent to the backend** (ChangeSet `pk`, `set`, `values`, CopySQLRequest rows)
use the same encoding, plus `{"$type":"default"}` (`SQL_DEFAULT`) for the DEFAULT
keyword and `{"$type":"binary","hex":"..."}` for binary input. Strings are bound as
parameters and cast by the database, so send what the user typed (`"12.50"`,
`"2025-01-31 10:00:00"`). Row identity for updates/deletes = the row's PK column
values exactly as received.

## 6. Store APIs (Pinia)

Shell-owned stores; the data agent may read and call them, not change them.

**`useProjectsStore()`** (`stores/projects.ts`)
- state: `projects: Project[]`, `connections: ConnectionConfig[]` (all projects),
  `statuses: Record<connId, ConnectionStatus>`, `activeProjectId`, `loaded`
- getters: `activeProject`, `projectConnections`
- `getConnection(id)`, `isReadOnly(id)`, `isConnected(id)`,
  `connColor(id)` → CSS value like `var(--conn-red)` (use as `style="--conn: …"`)
- actions: `init()`, `setActiveProject(id)`, `saveProject(p)`, `deleteProject(id)`,
  `saveConnection(cfg)`, `deleteConnection(id)`, `duplicateConnection(id)`,
  `connect(id, password?)`, `disconnect(id)`, `testConnection(cfg)`, `blankConnection(driver)`

**`useTabsStore()`** (`stores/tabs.ts`)
- state: `tabs: Tab[]`, `activeId`; getter `active`
- `openTable(connId, schema, table, {where?, orderBy?})` (focuses an existing tab and
  applies where/orderBy), `openStructure(connId, schema, table)`,
  `openConsole(connId, sql?, schema?)`, `open(tab)`, `activate(id)`,
  `close(id): Promise<boolean>` (confirms when `dirty`), `closeOthers(id)`,
  `closeConnection(connId)`, `setDirty(id, bool)`, `update(id, patch)`, `cycle(±1)`,
  `save(projectId)`, `restore(projectId)`
- `Tab` (`@/types`): `{id, kind: "table"|"console"|"structure", connId, schema?, table?,
  title, where?, orderBy?, sql?, dirty?, pinned?}`

**`useUiStore()`** (`stores/ui.ts`)
- `theme` (`"dark"|"light"|"system"`), `effectiveTheme`, `setTheme(t)`
- `toast(message, kind = "info", timeout?)` — kinds `info|success|warning|error`;
  `toasts`, `dismissToast(id)`
- `paletteOpen`, `openPalette()`, `closePalette()`, `togglePalette()`
- `modal: {name, props} | null`, `openModal(name, props)`, `closeModal()`;
  names: `connection`, `password`, `saveChanges`, `export`, `findInDatabase`, `confirm`
- `confirm({title, message, sql?, confirmText?, danger?}): Promise<boolean>`
  (rendered by the shell; the data agent uses it e.g. for the red-connection save confirmation)
- `sidebarWidth`, `sidebarVisible`

**`useChangesStore()`** (`stores/changes.ts`, data-owned): staged edits per table tab
(`byTab[tabId]`), `count(tabId)`, `hasChanges(tabId)` are used by the shell
(tab badge, status bar "N pending changes"). The data agent keeps
`tabs.setDirty(tabId, hasChanges)` in sync.

## 7. View contract

- `App.vue` mounts every open tab (kept alive with `v-show`) as
  `<TableView|SqlConsole|StructureView :tab="tab" :active="isActive" />`.
  Views must only react to keyboard shortcuts while `active` is true and must cancel
  in-flight requests on unmount.
- `TableView` owns its toolbar (refresh, paging, WHERE, ORDER BY, quick find, add/delete
  row, revert, Save, Copy as, Export, record panel) and renders `DataGrid`.
- `SqlConsole` renders CodeMirror + results via `DataGrid` (read-only).
- The connection context bar (name, database, READ-ONLY/PRODUCTION pill, color tint)
  sits above the active view and is rendered by the shell (TabBar area).

## 8. Read-only and color

- `ConnectionConfig.readOnly` (also `ConnectionStatus.readOnly`, `projects.isReadOnly(id)`).
- UI layer (data agent): in read-only connections disable editing, paste, add/delete
  row and Save; any edit attempt shows `ui.toast("Connection is read-only", "warning")`.
  Tables without PK (`columns.some(c => c.pk)` false) are read-only in the grid with a
  hint. Console results are always read-only.
- Backend service layer: `ApplyChanges`/`ApplyStructureChanges` reject with `read_only`.
  Console statements are tokenized per dialect (comments, all quote styles, `E''`,
  `$$`, backticks, MySQL `/*! */` treated as code) and classified by their tokens.
  Allowed on read-only connections: `SELECT`, `WITH … SELECT`, `SHOW`, `DESCRIBE`/`DESC`,
  `EXPLAIN` (not `ANALYZE`), `VALUES`, `TABLE`, and read PRAGMAs on SQLite. Refused:
  anything else (`USE`, `SET`, `BEGIN`/`COMMIT`, …), and any statement that contains a
  DML/DDL/transaction/`SET`/`RESET`/`CALL`/`DO`/`COPY`/`LOAD`/`HANDLER`/`LOCK`/`INTO`
  keyword anywhere, or calls a state-changing function (`set_config`, `nextval`,
  `pg_advisory_*`, `lo_*`, `dblink*`, `pg_terminate_backend`, `query_to_xml`, `GET_LOCK`,
  `LOAD_FILE`, `load_extension`, …). Refusals come back as a statement result with
  `error: "read_only: connection is read-only: <reason>"`. **Columns named like one of
  those keywords (`set`, `lock`, `load`, `copy`, `call`, `do`, `into` …) must be quoted
  in read-only consoles.** WHERE/ORDER BY input is checked the same way (and `;` is refused).
- On top of that every read on a read-only connection (console statement, page, count,
  find, cell value, export) runs inside `BEGIN READ ONLY` / `START TRANSACTION READ ONLY`
  that the app opens and always rolls back. So user transaction control is not
  available in read-only consoles, and nothing a statement does can outlive it.
- Database session: read-only too (pg `default_transaction_read_only`, MySQL/MariaDB
  `SET SESSION TRANSACTION READ ONLY` on every new session, sqlite `mode=ro` + `query_only`).
- Export never writes, on any connection: a SQL source must classify as a read
  statement that returns rows, and runs in a read-only transaction.
- Color tints (shell): tab top border + dot, context bar background (14% mix) and
  bottom border, status bar. Use `--conn-<color>` tokens from `styles/theme.css`.
- Choosing red in the connection form suggests enabling read-only. Saving changes on
  a red connection that is *not* read-only asks for an extra `ui.confirm` with the SQL.

## 9. Installed frontend packages

vue 3, pinia, @wailsio/runtime 3.1.0, tailwindcss 4 (+ @tailwindcss/vite, theme tokens
exposed as `bg-bg2`, `text-dim`, `border-bd`, `text-danger`, `font-mono`...),
@tanstack/vue-virtual, codemirror, @codemirror/{state,view,commands,language,search,
autocomplete,lang-sql,theme-one-dark}, @lezer/highlight, fzf (fuzzy matching).
No icon library: use small inline SVG components.

## 10. Keyboard shortcuts

| Key | Action | Handled by |
|---|---|---|
| Cmd+K | Command palette | shell (App) |
| Cmd+T | New SQL console for the active tab's connection | shell |
| Cmd+W | Close tab | shell |
| Ctrl+Tab / Ctrl+Shift+Tab, Cmd+1..9 | Next/prev/nth tab | shell |
| Cmd+Shift+F | Find in database | shell (opens modal) |
| Esc | Close palette/modal/menu; clear sidebar speed search | shell |
| typing in sidebar tree | speed search | shell |
| Cmd+S | Save staged changes (preview → confirm → apply) | data (TableView) |
| Cmd+Z | Undo last staged change | data |
| Cmd+R / F5 | Refresh | data |
| Cmd+F | Quick find in loaded rows | data |
| Cmd+Enter | Run statement at cursor / selection | data (SqlConsole) |
| Enter / F2 | Edit cell; Tab moves to next cell; Esc cancels | data (grid) |
| Cmd+Backspace | Mark selected row(s) for delete | data |
| Cmd+N | Add row | data |
| Cmd+C / Cmd+Shift+C | Copy as TSV / with headers | data |
| Cmd+V | Paste TSV/CSV into selection (staged) | data |
| Cmd+A | Select all cells | data |

Shell handlers must ignore keys while focus is in an input/CodeMirror unless the
combo is global (Cmd+K, Cmd+W, Cmd+T). Views stop propagation for keys they handle.

**Desktop menu (macOS).** The app has a standard menu: DB Viewer (About, Hide, Quit…),
Edit (Undo, Redo, Cut, Copy, Paste, Paste and Match Style, Delete, Select All —
native roles, so Cmd+C/V/X/A/Z work in every input and in CodeMirror), View (Reload and
Developer Tools in dev builds only; Toggle Full Screen) and Window. Key events reach
the web page first; when the page handles a combo it **must call
`event.preventDefault()`**, otherwise the menu also acts on it (e.g. Cmd+Z in the grid
without preventDefault would additionally send `undo:` to the focused element; in a
dev build an unhandled Cmd+R reloads the page). There are no menu items for Cmd+S,
Cmd+K, Cmd+T, Cmd+W, Cmd+F or Cmd+N, so those always reach the page.

## 11. Look

Follow the mockup (dense developer tool, 12px UI font, monospace data, 22px rows,
dark default + light). Tokens are in `styles/theme.css` (`--bg --bg2 --bg3 --bd --tx
--dim --ac --hov --selbg --selbd --chg-edit --chg-new --chg-del --mark --danger --warn
--pk --fk --syn-*`, layout sizes `--titlebar-h --sidebar-w --tabbar-h --row-h ...`) and
base classes in `styles/main.css` (`.btn .btn.ghost .btn.primary .input .badge .pill
.null .mono .dim .drag .no-drag .conn-dot .conn-tint-bg`).

macOS window: hidden-inset title bar. The traffic lights overlay the left
`--traffic-w` (78px) of our own 38px title bar; the title bar has class `drag`
(`--wails-draggable: drag`), interactive elements inside it need `no-drag`.

## 12. Frontend notes (data views)

Clipboard / paste format used by the grid (data agent):

- **Copy (Cmd+C, "Copy as TSV")** writes tab-separated text. A NULL cell is written as the token `\N`, a DEFAULT cell (new
  rows) as `\D`. A real string that equals `\N` or `\D` is written quoted (`"\N"`). Fields with tabs, quotes or newlines are
  quoted with doubled quotes (Sheets-compatible).
- **Paste** parses TSV or CSV (tab wins, then comma). An unquoted `\N` becomes SQL NULL (nullable columns), `\D` becomes
  DEFAULT, everything else is text. The literal text `NULL` is a plain string. Empty fields become NULL only for nullable
  non-text columns (int, date, ...), otherwise the empty string.
- **CSV export / "Copy as CSV"** writes NULL as an empty field (spreadsheet friendly; not round-trip safe).
- Truncated values (`{$type:"text"|"binary", truncated:true}`) are never copied, exported (current page / selection) or staged
  as cut text: the grid loads the full value with `CellValue` first, or refuses with a toast.
- Binary columns are read-only in the grid, record view and paste.
- Header click sorts (Shift = secondary sort); Alt-click or the header context menu selects the column. Delete/Backspace sets
  NULL only for a single nullable cell; larger selections use the context menu "Set NULL".

## 13. Backend changes after the audit (phase C)

Behaviour the frontend can rely on (all signatures unchanged; two additive fields):

- `StructureChange.expectedStatements?: string[]` (new, optional): send back the
  statements `PreviewStructureChanges` returned. `ApplyStructureChanges` then refuses
  with `invalid_argument` ("the table changed since the preview…") when the regenerated
  DDL differs, so what runs is always what the user confirmed.
- `Index.constraint` (new, Postgres): the index backs a PRIMARY KEY/UNIQUE/EXCLUDE
  constraint; dropping it emits `ALTER TABLE … DROP CONSTRAINT`, other indexes get a
  plain `DROP INDEX` (a CHECK constraint with the same name is no longer dropped).
- Console sessions: `QueryRequest.schema` is applied **once**, when the session's
  connection is created. `USE`/`SET search_path` typed in the console then stick for
  the following runs, although the console sends the tab schema every time.
- Cancel: a statement that completed before the cancel took effect keeps its result
  (a committed UPDATE is never reported as "canceled"); failed and row-returning
  statements hit by the cancel report `canceled: …`. Statements after it do not start.
- Disconnect / SaveConnection / UseDatabase / DeleteConnection cancel running queries of
  that connection first; they no longer wait for a long query.
- Console `maxRows` is capped at 50,000. When a result has more rows the statement is
  stopped on the server (KILL QUERY / cancel request), so `maxRows: 1000` on a huge
  result returns quickly. `durationMs` includes closing the result.
- Pool: 24 connections per open connection; every console tab keeps one. If all are
  busy, RunQuery fails after 15 s with "all connections of this connection are busy;
  close some console tabs…". Call `CloseSession` when a console tab closes.
- Statement splitting/classification uses the tokenizer: MySQL `5--1`, Postgres
  `E'it\'s; x'`, `$$` bodies, SQLite trigger bodies split correctly; `RETURNING` inside a
  string no longer turns an UPDATE into a result set; `ANALYZE TABLE`, `CHECK TABLE`,
  `FETCH`, `EXECUTE`, `CALL` return their rows.
- WHERE / ORDER BY input may end with a `-- comment`. A user ORDER BY gets the primary
  key appended as tie-breaker (no PK: `ctid` on Postgres tables, `rowid` on SQLite
  tables, all columns on MySQL), so paging is stable.
- Value encoding: boolean-ish columns (`tinyint(1)`, SQLite `BOOLEAN`) are `true`/`false`
  only for 0/1, other values stay numbers (2, -1); MySQL `bit(n)` with n > 1 is a binary
  cell, `bit(1)` a boolean; Postgres `bit`/`varbit` are strings like `"1010"`; Postgres BC
  dates are `"0044-03-15 BC"` (writable back); SQLite DATE/DATETIME/TIMESTAMP text is
  passed through exactly as stored (e.g. `"2025-01-31T10:00:00Z"`) in table pages, find
  and cell values. (Console results of SQLite date columns are still reformatted by the
  driver; select `CAST(col AS TEXT)` to see the stored text.)
- Postgres edits no longer truncate/pad: a too-long `varchar(n)`/`char(n)`/`bit(n)` value
  fails with the database error, as a hand-written UPDATE would.
- Connection config: `sslMode` must be one of "", disable, prefer, require, verify-ca,
  verify-full (else `invalid_argument`); `passwordMode` "save"/"ask". Connect errors never
  contain the password.
- `.env` import: `DATABASE_URL`/`DB_URL` win over the individual `DB_*` keys (a warning
  lists the ignored keys); the URL password is masked in `found`; `null`/`(null)`/`empty`
  values count as unset; BOM, quoted values with `#`, `export\t`, `sqlite:relative/path`
  URLs and `:memory:` (warning, no path) are handled; undefined `${VAR}` gives a warning.
- SSH: the client offers only the host key types known_hosts has for the host (a host
  known by its ed25519 key no longer fails as "changed"); a lost tunnel drops the pool
  (`conn:status` with `error: "ssh_error: SSH tunnel lost"`), the next call reconnects.
- History keeps the newest 5,000 statements per connection; password literals in
  `IDENTIFIED BY '…'` / `PASSWORD '…'` are stored as `'***'`.
- SQLite: paths with `#`, `?`, `%` open the right file (and stay read-only); one broken
  view (dropped base table) no longer breaks the whole schema (it is listed without
  columns).
- Window: default 1400×900, minimum 960×600; size, position and maximised state are
  restored (stored in UI state key `ui:window`, centered again if the screen is gone).

## 14. Secrets storage (no Keychain)

Saved passwords, SSH passwords and remembered key passphrases are stored **locally in
`secrets.json` in the app's data folder** (`~/Library/Application Support/dbviewer`, or
`$DBVIEWER_DATA_DIR`): the folder is mode 0700, the file 0600 (only the user can read it),
written atomically (temp file + rename). The macOS Keychain is not used at all, so an
ad-hoc signed build never shows Keychain prompts. `DBVIEWER_SECRETS` is gone (ignored).

There is no migration: the Keychain is never read. `hasPassword`, `ssh.hasPassword` and
`ssh.hasKeyPassphrase` always reflect whether the secret exists in `secrets.json`
(computed in `Connections`/`GetConnection`, stale stored flags are fixed), so a password
that was in the Keychain shows as "no saved password" and the form field is empty.

Connecting a `passwordMode: "save"` connection without a saved password: if the server
rejects the empty password, `Connect` (and lazy connects) reject with
`password_required` ("the password of … is not saved; enter it (it will be saved for
next time)"). The UI asks (PasswordModal: "It will be saved for next time.") and calls
`Connect(id, password)`; **after a successful connect the password is saved** in
`secrets.json` and `hasPassword` becomes true. A wrong password is never saved; "ask"
connections never save. Likewise an SSH key passphrase entered via
`ProvideKeyPassphrase` for a "save" connection is saved once its connect succeeds
(key mode: the connection's passphrase; auto mode: per key file).
UI labels: "Save password" (hint "Stored locally in the app's data folder") and
"Remember passphrase".
