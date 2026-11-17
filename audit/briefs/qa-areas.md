# QA areas, one per tester. Read qa-common.md first.

## qa1: projects, connections, navigation (use your own project "QA connections")

1. **Projects.** Create the project "QA connections", rename it, and switch between projects.

2. **New connection modal.** Create these connections in your project:
   a. MySQL over a Unix socket: socket `/opt/workbench/data/run/mysqld/mysql84.sock`, user root, empty password, database `dbviewer_dummy`, color green.
   b. The same via "Import from .env", by pasting the contents of `/Users/arno/tools/dbviewer/testdata/sample.env`. Check every field.
   c. Postgres at 127.0.0.1:55432, user postgres, password dbviewer, database `dbviewer_dummy`.
   d. SQLite with the path `/Users/arno/tools/dbviewer/testdata/dummy.sqlite`.
   e. A red "production" connection to `dbviewer_dummy_ro` with read-only on.

   For each connection: Test connection, Save, check that the tree loads, and check the color, lock and driver badges. Also try a wrong password and a wrong socket path; the error must be clear.

3. **Sidebar tree.** Expand and collapse, use the keyboard (arrows, Enter), and try the context menus.

4. **Speed search.** Click a table row, then just type (e.g. `custemail`, `sales_ord`).
   - Focus must stay in the sidebar after a single click.
   - The overlay must appear, and tables must match by name AND by column, with the column hint shown.
   - Check highlighting, arrows, Enter and Esc.
   - It must not trigger while an input has focus.

5. **Command palette (Cmd+K):** search, open, and the `>` commands.

6. **Tabs.**
   - A single click in the sidebar opens an italic preview tab. The next click replaces it, and a double-click makes it permanent.
   - Shift+click on a tab closes it. So do the × and a middle-click.
   - Drag to reorder. Cmd+W and Cmd+1..9.
   - Back/forward: dispatch MouseEvent `mouseup` with button 3 and 4 via page.evaluate on document, and press Cmd+[ / Cmd+].
   - The active tab's top stripe follows the connection color.
   - A read-only connection shows a lock and a READ-ONLY badge in the status bar.

7. **Dark vs light** (`open({dark:false})`): look for contrast problems, misalignment, clipped text and spacing.

8. **Failed requests:** there is at least one 404 on load. Find out which URL it is.

9. **Cleanup:** delete the connections and the project you created.

## qa2: data grid, filtering, copy/paste, editing, read-only (project "Dummy shop", MySQL dummy + dummy_ro)

1. **Opening tables.** Open `sales_order` (2000 rows).
   - No cell may be auto-selected on open.
   - Headers must show full column names.
   - Check the NULL rendering, number alignment, datetime and JSON display, and blob display.
   - Check the truncated `customer_note` and `payment_payload` columns.

2. **Paging, WHERE and ORDER BY.**
   - Page through, change the page size, and use the exact count.
   - Try `status = 'pending' AND grand_total > 100`.
   - Try a bad WHERE: the error must be clear and the old rows must not look valid.
   - ORDER BY, header-click sorting, and shift-click secondary sort.

3. **Filters.** Per-column filter popover with every operator, the filter chips, quick find (Cmd+F) with highlighting, and the right-click "filter by this value".

4. **Selection.**
   - Click, shift-click, Cmd-click, drag, and Cmd+A.
   - Copy with Cmd+C and verify the clipboard content (TSV, NULL as `\N`).
   - Use Cmd+Shift+C and every "Copy as" format. Check CSV/JSON/SQL escaping on values with quotes, tabs and newlines.

5. **Editing** (stage only, then Revert, except one real save test).
   - Double-click to edit, Enter, Esc, Tab.
   - Set NULL, the long-value popover, add row, duplicate row, delete row.
   - Undo/redo (Cmd+Z, Cmd+Shift+Z).
   - Colors: amber, green, red.
   - The pending badge on Save and the dirty dot on the tab.
   - Close a dirty tab: it must ask for confirmation.
   - Paste TSV into a selection.
   - One real save on `dbviewer_dummy.store`: change `name` of one row, Save, check the SQL preview and confirm, verify in the grid, then change it back and save again.

6. **Read-only.**
   - Open a table on `dummy_ro` (production, read-only).
   - EVERY edit path must be blocked with a single "Connection is read-only" toast: typing, paste, add, delete, Set NULL, Backspace, and the record panel.

7. **Record panel and FKs.** The record panel, and FK "open referenced row" from `sales_order_item.order_id`.

8. **Visual quality** of the toolbar, grid and popovers in dark and light.

## qa3: SQL console, structure, export, find-in-database, overall polish (project "Dummy shop" + Postgres/SQLite if present, else create your own project "QA console")

1. **SQL console** (the + SQL button).
   - Autocomplete for tables, and for columns after `table.` and after an alias.
   - Cmd+Enter runs the current statement; Cmd+Shift+Enter runs all of them.
   - Run multiple statements and check the result tabs.
   - Errors must show a clear message and position.
   - Cancel a slow query (`SELECT SLEEP(5)` on MySQL, `SELECT pg_sleep(5)` on Postgres).
   - History, saved queries, and EXPLAIN.
   - On a read-only connection, a write must be refused with a clear message, and a SELECT must still work.

2. **Structure tab.**
   - Columns, indexes, FKs in and out, and the DDL with its copy button.
   - Stage a column rename and an add column, check the SQL preview, then Revert. Do not apply.

3. **Export:** CSV and JSON of a filtered table. In the browser the native dialog falls back to a typed path; use the scratchpad folder as the target. Verify the file content.

4. **Find in database** (Cmd+Shift+F): search for `anouk` and check the progress, cancel, the results, and that clicking a result opens the right row.

5. **Overall UI polish.** Take full-window screenshots of every view in dark AND light, and judge them like a picky designer: spacing consistency, alignment, font sizes, icon sizes, contrast, hover/focus states, empty and loading states, and anything that looks unfinished.

6. **Keyboard-only flow:** use the app without the mouse for a minute (Cmd+K, arrows, Enter, Cmd+W, Cmd+[ ) and report where you get stuck.
