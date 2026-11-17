# QA 2: data grid, filtering, copy/paste, editing, read-only

Tester: qa2. Project "Dummy shop", MySQL `dbviewer_dummy` (sales_order, store) and `dbviewer_dummy_ro` (store, read-only). No save was performed. All edits were staged and reverted.

Shots dir: /private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/pw/shots/

Coverage note: this pass covered sections 1, 2 (WHERE/ORDER BY/paging), 4 (partly), 5 (partly), and 6. Sections 3, 7, 8 and the real save test in 5 were NOT covered. See the end.

## Findings

### 1. [medium] Cmd+A then Cmd+C copies an empty clipboard on sales_order
- Steps: open `sales_order` (Dummy shop, dummy local mysql). Click any cell. Press Cmd+A. Press Cmd+C. Read the clipboard.
- Expected: TSV of the selected rows (at least the 500 loaded rows).
- Actual: Cmd+A marks all 492 rendered cells as selected (`.dg-cell.sel`). Cmd+C then leaves the clipboard empty (length 0). A shift-click range of two rows copies correctly, so the problem is specific to select-all.
- Seen once in the run. Re-verify before filing.
- Screenshot: shots/qa2-cmda.png

### 2. [medium] Bad WHERE keeps the previous valid rows on screen with a valid-looking count
- Steps: open `sales_order`. Type `status = 'pending' AND grand_total > 100` in WHERE, press Enter (shows "1–318 of 318"). Then type `status = = 'x' AND` and press Enter.
- Expected: the red error is shown and the old rows are clearly marked as stale (dimmed or labelled), or the grid is cleared.
- Actual: the red banner shows the MySQL 1064 error, which is readable. But the grid still shows the 318 old rows with a normal "1–318 of 318" count, and the status bar still says "318 rows". Nothing marks the rows as stale. The banner also shows the generated query tail (`... ORDER BY grand_total DESC, \`entity_id\` LIMIT 501 OFFSET 0`), which is noise for the user.
- Screenshot: shots/qa2-where-bad.png

### 3. [low] Console and HTTP 422 on a bad query
- Steps: as in #2.
- Actual: a 422 is returned from `/wails/runtime` and logged to the console as "Failed to load resource". This is the expected error path, but the console noise makes real errors harder to spot.

### 4. [low] Header type labels truncate hard at default widths
- Steps: open `sales_order` with the default column widths.
- Actual: column names are shown in full, but type hints collapse to a few characters: `int unsigned` shows as `i…`, `varchar(128)` is only shown on some columns, and `blob` shows as `b…`. The types are useful for a DBA, so they are worth keeping. Consider a tooltip with the full type or a wider header.
- Screenshot: shots/qa2-sales-order-open.png

### 5. [low] Shared app state leaks into the grid on open
- Steps: open `sales_order` after another session has run. The ORDER BY already holds `customer_firstname` with an ascending sort arrow, which I did not set.
- Actual: the per-tab WHERE/ORDER BY state persists between sessions and is not reset on open. It is probably intended (tab state), but it means the first screen of a table may not show the natural order. Also, the active project switched during the run because the app is shared with the other testers.
- Screenshot: shots/qa2-sales-order-open.png

### 6. [low] Tab strip fills with "Console" tabs
- Actual: several "Console", "Console 2" ... "Console 6" tabs accumulate in the tab strip during the run. They are probably from other testers, but they push the real tabs and the "+ SQL" button toward the edge. Check whether there is a cap or a cleanup.
- Screenshot: shots/qa2-where-bad.png (tab strip)

## Works fine

- Opening `sales_order`: no cell is auto-selected (no `.dg-cell.sel` on open). NULL is shown italic. Numbers are right-aligned. Booleans are green `true` and grey `false`. Datetimes are shown in full. JSON cells are truncated with an ellipsis. Blobs show as `<blob N B>`. Long `customer_note` text is truncated with an ellipsis.
- Row count and paging: "1–500 of 2,000" on open. Next page gives "501–1,000 of 2,000". The page size select has 100 / 250 / 500 / 1000 / 5000 and 100 works ("1–100 of 2,000").
- Valid WHERE `status = 'pending' AND grand_total > 100` returns "1–318 of 318" with the exact count.
- ORDER BY `grand_total DESC` applies and the paging stays consistent.
- Single-cell Cmd+C copies the raw value. A NULL cell copies as `\N`.
- Shift-click range Cmd+C copies TSV: tabs between columns, newline between rows.
- The "Copy as" menu lists CSV, TSV, JSON, SQL INSERT, SQL UPDATE, Markdown table, IN (…) list, Column names.
- Staged edit: Enter commits the value. The Save button shows the pending count ("Save 1"), the tab shows a "1" badge, and the status bar shows "1 pending change". Escape cancels a new edit without changing the cell.
- Undo and redo: Cmd+Z removes the staged edit and Cmd+Shift+Z restores it.
- Revert asks "Revert all changes? Discard 1 pending change in store?" with Cancel and Revert all. The dialog works (screenshot shows it).
- Read-only `dummy_ro` store: typing, paste (Cmd+V), Backspace, Cmd+Backspace (delete row), Cmd+N (add row), and Cmd+D (duplicate) are all blocked. Each shows the "Connection is read-only" message, the grid is unchanged, and no pending change is created. The READ-ONLY badge and the PRODUCTION · READ-ONLY status bar are shown. Screenshot: shots/qa2-ro-blocked.png

## Not covered in this pass

- Section 2: sorting by header click and shift-click secondary sort; a bad ORDER BY.
- Section 3: per-column filter popover and operators, filter chips, quick find (Cmd+F) and highlighting, and "filter by this value".
- Section 4: Cmd+Shift+C escaping for quotes, tabs and newlines; paste of CSV/JSON; the long-value popover on `customer_note` and `payment_payload`.
- Section 5: Set NULL, add/duplicate/delete row on the dummy DB, colors (amber/green/red), the close-dirty-tab confirmation, TSV paste into a selection, the "long value" popover, and the real save test on `store` (with restore).
- Section 7: record panel and FK "open referenced row" from `sales_order_item.order_id`.
- Section 8: visual review of toolbar, grid and popovers in dark and light.

## Summary by severity

- high: 0
- medium: 2 (#1 select-all copy, #2 stale rows after bad WHERE)
- low: 4 (#3 console 422, #4 type truncation, #5 persisted sort state, #6 Console tab buildup)
