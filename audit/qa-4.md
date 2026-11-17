# QA 4: final pass (tabs, sidebar, grid, save, structure, console, find)

Tester: qa4. Project "Dummy shop" (dummy (local mysql), dummy_ro (production)). No "QA final" project was created, so there is nothing to delete. Coverage is partial; see "Not covered".

Shots dir: /private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/pw/shots/

## Test incident (not a product bug, disclosed)

- While testing the read-only console, my script typed `UPDATE store SET name = 'x' WHERE store_id = 1;` into the console that was already open on **dummy (local mysql)**, not dummy_ro (my "New SQL console" step did not find the dummy_ro row). It ran and changed `dbviewer_dummy.store` row 1 to `x`.
- Restored right away with the mysql CLI: `dbviewer_dummy.store` row 1 is `Default Store View` again (verified). `dbviewer_dummy_ro` was never changed (still `Default Store View`).
- The later real read-only test (below) used the correct dummy_ro console.

## Findings

### 1. [medium] Uncaught JS errors while running EXPLAIN then UPDATE in the SQL console
- Steps: Open a new SQL console on dummy (local mysql). Run `EXPLAIN SELECT * FROM sales_order WHERE status = 'complete';` (Cmd+Enter). Replace the text with `UPDATE store SET name = name WHERE 1 = 0;` and run it (Cmd+Enter).
- Expected: no page errors. The result area shows the update result.
- Actual: two uncaught page errors were logged: `Cannot read properties of null (reading 'id')` and `Cannot read properties of null (reading 'nextSibling')`. The UI still works, but the errors point to a DOM/null-reference bug in the result or editor code. The same errors did not appear in a later run that opened the read-only console, so it may depend on the order of actions.
- Screenshot: shots/qa4-console-update-local.png (shows the next finding too)

### 2. [medium] Old EXPLAIN result stays under the new UPDATE result
- Steps: As in finding 1.
- Expected: after an UPDATE, the result area shows only the write result (`OK · 0 affected`).
- Actual: the banner says `OK · 0 affected` and the status bar says `0 rows`, but the grid below still shows the EXPLAIN columns (id, select_type, table, partitions, type, possible_keys, key ...) with the old row. The user can read it as the result of the UPDATE.
- Screenshot: shots/qa4-console-update-local.png

### 3. [medium] Save looks active with no pending changes; Revert is disabled while a draft column is open
- Steps: Open `store` on dummy (local mysql). Do nothing. Look at the toolbar. Then open "Open structure", click "+ Add column" and look at the toolbar again.
- Expected: Save is disabled (grey) when nothing is pending. Revert is enabled when an unsaved draft row (the new column) exists.
- Actual: with no changes, Save is the full green primary button (same on the data grid and the structure view). After "+ Add column" the draft row appears in green, but Revert stays greyed out, and Save stays green. Revert only works after a real staged change.
- Screenshots: shots/qa4-start.png (Save green, Revert grey, no changes), shots/qa4-structure-add-col.png (draft row, Revert grey)

### 4. [low] Find in database: empty placeholder bars left in the results
- Steps: Cmd+Shift+F. Connection dummy (local mysql), search `anouk`, contains, Enter.
- Expected: the search finishes with "220 rows in 20 tables" and only the matching table cards.
- Actual: the search finishes correctly (220 rows in 20 tables, 50 ms, results open in order), but between the matched cards there are about 15 empty grey bars with no text (looks like unfilled skeletons that are never removed). The list looks broken. Result cards with data are readable: `sales_order 50 rows`, with matching `anouk.dekker487@example.nl` rows highlighted.
- Screenshot: shots/qa4-find-results-dark.png

### 5. [low] Column filter popover: stray glyphs in the row-number gutter
- Steps: Open `sales_order`. Click the filter icon on `customer_firstname` (or any column) with a filter active, so the popover is open over the grid.
- Expected: the row-number gutter is empty except for numbers.
- Actual: small clipped characters (for example `t`, `m`) appear to the left of the customer_firstname cells on several rows (rows 3, 9, 11, 14, 20, 29 in the screenshot). They look like the tail of a cell scrolled out of view, leaking into the sticky gutter.
- Screenshot: shots/qa4-pop-endsw.png

### 6. [low] Filter chip wording: "not contains"
- Steps: Column filter on `customer_firstname`, operator "does not contain", value `an`, Apply.
- Expected: the chip reads like the picker label, e.g. "does not contain an".
- Actual: the chip reads `customer_firstname not contains an`. The chip for "starts with" and "ends with" reads correctly.

### 7. [low] Keycap hints in buttons render as empty black pills
- Steps: Look at the Save preview dialog (Cmd+S on a staged change), the console "Run" button, and the column filter "Apply" button.
- Expected: the shortcut hint (⌘↵ / ↵) is visible inside the button.
- Actual: the hint is a black pill with no visible glyph (the Save button in the preview dialog, "Run" in the console, "Apply" in the popover). Readable on the "Run all ⌘⇧↵" button, which is inconsistent.
- Screenshots: shots/qa4-save-preview.png, shots/qa4-console-update-local.png

### 8. [low] Console tab strip keeps growing
- Steps: Open several SQL consoles (the "+ SQL" button or the palette).
- Expected: a limit, or the tabs scroll cleanly with a visible overflow control.
- Actual: "Console", "Console 2" ... "Console 4" and a read-only "Console" with a lock pile up in the strip. The strip scrolls but the active tab can end up off-screen. This was already in qa2 finding 6 and is still present.
- Screenshot: shots/qa4-structure-store-dark.png (strip at the top)

## Works fine

Tabs
- Single click in the sidebar opens an italic preview tab. The next single click replaces that preview tab.
- Double-click makes a preview tab permanent (it stops being italic).
- Applying a WHERE filter inside a preview tab makes it permanent, so the next single click opens a new preview tab instead of replacing it.
- A single click on a table that already has a permanent tab activates that tab, no new tab.
- Middle-click on a tab closes it. Shift-click on a tab closes it.
- Cmd+1 and Cmd+2 select tabs 1 and 2.
- Mouse back/forward (mouseup button 3 and 4) and Cmd+[ / Cmd+] move back and forward through tab history.
- Cmd+W closes the active tab.
- The read-only connection tab has a red stripe and a lock icon. The status bar shows `dummy_ro (production)` and `PRODUCTION · READ-ONLY`.

Sidebar
- After a single click on a table, the focus stays in the tree, and typing `custemail` starts speed search at once.
- While speed search is active the sidebar widens from 268 px to 440 px. Table names and the `· customer_email` hints are fully readable (qa1 finding 3 is fixed). The overlay shows `63 matches` and Esc clears it and the width returns to 268 px.
- Keyboard: ArrowDown/Up move through rows, ArrowRight expands a collapsed node, ArrowLeft collapses it, Enter on a table opens it.
- Context menus. Connection: Disconnect, New SQL console, Refresh, Edit, Duplicate, Delete. Table: Open data, Open structure, Copy name, Copy SELECT, New SQL console with SELECT, Refresh. The schema menu has only New SQL console and Refresh (thin, but not wrong).

Grid
- Column filter popover opens with the column name, type, operator select, value box, and Cancel/Apply.
- Operators tested: greater than, at least, less than, at most, not equals, contains, starts with, equals. Each filter gives the expected count (for example grand_total > 6000 gives 19 rows, contains "an" on customer_firstname gives 322 rows). "is NULL" and "is not NULL" have no value box (correct). "between" has From/To boxes (not verified against data).
- Filter chips appear above the grid ("Filters: grand_total ≠ 0 x, ... Clear all"). Clear all removes them and restores 2,000 rows.
- Quick find (Cmd+F) shows the "Find in loaded rows" box and filters the view.
- Right-click on a cell: "Filter = value", "Filter ≠ value", "Filter IS NULL", copy items, Set NULL, View value, Show referencing rows, Duplicate row, Delete row. "Filter = value" gives 62 rows for Julia (1–62 of 62).
- Cmd+click on `sales_order.entity_id` (value 1) opens a "REFERENCED BY" dropdown: `sales_order_item.order_id (1 row)` and `sales_order_status_history.parent_id (2 rows)`. Screenshot: shots/qa4-fk-cmd-entity.png
- Cmd+click on a `sales_order_item.order_id` value navigates to `sales_order` (tab list changed from sales_order_item to sales_order).

Save round trip (dummy DB, restored afterwards)
- Edit `store.name` on row 1 to `QA Final Test`, press Enter: "1 pending change" in the status bar.
- Cmd+S opens "Save changes to store", 1 statement, run in one transaction, with the SQL `UPDATE \`dbviewer_dummy\`.\`store\` SET \`name\` = 'QA Final Test' WHERE \`store_id\` = 1;`, and Cancel / Save buttons. Screenshot: shots/qa4-save-preview.png
- Save shows the toast "Saved 1 change"; the grid shows the new value.
- Set back to `Default Store View` and saved again. Verified in both DBs (mysql CLI).

Structure
- Columns, indexes, foreign keys, "Referenced by" (3 tables, with ON UPDATE/ON DELETE) and the DDL block render correctly with the right types and key badges. Screenshot: shots/qa4-structure-store-dark.png
- "+ Add column" adds a draft row with name, type (varchar(255)), nullable and default fields.

Console
- EXPLAIN on sales_order returns the plan table (id, select_type, table, type, possible_keys, key, rows, filtered, Extra).
- `SELECT` on dummy_ro (read-only, red, "PRODUCTION · READ-ONLY") returns "4 rows · 0 ms".
- `UPDATE store SET name = 'x' WHERE store_id = 1;` on dummy_ro is refused. The message says the connection is read-only and writes are blocked. The row is not changed (verified in dbviewer_dummy_ro). Screenshot: shots/qa4-ro-update-refused.png

Find in database
- Search `anouk` (contains, limit 50 per table, all 266 tables) finds 220 rows in 20 tables in 50 ms. Result cards show the table name, matching columns, and the matching values, with the match highlighted in amber.

## Not covered (time)

- Tabs: none left open from the list, but "filter inside tab = permanent" was checked only with a WHERE filter, not with the column popover.
- Grid: row marking with Delete (red rows) and Revert, record panel, long-value viewer, paste.
- Structure: rename staging and the ALTER preview (the rename attempt went into the data grid by mistake, nothing was saved, the DB was verified unchanged).
- Export CSV/JSON of a filtered sales_order (not done).
- Find in database: clicking a result row and cancel (not done).
- Connection modal: pasting `postgres://postgres:dbviewer@127.0.0.1:55432/dbviewer_dummy` into Host, and the wrong-socket error text (not done).
- Postgres `pg_sleep(3)` cancel and SQLite (no "QA final" project was created).
- Dark/light visual pass on palette, connection modal, context menu and save preview (only some dark screenshots were taken and reviewed).
- Keyboard-only flow for the whole app.

## Summary by severity

- high: 0
- medium: 3 (#1 JS null-reference errors in console, #2 stale result under UPDATE, #3 Save green and Revert disabled with draft or no changes)
- low: 5 (#4 empty bars in Find in database, #5 gutter glyphs, #6 "not contains" chip, #7 empty keycap pills, #8 console tab strip overflow)
