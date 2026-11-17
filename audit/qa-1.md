# QA 1: projects, connections, navigation

Scope: project "QA connections", connections a to e, sidebar, speed search, command palette.
Not finished: tabs (preview, shift-click, drag, back/forward), keyboard tree navigation, context menus, dark vs light pass, read-only badge check, PG right-password test, cleanup (connections and project still exist, see end).

Screenshots dir: /private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/pw/shots/

## Findings

### 1. [high] Browser sessions share state: other testers' tabs and project appear in my window
- Steps: Open the app in one headless browser (project "QA connections"). In a second browser run, the header of the first run switched to "Dummy shop", and later showed tabs I never opened (`sales_order` with `WHERE entity_id = 1719`, `sales_order_status_history`, `store`, a `Console` tab).
- Expected: each browser session keeps its own active project and open tabs, or the app clearly isolates testers.
- Actual: the active project, tabs and the Console tab change without user action. A new connection could be created or a query run in the wrong project or connection.
- Screenshot: shots/qa1-dbg2.png (tabs from another session, header "QA connections — dummy (local mysql) · sales_order"), shots/qa1-b-parsed.png (Console tab I did not open).
- Note: could also be a harness artifact because the three testers share one dev instance. Still, the app shows it as one shared state.

### 2. [medium] Raw driver error is truncated in the Test connection footer
- Steps: New connection, MySQL, Unix socket, socket `/opt/workbench/data/run/mysqld/nope.sock`, user root, Test connection.
- Expected: a short, readable message such as "Socket not found: /opt/.../nope.sock (is MySQL running?)".
- Actual: red text `dial unix /opt/workbench/data/run/mys...` (Go error, cut off with ellipsis). The full text is not visible anywhere.
- Same for Postgres with wrong password: `failed to connect to \`user=postgres da...` (cut off, raw pgx text).
- Screenshots: shots/qa1-c-badsocket.png, shots/qa1-c-pg-wrongpw.png

### 3. [medium] Speed search overlay truncates table names and column hints
- Steps: Click a table in the sidebar, type `custemail`.
- Expected: table name fully readable, column hint readable (e.g. `· customer_email`).
- Actual: names cut to about 12 characters (`admin_attrib...`, `catalogsearc...`), and the column hint is cut to `· cust...` on most rows. The hint is unreadable where it matters most.
- Result count is shown (`59 matches`), which works.
- Screenshot: shots/qa1-speed-custemail.png

### 4. [low] Modal header text reads as unfinished
- Steps: Open New connection.
- Actual: header line reads `Configure the connection or` with the `Import from .env` link on the right. The trailing "or" reads as a sentence cut off.
- Screenshot: shots/qa1-newconn-modal.png

### 5. [low] Save of the production connection logged an HTTP 422 on /wails/runtime
- Steps: Create "production" (MySQL socket, database `dbviewer_dummy_ro`, red, read-only), Save.
- Actual: console shows `http 422 .../wails/runtime` and `Failed to load resource: 422`. The save itself showed the "Created 'production'" toast. Not clear whether the 422 is tied to the read-only toggle or the color.
- Screenshot: shots/qa1-e-prod-saved.png (not reviewed in detail)

### 6. [low] Project menu swallows the next click
- Steps: Open the project menu, pick another project, then click "+ Connection" without closing the menu.
- Expected: the menu closes and the click opens the modal, or the menu closes on the pick.
- Actual: the menu stays open after picking a project, and the first click on "+ Connection" only closes it. The modal opens on the second click.

### 7. [low] Unidentified 404 on page load
- Steps: Load http://localhost:9245 and reload.
- Actual: console error `Failed to load resource: 404 (Not Found)` on every load. My response listener recorded no HTTP 4xx responses for the reload, so the request is probably not a page fetch (maybe favicon or a resource fetched outside the listener). Not identified yet.

## Works fine
- Projects: create "QA connections", rename it, switch back and forth. Header updates each time. Project menu lists Dummy shop, Other project, QA connections, New project, Rename, Move up/down, Delete.
- MySQL via Unix socket (connection a): Test shows "Connected to MySQL 8.4.9". Save works, sidebar shows the connection with the green dot and the MY badge, tree loads dbviewer_dummy with 265 tables.
- Import from .env (connection b): "Parse & fill" fills Name (`DummyShop (dbviewer_dummy)`, from APP_NAME), Driver MySQL, Unix socket, socket path, User root, Password empty, Database dbviewer_dummy, and lists the imported keys. All values match sample.env. Saved as `qa-env-import`.
- SQLite (connection d): File field with Browse, placeholder `/path/to/database.sqlite`, Test shows "Connected to SQLite 3.54.1 · 4 ms". Saved as `qa-sqlite`.
- Postgres (connection c): TCP host/port/user/database fields fill correctly; saved as `qa-pg` (PG badge shown). Wrong password fails at Test (see finding 2).
- Color swatches: green and red swatches set correctly (rgb 63,185,80 and 248,81,73).
- Speed search: typing `custemail` matches tables by column name, with count and hint shown; overlay appears and is anchored in the sidebar.
- Command palette (Cmd+K): typing `sales` lists tables (sales_order, sales_order_grid, ...) and the SQL console entry.

## Cleanup status
Not done. Left in place: project "QA connections"; connections qa-mysql-socket, qa-env-import, qa-pg, qa-sqlite, production. Delete them when the other areas are checked.
