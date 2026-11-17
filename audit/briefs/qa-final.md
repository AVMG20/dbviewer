# QA final pass (tester qa4): you are the only tester now

Read qa-common.md first. Your tester id is qa4, and you write findings to `/Users/arno/tools/dbviewer/audit/qa-4.md`.

Earlier passes found and fixed issues; see qa-1.md, qa-2.md and qa-3.md. Don't re-report things those files list as fixed unless they are still broken.

Use project "Dummy shop": the MySQL "dummy (local mysql)" connection and the red read-only "dummy_ro" connection. You may create a project "QA final" with a Postgres connection (127.0.0.1:55432, postgres/dbviewer, db dbviewer_dummy) and an SQLite connection (`/Users/arno/tools/dbviewer/testdata/dummy.sqlite`). Delete that project at the end.

Cover these areas. Take screenshots, look at them, and write a finding for anything broken, confusing or ugly.

1. **Tabs.**
   - A single click in the sidebar opens an italic preview tab, and the next click replaces it.
   - A double-click, or filtering inside the tab, makes the tab permanent.
   - Shift+click on a tab closes it. Also check middle-click, Cmd+W and Cmd+1..9.
   - Back/forward:
     - Dispatch a MouseEvent `mouseup` with button 3 or 4 on the document via page.evaluate.
     - Press Cmd+[ and Cmd+].
   - The active tab's stripe follows the connection color, and the read-only connection shows a lock plus a READ-ONLY badge in the status bar.

2. **Sidebar.**
   - After a single click on a table, typing must start the speed search: focus stays in the sidebar.
   - Check speed search readability; while it is active the sidebar widens.
   - Check keyboard tree navigation (arrows, Left/Right, Enter) and the context menus on connection, schema and table.

3. **Grid.**
   - Column filter popover, every operator, and the chips.
   - Quick find (Cmd+F).
   - Right-click "filter by this value".
   - Record panel.
   - Cmd+click on `sales_order.entity_id`: a "Referenced by" dropdown appears.
   - Cmd+click on `sales_order_item.order_id`: it jumps straight to sales_order.
   - Select several rows via the row numbers and press Delete: they turn red. Revert.

4. **One real save.**
   - In `dbviewer_dummy.store`, edit the `name` of one row and press Cmd+S. Check the SQL preview, confirm, and verify the grid.
   - Then set the value back and save again.

5. **Structure tab.**
   - Check columns, indexes, FKs in and out, and DDL with Copy.
   - Stage a rename and an add column, check the preview, then Revert. Do not apply.

6. **Console.**
   - EXPLAIN, history, saved queries.
   - On dummy_ro, an UPDATE must be refused with a clear message, and a SELECT must work.
   - Postgres: `SELECT pg_sleep(3)` and cancel it.

7. **Export.** Export CSV and JSON of a filtered sales_order. In the browser, type a path inside the pw scratchpad folder. Open the files and verify the content.

8. **Find in database (Cmd+Shift+F).** Search `anouk`. Check the progress, the results, that a click opens the right row, and that cancel works.

9. **Connection modal.**
   - Paste `postgres://postgres:dbviewer@127.0.0.1:55432/dbviewer_dummy` into the Host field: the whole form must fill.
   - Check that a wrong socket path gives a readable error.

10. **Visual polish.**
    - Dark and light screenshots of the table, structure, console, palette, connection modal, context menu, save preview modal and find modal.
    - Judge them like a picky designer: alignment, spacing, contrast, icon visibility, truncation, and hover/focus states.

11. **Keyboard-only.** Use the app for a while without the mouse and report where you get stuck.

Cleanup: revert all staged edits, close the tabs you opened, and delete the "QA final" project.
