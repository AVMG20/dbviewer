# Backend audit

Scope: `main.go`, `internal/**`, `testdata/seed`, read against PLAN.md and CONTRACT.md.
No source file was changed. Probes were run from a copy of the repo in the scratchpad
(`/private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/audit-be/repo/internal/services/audit*_test.go`)
against the dummy MySQL, the `dbviewer-pg` container and temporary copies of `dummy.sqlite`.
All `audit_*` scratch tables were dropped afterwards (checked).

`go vet ./...` is clean. `go test -race ./...` passes for the existing tests; the race detector does
fire on a cancel path the existing tests do not exercise (item 20).

VERIFIED = reproduced with a probe. SUSPECTED = found by reading, not reproduced.

Counts: 0 critical, 8 high, 13 medium, 12 low.

One side effect of the audit to know about: a read-only bypass probe ran `RESET PERSIST` once on the
local MySQL 8.4 server (it passed the read-only allow-list, see item 1). There was no
`mysqld-auto.cnf` in the data directory afterwards, so no persisted variables existed and nothing
was lost. The statement was removed from the probes.

---

## High

### 1. Read-only connections accept writes from the console (Postgres and MySQL) — VERIFIED
`internal/query/readonly.go:17-19, 64-66, 68-79, 80-83, 97-101`; `internal/query/run.go:237-255` (`stripLeadingComments`)

Both guard layers can be switched off with statements the allow-list lets through, after which a
write goes in under an allowed first keyword. Each of these changed rows on a connection saved
with `readOnly: true`:

| Engine | Script run in the console | Result |
|---|---|---|
| Postgres | `BEGIN READ  WRITE; WITH d AS (DELETE FROM audit_ro WHERE id = 1 RETURNING id) SELECT * FROM d; COMMIT` (two spaces; a tab or newline works too) | row deleted |
| Postgres | `SELECT set_config/**/('default_transaction_read_only','off',false); WITH d AS (DELETE ...) SELECT * FROM d` | row deleted |
| Postgres | `SELECT "set_config"('default_transaction_read_only','off',false); SELECT 1 AS x INTO audit_ro2` | table created |
| MySQL | `SET @a = 1, SESSION transaction_read_only = 0; WITH x AS (SELECT 1) DELETE FROM audit_ro WHERE id = 1` | row deleted |
| MySQL | `START TRANSACTION READ  WRITE; WITH x AS (SELECT 1) DELETE FROM audit_ro WHERE id = 2; COMMIT` | row deleted |
| MySQL | `SET sql_mode = '', @@session.transaction_read_only = 0; /*!80000 UPDATE audit_ro SET v = 'hacked' WHERE id IN ( */ SELECT 3 /*! ) */` | row updated |

Why:
- `strings.Contains(lower, "read write")` only matches one exact space.
- `roBlockedExpr` wants the function name directly followed by `(`; a comment or a quoted name
  (`"set_config"(`, `set_config/**/(`) slips through. The same trick passes `pg_terminate_backend`,
  `pg_cancel_backend`, `lo_import`, `dblink`, and `INTO/**/OUTFILE`.
- `roSetVarRe` only looks at the first assignment of a `SET`; MySQL accepts a comma list.
- `WITH` is allowed, but `WITH ... DELETE/UPDATE/INSERT` is DML on all three engines.
- `stripLeadingComments` treats MySQL executable comments (`/*! ... */`) as comments, while the
  server runs their content.
- `RESET`/`DISCARD` pass when the text contains `temp`, `plans` or `search_path` anywhere, also in
  a comment (`RESET PERSIST /* temp */` ran on MySQL).

The same fragment check guards the WHERE/ORDER BY inputs and Export, and it is bypassed the same
way on the shared pool: WHERE `(SELECT set_config/**/('default_transaction_read_only','off',false)) IS NOT NULL`
left a pooled connection read-write, and a following
`Export{sql: "WITH d AS (DELETE FROM audit_ro WHERE id = 3 RETURNING id) SELECT * FROM d"}`
on the read-only connection deleted the row. ORDER BY `id LIMIT 3 INTO/**/OUTFILE '/path' -- `
passed the check too and was only stopped by the server's `secure_file_priv`.

Accidental DML is still stopped: plain `UPDATE`/`DELETE`, `WITH ... DELETE`, `EXPLAIN ANALYZE DELETE`
and `nextval()` were all refused by the session-level setting. The hole is that the session
setting can be turned off.

Fix: do not rely on regexes over raw SQL.
- Postgres: connect read-only sessions with a role-independent hard stop: run every console
  statement inside `BEGIN READ ONLY` ... (reject user transaction control, or track it), and refuse
  any statement where a tokenizer (comments and strings removed) finds `set_config`, `READ` + `WRITE`,
  a data-modifying keyword after `WITH`, or `INTO` after `SELECT`.
- MySQL: tokenize first (treat `/*! */` and `/*+ */` as code, not comments), reject `SET` with more
  than one assignment unless every target is allowed, reject `START TRANSACTION`/`BEGIN` with any
  `WRITE` token, and reject `WITH` unless the first top-level keyword after the CTE list is `SELECT`.
- After every statement on a read-only session, re-check the setting
  (`SHOW transaction_read_only` / `SELECT @@session.transaction_read_only`) and discard the
  connection when it is off.
- For WHERE/ORDER BY on the pool: run the page query in a `BEGIN READ ONLY` / `START TRANSACTION READ ONLY`
  transaction so a `set_config` inside it cannot outlive the call.

### 2. Postgres edits are silently truncated or padded instead of rejected — VERIFIED
`internal/changes/values.go:98` (`"CAST(" + ph + "::text AS " + typ + ")"`)

Values are bound as `CAST($n::text AS <column type>)`. An explicit cast to `varchar(n)`, `char(n)`
or `bit(n)` truncates or pads without error; a normal assignment raises one.

Reproduction on `audit_trunc (id int primary key, s varchar(5), ch char(3), b bit(8))`:
`ApplyChanges` with `set: {s: "abcdefghij", ch: "wxyz", b: "1"}` returns `ok: true`, the preview shows
`SET "s" = 'abcdefghij', "ch" = 'wxyz', "b" = '1'`, and the row holds `abcde`, `wxy`, `10000000`.
The same `UPDATE` typed by hand fails with `value too long for type character varying(5)`.

Fix: cast to the base type without a length (`a.atttypid::regtype`, e.g. `character varying`,
`bit varying`) and let the assignment enforce the length, or bind with the column's type OID so no
cast is needed.

### 3. A statement that committed is reported as "canceled" — VERIFIED
`internal/query/run.go:305-309` (in `Run`), `run.go:119-138` (`Cancel`)

After `runOne` returns, the result is overwritten with `canceled: query canceled` whenever a cancel
was requested, even when the statement finished without error. A cancel that arrives just after
the statement completed (or a MySQL `KILL QUERY` that lands between statements) hides a committed
write.

Reproduction: 300 runs of `UPDATE audit_cancel SET n = n + 1 WHERE id = 1` with `CancelQuery` fired
0–2 ms after the start. Postgres: 55 runs reported success, 131 reported "canceled", `n` was 183.
MySQL: 283 success, 10 "canceled", `n` was 289. So about 128 (Postgres) and 6 (MySQL) updates were
committed while the UI was told they were canceled.

Fix: only relabel when the statement itself failed with a cancel/interrupt error, or (MySQL
`SLEEP` case) when it is a row-returning statement. Never discard a successful DML result; show
"completed before the cancel took effect" instead.

### 4. Export with a SQL source runs any statement, including DML — VERIFIED
`internal/export/export.go:66-79`

The contract says "exactly one SELECT"; the code only checks that there is one statement and runs
it with `QueryContext`. On non-read-only connections:
- MySQL `Export{sql: "DELETE FROM audit_exp WHERE id < 3"}` → `rows: 0`, no error, two rows deleted.
- Postgres `Export{sql: "UPDATE audit_exp SET v = 'changed' RETURNING *"}` → 3 rows exported, 3 rows updated.

The frontend currently only exports pages, so this is reachable through the API only. It becomes
a real hazard as soon as "export this console result" is wired to the result's statement: exporting
an `INSERT ... RETURNING` result would insert the rows again.

Fix: require a row-returning read statement (same tokenizer as item 1) on every connection, and run
it inside a read-only transaction.

### 5. Console: a large result is read to the end after `maxRows` — VERIFIED
`internal/query/run.go:363` (`defer rows.Close()`), `internal/query/page.go:259-261`

`scanRows` stops at `maxRows`, then `rows.Close()` makes both drivers read and discard the rest of
the result. `SELECT a.* FROM sales_order_item a CROSS JOIN sales_order_item b` (24 M rows) with
`maxRows: 10` returned 10 rows with `durationMs: 34` (MySQL) / `114` (Postgres), but the call took
7.1 s and 6.0 s on localhost. On a remote server with a real table this is minutes of traffic, the
session stays locked, and the reported duration is wrong.

Fix: when the cap is hit, cancel the statement before closing (Postgres: cancel the query context;
MySQL: `KILL QUERY <id>` for session connections, close the connection for throw-away ones), and
measure the duration after `Close`.

### 6. SSH: a host that is in `~/.ssh/known_hosts` is refused as "host key changed" — VERIFIED
`internal/conn/ssh.go:45-68` (`hostKeyCallback`), `ssh.go:179-184` (`ClientConfig` without `HostKeyAlgorithms`)

The client offers Go's default host key order (ECDSA before Ed25519). When `known_hosts` has the
host under another key type than the one the server then presents, `knownhosts` returns a
`KeyError` with `Want` set, which the code reports as `host_key_mismatch ... This may be an attack`.

Reproduction: server with an Ed25519 and an ECDSA host key, `known_hosts` containing only the
Ed25519 line (what OpenSSH writes without `UpdateHostKeys`): `TestConnection` fails with
`host_key_mismatch: host key for 127.0.0.1:49968 changed (got ecdsa-sha2-nistp256 ...)`. There is no
way around it from the UI, since mismatches never offer to trust.

Fix: before dialing, look up the key types known for the host (call the `knownhosts` callback with
a dummy key and read `KeyError.Want`) and set `ClientConfig.HostKeyAlgorithms` to those types.

### 7. SQLite: one broken view makes the whole database unbrowsable — VERIFIED
`internal/driver/sqlite.go:143-148` (bulk `pragma_table_xinfo` join), `internal/meta/meta.go:42-64`

The bulk column query joins `pragma_table_xinfo(m.name)` for every table and view. A view whose
base table was dropped makes that pragma fail, so the whole query fails, `Meta.load` returns the
error, and every call that needs metadata fails.

Reproduction: `CREATE VIEW audit_v AS SELECT a FROM audit_base; DROP TABLE audit_base;` →
`Tables()` and `FetchPage(store)` both return `db_error: SQL logic error: no such table: main.audit_base`.
A virtual table whose module is not compiled in will do the same.

Fix: on error, fall back to one `pragma_table_xinfo` call per table and skip the ones that fail
(keep the table in the list with no columns and a note).

### 8. "Test connection" on a connected MySQL-over-SSH connection breaks the open pool — VERIFIED
`internal/driver/mysql.go:42-50` (`mysql.RegisterDialContext`), `internal/conn/conn.go:122-143` (`Test`)

The dial function is registered globally under `dbviewer-ssh-<connection id>`. `Test` opens a second
tunnel for the same id, which replaces the registered function, and then closes that tunnel. The
live pool now dials through a closed SSH client.

Reproduction: connect, `FetchPage` works, call `TestConnection` with the saved config, then every
new physical connection fails with `db_error: via SSH tunnel: EOF` until a manual reconnect.
Unsaved configs all share the name `dbviewer-ssh-`, so two tests at once can cross tunnels. The
registrations are never removed.

Fix: register under a unique name per `Open` call (for example a counter or random suffix) and
call `mysql.DeregisterDialContext` in the pool's closer.

---

## Medium

### 9. Disconnect, save, delete and UseDatabase hang until a running console query ends — VERIFIED
`internal/query/run.go:153-164` (`CloseSession` takes `s.mu`), `internal/conn/conn.go:171-183, 226-239`

`OnClose` → `Runner.CloseConnection` → `CloseSession` waits for the session mutex, which `Run` holds
for the whole query. `Disconnect` while `SELECT pg_sleep(6)` was running took 5.5 s. Running
queries are not cancelled, so disconnecting from a runaway query does not stop it.

Fix: on close, first cancel every running query of that connection (keep the cancel func per
session), then discard the connection.

### 10. SQLite paths with `#`, `?` or `%` open the wrong file, and read-only is lost — VERIFIED
`internal/driver/sqlite.go:42` (`"file:" + path + "?" + q.Encode()`)

The path is put into a URI without escaping.
- `my#db.sqlite` (read-only connection): SQLite opened `.../my`, created that empty file on disk,
  and showed an empty database. `mode=ro` sits behind the `#` and is ignored.
- `what?.sqlite` and `100%41.sqlite`: `unable to open database file (14)`.

Fix: build the URI with `(&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()`.

### 11. Postgres connect errors leak part of the password to the UI — VERIFIED
`internal/driver/postgres.go:55, 70-73`; `internal/conn/conn.go:210-217` (`fail` stores and emits it)

A config parse error contains the DSN. pgx redacts `password='...'` up to the first quote, so a
password with a quote leaks its tail. Password `hunter2' tail-of-secret` with `sslmode: bogus` (or
a bad `connect_timeout` param) gives:
``invalid_argument: cannot parse `host='127.0.0.1' ... password=xxxxx tail-of-secret' dbname=...` ``
This text is returned by `TestConnection`, kept in `Manager.errors` and sent in `conn:status`.

Fix: build the config with `pgx.ParseConfig("")` and set `Host`, `Port`, `User`, `Password`,
`Database` and `TLSConfig` on the struct, and validate `sslMode` in `store.Normalize`.

### 12. `.env` import: individual `DB_*` keys override `DATABASE_URL` — VERIFIED
`internal/envimport/envimport.go:90-138`

Laravel gives the URL precedence. Here the URL is applied first and then overwritten:
`DATABASE_URL=postgres://app:s3cr3t@prod.example.com:5433/shop?sslmode=require` plus the stock
`DB_HOST=127.0.0.1`, `DB_PORT=3306`, `DB_DATABASE=laravel`, `DB_USERNAME=root`, `DB_PASSWORD=` gives
driver postgres, host `127.0.0.1`, port `3306`, user `root`, empty password, database `laravel`.
The form is prefilled with a mix that points at neither database, without a warning.

Fix: when a URL was applied, only fill fields it left empty, and warn about ignored keys.

### 13. `.env` import returns the `DATABASE_URL` password unmasked — VERIFIED
`internal/envimport/envimport.go:80-87`

`Found` masks `DB_PASSWORD` but copies `DATABASE_URL` / `DB_URL` as is
(`found[DATABASE_URL] = postgres://app:s3cr3t@prod...`), so the import summary shows the password.

Fix: mask the userinfo password of URL values before putting them in `Found`.

### 14. Boolean columns hide values other than 0/1, and SQL export writes `1` — VERIFIED
`internal/query/encode.go:72-75`; `internal/driver/mysql.go:278-281`, `internal/driver/driver.go:91-92`

MySQL `tinyint(1)` and SQLite `BOOLEAN` are encoded as `x != 0`. A `tinyint(1)` holding `2` or `-1`
is shown as `true`, and `Export{format: "sql"}` wrote `VALUES (1, 1, ...)` and `(2, 1, ...)` for the
rows with `flag = 2` and `flag = -1`. Importing that dump changes the data.

Fix: encode as boolean only for 0 and 1; send other values as numbers, and let the SQL writer use
the raw integer.

### 15. SQLite date/time columns are reformatted, and rows keyed by them cannot be edited — VERIFIED
`internal/query/encode.go:97-98, 159-171`; `internal/driver/sqlite.go:19-52` (DSN)

`modernc.org/sqlite` parses text in `DATE`/`DATETIME`/`TIMESTAMP` columns into `time.Time`, and the
encoder prints its own format, so the grid does not show what is stored:

| Stored | Shown |
|---|---|
| `2025-01-31T10:00:00Z` | `2025-01-31 10:00:00` |
| `2025-01-31 10:00` | `2025-01-31 10:00:00` |
| `2025-01-31 23:59:59` in a `DATE` column | `2025-01-31` |
| `2025-01-31 10:00:00.123456789` | `2025-01-31 10:00:00.123456` |

Duplicating or pasting a row writes the shown form. With such a column as primary key, an update
fails with `UPDATE matched no row (expected exactly 1): the row was changed or deleted in the meantime`,
which is not what happened.

Fix: select these columns as text (`CAST(col AS TEXT)` in `BuildSelect` for SQLite date kinds) so
the stored string is passed through.

### 16. Console: `USE` and `SET search_path` are undone on the next run — VERIFIED
`internal/query/run.go:283-289`; caller `frontend/src/components/console/SqlConsole.vue:127`

`Run` executes `USE <schema>` / `SET search_path TO <schema>` whenever `req.Schema` is set, and the
console sends the tab's schema on every run. `USE dbviewer_dummy_ro` followed by a second run of
`SELECT DATABASE()` in the same session returns `dbviewer_dummy`. The contract says `USE` and `SET`
carry over between runs.

Fix: apply the schema only when the session connection is created.

### 17. Dropping a Postgres unique index also drops a constraint with the same name — VERIFIED
`internal/changes/structure.go:68-71`

For unique indexes the generated DDL is `ALTER TABLE t DROP CONSTRAINT IF EXISTS "name"; DROP INDEX IF EXISTS ...`.
Index and CHECK/FK constraint names live in different namespaces. With `CONSTRAINT audit_uq CHECK (id > 0)`
and `CREATE UNIQUE INDEX audit_uq ON audit_trunc (n)`, dropping the index removed the CHECK
constraint as well (0 check constraints left).

Fix: read `pg_constraint.conindid` in `Indexes` and emit `DROP CONSTRAINT` only for indexes that
back a constraint, otherwise plain `DROP INDEX`.

### 18. Read-only connections probably cannot connect to MariaDB before 11.1 — SUSPECTED
`internal/driver/mysql.go:76-80`

Read-only is applied as the connection parameter `transaction_read_only=1`, i.e.
`SET transaction_read_only = 1` on connect. MariaDB 10.x names the variable `tx_read_only`, so the
connect would fail with "Unknown system variable". No MariaDB server was available to confirm.

Fix: after connecting, run `SET SESSION TRANSACTION READ ONLY` (valid on both) from a connector
hook, or detect the server and choose the variable.

### 19. A dropped SSH tunnel is never rebuilt — SUSPECTED
`internal/conn/ssh.go:204-220` (keepalive closes the client), `internal/conn/conn.go:87-120`

When the keepalive fails the SSH client is closed, but the pool stays in `Manager.pools` with a dial
function bound to the dead client. Every later query fails with `via SSH tunnel: ...` and the status
stays "connected" until the user disconnects by hand.

Fix: on tunnel loss, drop the pool (`Disconnect` + `conn:status` with the error) so the next call
reconnects, or redial the SSH client inside the dial function.

### 20. Data races in the query runner — VERIFIED (first), SUSPECTED (others)
`internal/query/run.go:119-138, 102-108, 190, 55-73`

- `Cancel` reads `q.pool` and `q.mysqlID` after releasing the lock while `setRunningConn` writes
  them. `go test -race` reports it (`Write at ... setRunningConn run.go:105`).
- `acquire` calls `go discard(s.conn)` on a session it does not hold the lock for.
- `janitor` can remove a session that was just created (its `lastUsed` is the zero time) in the gap
  between `r.mu.Unlock()` and `s.mu.Lock()` in `acquire`. The run then uses an orphaned session
  whose connection is never returned, with any open transaction left open.

Fix: copy `pool`/`mysqlID` under the lock in `Cancel`; set `lastUsed` when the session is created;
discard under the session lock.

### 21. Console statement classification loses results and row counts — VERIFIED
`internal/query/run.go:226-235` (`rowKeywords`, `returningRe`)

- `UPDATE audit_misc SET v = 'returning customer'` matches `\breturning\b` inside the string, runs
  through `QueryContext`, and comes back as an empty result set with `rowsAffected: -1`.
- `ANALYZE TABLE t` (MySQL) and `FETCH ALL FROM cur` (Postgres) return rows but run through
  `ExecContext`; the rows are dropped. The same holds for `CHECK TABLE`, `OPTIMIZE TABLE`,
  `EXECUTE` and `WITH ... UPDATE` (no affected count).

Fix: strip strings and comments before matching, and extend the keyword list
(`fetch`, `execute`, `analyze`, `check`, `optimize`, `repair`, `checksum`, `handler`).

---

## Low

### 22. Statement splitter errors — VERIFIED
`internal/query/split.go:34-35, 66-86`
- MySQL: `SELECT 5--1 AS a; SELECT 2 AS b` is sent as one statement (`--` without a following space
  is not a comment in MySQL) and fails with a syntax error near `SELECT 2 AS b`.
- Postgres: `SELECT E'it\'s; fine' AS s` is cut at the `;` inside the string
  (`unterminated quoted string`).

Fix: MySQL `--` needs whitespace or end of input after it; handle backslash escapes in `E'...'`.

### 23. A trailing comment in the WHERE input breaks the query — VERIFIED
`internal/query/page.go:39`
WHERE `id = 1 -- only the first` becomes `WHERE (id = 1 -- only the first) ORDER BY ...` and fails with
`You have an error in your SQL syntax ... near ''`. Fix: emit `"(" + w + "\n)"`.

### 24. MySQL IPv6 hosts — VERIFIED
`internal/driver/mysql.go:39` (`fmt.Sprintf("%s:%d", host, port)`)
Host `::1`, port 3384 gives `dial tcp [::1:3384]:3306: connect: no route to host`; `[::1]` works.
Fix: `net.JoinHostPort`.

### 25. `DISCARD ALL` leaves a Postgres console session unusable — VERIFIED
`internal/query/readonly.go:80-83` allows it; pgx then fails with
`prepared statement "stmtcache_..." does not exist` for statements it had cached.
Fix: after `DISCARD ALL` / `DEALLOCATE ALL`, discard the session connection.

### 26. Postgres BC dates are shown wrongly and cannot be saved back — VERIFIED
`internal/query/encode.go:159-171`
`0044-03-15 BC` is shown as `-0043-03-15`; writing that value back fails with
`time zone displacement out of range`. Fix: format negative years as `YYYY-MM-DD BC`, or select
date kinds as text.

### 27. MySQL `bit(n)` with n > 1 is text or binary depending on the bytes — VERIFIED
`internal/driver/driver.go:80-87, 111`, `internal/query/encode.go:66-69`
`bit(8)` holding `0x41` is shown as `"A"`, `0xff` as a binary cell, and an all-zero `bit(64)` as a
string of eight NUL bytes. Fix: map `bit(n)` to the binary kind.

### 28. `.env` parsing edge cases — VERIFIED
`internal/envimport/envimport.go:33-65, 185-191`
- `DB_PASSWORD="sec#ret" # the "real" one` → password `sec#ret" # the "real` (closing quote found
  with `LastIndex`). `DB_USERNAME='root' # it's root` → user `root' # it`.
- A UTF-8 BOM before `DB_CONNECTION=pgsql` hides the key: "DB_CONNECTION missing, assuming mysql".
- `DB_PASSWORD=null` and `DB_SOCKET=null` are taken as the text `null` (Laravel reads them as null),
  which also triggers the "socket is used" warning.
- `DATABASE_URL=sqlite:database/app.sqlite` is ignored and the Laravel default path is used.
- `DB_DATABASE=:memory:` becomes `<dir>/:memory:`.
- `"pa$$w${ord}x"` loses `${ord}` (expansion of an undefined variable to empty) without a warning.

Fix: scan for the first unescaped closing quote, strip a BOM, map `null`/`(null)`/`empty`, handle
opaque sqlite URLs, and warn on undefined `${VAR}`.

### 29. Metadata cache reloads more than needed — SUSPECTED (by reading; reload measured at 27 ms on the local dummy)
`internal/meta/meta.go:42-64, 75-92`; `internal/services/data_service.go:121-123`
- Every successful `ApplyChanges` drops the schema cache, so each save is followed by a full
  tables + columns reload. On a remote 976-table schema this is a visible pause per save.
- `load` has no single-flight: the sidebar, the grid and the search index each run the bulk queries
  on first use.
- Asking for a table that is not in the cache forces a full reload on every call.

Fix: keep the cache on data changes (refresh only row estimates), wrap `load` in a per-key
`singleflight`, and remember negative lookups briefly.

### 30. Query history grows without bound and stores statements verbatim — SUSPECTED
`internal/store/store.go:433-439`
No pruning, and statements such as `CREATE USER ... IDENTIFIED BY '...'` or `ALTER ROLE ... PASSWORD`
are kept in plain text in `state.sqlite`. Fix: cap per connection (for example 5,000 rows) and
mask password clauses.

### 31. `maxRows` has no upper bound — SUSPECTED
`internal/query/run.go:270-273`
`FetchPage` caps at 10,000, the console accepts any `maxRows` from the caller and holds the rows in
memory. Fix: cap it (for example 50,000).

### 32. Structure preview is regenerated on apply — SUSPECTED
`internal/services/schema_service.go:197-213, 226-240`
`ApplyStructureChanges` rebuilds the DDL from fresh metadata instead of running what
`PreviewStructureChanges` showed. If the table changed in between, the executed DDL differs from the
confirmed preview. Fix: return the statements from the apply call and compare them with a hash the
preview handed out, or have the frontend send back the previewed statements for a check.

### 33. Paging without a primary key is unstable — SUSPECTED
`internal/query/page.go:144-153`
With no PK and no ORDER BY there is no ordering at all, so `LIMIT/OFFSET` pages can repeat or skip
rows. A user ORDER BY gets no tie-breaker either. Fix: append the PK columns (or `ctid` / `rowid`)
as the last sort key.

---

## Checked and found sound

- **Row targeting in ApplyChanges.** WHERE uses exactly the PK columns in PK order, rejects a
  missing, NULL or DEFAULT PK value, and every UPDATE/DELETE must hit exactly one row
  (`ClientFoundRows` is on for MySQL). Composite keys work. MySQL `bigint unsigned` keys above 2^53
  and `decimal(30,10)` keys sent as strings matched only the exact row, and a stale delete was
  refused rather than hitting a neighbour.
- **Transactions.** Every failure path in `Apply` and `ApplyStructure` rolls back before returning;
  a failed commit is reported; context cancel rolls back through `database/sql`.
- **Identifier quoting.** Table, schema and column names are quoted with doubled quote characters
  everywhere; column filters and Find terms are bound parameters; unknown columns are rejected.
- **Read-only, session layer.** Plain DML, `WITH ... DELETE`, `EXPLAIN ANALYZE DELETE`, `nextval()`
  and `BEGIN READ WRITE` (single space) are refused on Postgres and MySQL. SQLite stays read-only
  even after `PRAGMA query_only/**/=0`, because the file is opened with `mode=ro`.
  `ApplyChanges` and `ApplyStructureChanges` refuse read-only connections twice.
- **Value round-trips.** Re-inserting displayed values gave byte-identical rows for Postgres
  `timestamptz`, `timestamp`, `time`, `timetz`, `interval`, arrays, `uuid`, `bit`, `varbit`,
  `money`, `bytea`, `json`, `jsonb`, `numeric`, `real`, `double precision`, `bigint`, `inet`,
  `char(n)`, `xml`, `point`, range, including `infinity`, `NaN` and NULL vs empty string, and for
  MySQL `datetime(6)`, `timestamp(3)`, `date`, `time(2)`, `year`, `decimal(40,20)`, signed and
  unsigned `bigint`, `float`, `double`, `binary`, `varbinary`, `blob`, `json`, `enum`, `set`, text
  with newlines, quotes and backslashes, and the literal string `NULL`.
- **Connections.** MySQL over the Unix socket with an empty password works (all MySQL probes used
  it). Lazy connects are serialized per connection. `FetchPage` enforces the 10,000 row cap and
  truncates long cells.
- **Secrets.** Passwords are not in the stored config JSON and not in any returned config; the
  Keychain backend passes the secret over stdin, not argv; `secrets.json` (dev only) and
  `known_hosts` are written 0600, the data directory 0700.
- **Export.** Rows are streamed to a `.part` file and renamed; nothing is buffered beyond one row.
- **Metadata.** Columns, PKs and single-column FKs come from one bulk query per schema; no N+1 in
  the sidebar path (reload: 27 ms MySQL, 16 ms Postgres, 4 ms SQLite on the 266-table dummy).
