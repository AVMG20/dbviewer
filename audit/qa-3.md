# QA 3 findings: SQL console, structure, export, find-in-database, polish

Scope covered: SQL console on `dummy (local mysql)` (Dummy shop). Not covered (see "Not tested"): read-only SQL on `dummy_ro`, EXPLAIN, history/saved queries, Structure tab, export, Find in database, dark/light polish pass, keyboard-only flow.

Harness scripts: `/private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/pw/qa3-sql2.js`, `qa3-sql3.js`.

## Findings

### 1. [medium] Stale error panel stays visible while a new query runs, and after cancel
- Steps: SQL console on dummy (local mysql). Run `SELECT * FROM no_such_table` (Cmd+Enter). Then replace the text with `SELECT SLEEP(5)` and press Cmd+Enter. Click Cancel after about 1 s.
- Expected: while the new query runs, the result area shows a running state, or the old result is cleared. After cancel, the result area shows a "cancelled" state for this statement.
- Actual: during the run, the result area still shows the previous statement's error ("Query failed / Error 1146 ... no_such_table doesn't exist / SELECT * FROM no_such_table"), while the header says "Running...". After cancel the error panel still shows the old table error. The status bar says "Query failed" and the only sign of cancel is a toast, "Query cancelled".
- Screenshots: `/private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/pw/shots/qa3-sql-running.png`, `/private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/pw/shots/qa3-sql-cancelled.png`

### 2. [low] Status bar shows "1 row" after Run all with three result sets
- Steps: SQL console, enter `SELECT 1 AS a;` / `SELECT 2 AS b;` / `SELECT 3 AS c;` on separate lines, press Cmd+Shift+Enter.
- Expected: the status bar summarises all three statements (for example "3 results" or the total row count).
- Actual: the tabs read Result 1 / Result 2 / Result 3, but the status bar reads "1 row · 0 ms" (the value of the first result only).
- Screenshot: `/private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/pw/shots/qa3-sql-run-all.png`

### 3. [low] Console error on every page load: 404 Not Found
- Steps: open http://localhost:9245 and wait for load.
- Expected: no failed requests.
- Actual: `Failed to load resource: 404 (Not Found)` in the console on every load. The URL is not captured by the harness console handler; qa1 has the URL (qa1 item 8).
- Screenshot: none (console only).

## Works fine
- SQL autocomplete after `FROM sales_order o WHERE o.` lists the `sales_order` columns (alias resolved), with the table name as the right-hand detail. Screenshot: `.../shots/qa3-sql-autocomplete-alias.png`.
- Autocomplete for table names after `FROM sales_ord` lists `sales_order`, `sales_order_grid`, `sales_order_item`, `sales_order_status_history`, `sales_order_summary_view` (view tagged `view`), and `salesrule_order_value`.
- Autocomplete after `c.` in a JOIN resolves the `customer_entity` alias correctly.
- Cmd+Enter runs only the statement under the cursor (`SELECT 3 AS c;`). Screenshot: `.../shots/qa3-sql-cmd-enter.png`.
- Cmd+Shift+Enter runs all statements and produces Result 1/2/3 tabs.
- Syntax error shows a clear MySQL message with code (`Error 1064 (42000): You have an error in your SQL syntax ...`), a "Go to line 1 of the statement" link, and "Query failed" in the status bar. Screenshot: `.../shots/qa3-sql-error-syntax.png`.
- Unknown table shows `Error 1146 (42S02): Table 'dbviewer_dummy.no_such_table' doesn't exist` with the statement echoed below. Screenshot: `.../shots/qa3-sql-error-table.png`.
- Cancel of `SELECT SLEEP(5)` works: the Run button becomes Cancel while running, and the "Query cancelled" toast appears (see finding 1 for the stale panel).

## Not tested (blocked or out of time)
- Read-only connection (dummy_ro): my attempts to open it failed because the sidebar tree is long and the connection is far down the list. The later click landed on another project's state (see note below), so I did not get a clean read-only result. Read-only write refusal and SELECT on dummy_ro still need a test.
- EXPLAIN, history, saved queries.
- Structure tab (columns, indexes, FKs, DDL copy, staged rename/add column and Revert).
- Export CSV/JSON, Find in database (Cmd+Shift+F).
- Dark/light polish pass and keyboard-only flow.

## Test-environment note (not a product bug)
The app state (active project, open tabs) is shared between all QA testers. During my run the active project changed to "QA connections" (qa1's project), so my clicks landed in that project. An `UPDATE store SET name = name WHERE 1 = 0` that I ran in that state was on `dummy (local mysql)`, affected 0 rows, and changed nothing. Testers should use explicit connection selection rather than clicks on tabs that another tester may have moved.
