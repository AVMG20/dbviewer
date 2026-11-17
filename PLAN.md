# DB Viewer — plan

Desktop database viewer for PostgreSQL, MySQL/MariaDB and SQLite. Go + Wails + Vue 3.
Goal: a fast, keyboard-first tool in the spirit of DataGrip/TablePlus, with safety rails for production.

## Stack

| Layer | Choice | Why |
|---|---|---|
| Shell | Wails v3 (v3.1.0) | Native window, services with generated TS bindings, native dialogs, typed events, server mode for browser dev |
| Postgres | `jackc/pgx/v5` (stdlib) | Best PG driver, proper type info |
| MySQL | `go-sql-driver/mysql` | Standard |
| SQLite | `modernc.org/sqlite` | Pure Go, no CGO, easy cross-compile |
| SSH tunnel | `golang.org/x/crypto/ssh` | Bastion/remote DBs |
| Secrets | `secrets.json` in the app data dir (0600, dir 0700, atomic writes) | Passwords stored locally, readable only by the user, never in the config; no Keychain (an ad-hoc signed app would prompt on every access) |
| App state | SQLite file in `~/Library/Application Support/dbviewer/` | Projects, connections, history, saved queries |
| Frontend | Vue 3 + TS + Vite + Pinia + Tailwind | |
| Grid | Own virtualized grid on `@tanstack/vue-virtual` (rows + columns) | Full control over selection, inline edit, staging; handles 100k+ cells |
| SQL editor | CodeMirror 6 + `@codemirror/lang-sql` | Dialects + schema-aware autocomplete |
| Fuzzy search | `fzf`-style scorer in TS (or `fuse.js`) | Speed search, command palette |

## Data model (app state)

- **Project**: id, name, sort order.
- **Connection**: id, project_id, name, driver (`postgres|mysql|sqlite`), host, port, user, database / file path, ssl mode, ssh tunnel settings, **color** (`none|green|yellow|orange|red|blue|purple`), **read_only** (bool), default schema; the password lives in the local secrets file.
- **QueryHistory**: connection_id, sql, ran_at, duration, row count.
- **SavedQuery**: connection_id / project_id, name, sql.
- **UI state**: open tabs per project, column widths, last filters per table.

## Go backend

```
internal/
  driver/        Driver interface + postgres.go, mysql.go, sqlite.go
  conn/          pool manager, ssh tunnels, read-only enforcement
  meta/          schema cache (tables, columns, indexes, FKs) + search index
  query/         paged select builder, WHERE/ORDER BY passthrough, count
  changes/       ChangeSet → SQL (UPDATE/INSERT/DELETE), applied in one transaction
  export/        CSV, TSV, JSON, SQL INSERT, Markdown
  store/         app-state SQLite + local secrets file
  envimport/     Laravel .env / DATABASE_URL parsing
  model/         all types that cross the Wails boundary + error codes
  services/      Wails v3 services (Project, Connection, Schema, Data, Query), thin
main.go          app + window setup, registers the services
```

`Driver` interface (sketch):

```go
type Driver interface {
    Open(ctx context.Context, cfg ConnConfig) (*sql.DB, error)
    Databases(ctx) ([]string, error)
    Schemas(ctx, db string) ([]string, error)
    Tables(ctx, schema string) ([]Table, error)          // name, type (table/view), est. rows
    Columns(ctx, schema string) (map[string][]Column, error) // bulk, for search index
    Indexes(ctx, table TableRef) ([]Index, error)
    ForeignKeys(ctx, table TableRef) ([]ForeignKey, error)
    DDL(ctx, table TableRef) (string, error)
    QuoteIdent(string) string
    Placeholder(n int) string                             // $1 vs ?
    SetReadOnly(ctx, conn *sql.Conn) error
}
```

Results go to the frontend as `{columns: [{name,type,nullable,pk,fk}], rows: [][]any, total, durationMs}`. Values are serialized so the UI can tell `NULL` from `""`, and binary/large values are truncated with a "load full value" call.

## Connection options

- MySQL: TCP host/port **or Unix socket** (`/opt/workbench/data/run/mysqld/mysql84.sock`). Empty password allowed.
- Postgres: TCP or socket directory (`host=/var/run/postgresql`).
- SQLite: file path, open dialog.
- **Import from `.env`**: pick a Laravel-style `.env` (or paste it) and the form is filled from `DB_CONNECTION`, `DB_HOST`, `DB_PORT`, `DB_SOCKET`, `DB_DATABASE`, `DB_USERNAME`, `DB_PASSWORD` (commented lines ignored, `DB_CONNECTION=pgsql|mysql|mariadb|sqlite` mapped). Also accepts `DATABASE_URL`.
- Optional "save password" (local secrets file, owner-only) vs ask on connect.

## Read-only and production safety

Enforced in layers, so the UI cannot be the only guard:

1. **UI**: editing, paste, add/delete row and Save are disabled; editing attempts show "Connection is read-only".
2. **Backend**: `ApplyChanges` rejects any call for a read-only connection.
3. **Database session**:
   - Postgres: `SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY` (plus `default_transaction_read_only=on` in the connection string).
   - MySQL: `SET SESSION TRANSACTION READ ONLY`.
   - SQLite: open with `?mode=ro` and `PRAGMA query_only=ON`.
   This also blocks writes from the SQL console.

Color: the connection color tints the tab bar, the toolbar border and the status bar (like the green/red datasource colors in DataGrip). Choosing **red** suggests turning on read-only. Saving changes on a red connection that is not read-only asks for an extra confirmation, with the SQL shown.

## Editing (always staged, always explicit Save)

- Double-click / Enter / F2 edits a cell; Tab moves to the next cell. Set NULL, set DEFAULT, and a multi-line/JSON editor are available for long values.
- Add row, duplicate row, delete row(s). Deleted rows are struck through, new rows green, changed cells amber.
- Paste (Cmd+V) of TSV/CSV from the clipboard fills the selection or appends rows (staged).
- Nothing is written until **Save (Cmd+S)**. Save opens a preview of the generated SQL, then runs all of it in **one transaction**. If anything fails, everything rolls back and the failing statement is highlighted.
- **Revert** (Cmd+Z per change, or "Revert all") discards staged changes. Closing a tab with pending changes asks first.
- Updates/deletes use the primary key in the WHERE clause. Tables without a PK are read-only in the grid (Postgres `ctid` / SQLite `rowid` fallback later, behind a warning).
- Removing columns (DDL) goes through the Structure tab, also staged, with an `ALTER TABLE` preview before applying.

## Finding things

- **Sidebar speed search**: when the tree has focus, typing starts a filter (no input field needed). It fuzzy-matches table names and **column names**; a table that matches by column shows the matched column as a hint (`sales_order · customer_email`). Esc clears. Backed by the cached column index (bulk query on `information_schema` / `pg_catalog` / `pragma_table_info` on connect, refreshed on demand), so it stays fast on 976 tables.
- **Command palette (Cmd+K)**: jump to any table/view/saved query across all connections in the project.
- **Grid filtering**:
  - WHERE and ORDER BY inputs (raw SQL, autocomplete on column names), like DataGrip.
  - Per-column filter popover (=, !=, contains, starts with, is null, in list, between).
  - Right-click a cell → filter by this value / not this value / open referenced row (FK).
  - Quick find (Cmd+F) within loaded rows, highlights matches.
- **Find in database** (Cmd+Shift+F): searches a value across text columns of selected tables, with a row limit per table and cancel.

## Copy / paste / export

- Selection: click, shift-click range, Cmd-click multi, click header to select a column, Cmd+A.
- Cmd+C copies as **TSV** (pastes straight into Sheets/Excel/Numbers); Cmd+Shift+C copies with headers. "Copy as" menu: CSV, TSV, JSON, SQL INSERT, SQL UPDATE, Markdown, `IN (...)` list.
- Export table / query result to CSV, JSON or SQL (streams from Go, does not load everything in the UI).
- Import CSV into a table with column mapping (later phase).

## Other features

- SQL console per connection: multiple statements, run selection/current statement (Cmd+Enter), result tabs, EXPLAIN, history, saved queries, cancel running query.
- Structure tab: columns, indexes, FKs, triggers, DDL.
- Record view: form layout of one row; FK values clickable to open the referenced row.
- JSON / long-text cell viewer with pretty-print.
- Tabs remembered per project; multiple windows later.
- Light/dark theme following macOS.

## Keyboard map (initial)

| Key | Action |
|---|---|
| Cmd+K | Command palette |
| Cmd+S | Save staged changes |
| Cmd+Z | Undo last staged change |
| Cmd+R / F5 | Refresh |
| Cmd+F | Find in results |
| Cmd+Shift+F | Find in database |
| Cmd+Enter | Run statement |
| Cmd+T | New SQL console |
| Cmd+W | Close tab |
| Enter / F2 | Edit cell |
| Cmd+Backspace | Mark row(s) for delete |
| Cmd+N | Add row |

## Phases

1. **Skeleton**: Wails app, app-state store, projects + connections CRUD with color/read-only, saved passwords, connect/test for all three drivers.
2. **Browse**: sidebar tree, metadata cache, speed search (tables + columns), virtualized read-only grid with paging, WHERE/ORDER BY, sort, copy as TSV/CSV.
3. **Edit**: staged changes, inline edit, add/delete rows, paste, SQL preview + transactional Save, read-only enforcement in all three layers.
4. **Query**: SQL console with CodeMirror, autocomplete, history, saved queries, cancel.
5. **Polish**: structure tab, FK navigation, record view, command palette, find in database, export/import, SSH tunnels.

## Open points

- "Removing columns": the plan assumes you meant deleting **rows** in the grid (staged). Dropping table columns is in the Structure tab, also staged.
- Wails v2 vs v3: v3.1.0 is stable; the project uses v3 (see CONTRACT.md for the API and dev setup).
