# Frontend audit

Scope: `frontend/src` (Vue 3, Pinia, Tailwind 4), audited against PLAN.md, CONTRACT.md and the mockup.
Method: full read of every file in `frontend/src`, `npx vue-tsc --noEmit` (passes, no errors), and click-through in the browser dev server at http://localhost:9245 in dark and light theme.

Labels: **VERIFIED** = reproduced in the running app. **SUSPECTED** = follows from the code, not reproduced (reason given).

Test data: only the dummy databases were touched. One save was attempted on `dbviewer_dummy.admin_user` (item 3); it failed and rolled back as expected, and the row was checked afterwards. All staged edits were reverted, UI state keys were restored, and the preview tab was closed.

Not covered: the desktop WKWebView build (only the browser mode was run), SSH tunnels, `passwordMode: "ask"` connections (none exist in the test data), and binary values over 256 bytes (the test data has none). The mockup body is rendered by script, so only its CSS was compared.

Counts: 2 critical, 10 high, 17 medium, 14 low.

---

## Critical

### 1. Record view saves a truncated value for long text columns
**VERIFIED** · `components/table/RecordPanel.vue:29-41`, `:67-71`

Text over 8 KB arrives as `{$type:"text", truncated:true}` with only the first 8,192 characters. The record panel puts that cut text in a textarea and stages whatever is in it. The grid popover loads the full value first (`DataGrid.vue:537-566`); the record panel does not.

Repro: open `dbviewer_dummy.sales_order`, select row 2 (`customer_note` is 11,400 characters), open the record view, type one character in the `customer_note` textarea, and blur. The staged value is 8,193 characters. Save would drop the last 3,200 characters without any warning.

Fix: in the record panel, render truncated values read-only with an "Edit full value" button that goes through `loadFull` (or opens the grid popover). Also refuse in `onRecordEdit` when the original value is truncated and the full value was not loaded.

### 2. Entering and leaving a NULL cell stages an empty string
**VERIFIED** · `components/grid/DataGrid.vue:581-591` (check on line 588), `lib/format.ts:102-116`

`commitText` treats the edit as unchanged only when `cellEquals(val, cur)` or `cur !== null && cellText(cur) === text`. For a NULL cell the editor opens with `""`, `coerceInput` returns `""` for string, text, json and other kinds, and `""` is not equal to `null`, so an edit is emitted.

Repro: in `sales_order`, select a NULL `coupon_code` cell, press Enter, then Enter again (or double-click the cell and click elsewhere). Result: `updates = {"[1]": {set: {coupon_code: ""}}}`, the tab is dirty, and Save writes `''` over NULL. The same happens with Tab, and with Apply in the popover on a NULL JSON cell (which would write `''` into a JSON column).

Fix: track in `EditState` whether the text was changed (`text !== initialText`) and never emit when it was not. Keep "set empty string" explicit: typing and then clearing may stage `''`, but an untouched editor must be a no-op.

---

## High

### 3. Editing a cell and then deleting the same row makes Save fail every time
**VERIFIED** · `stores/changes.ts:174-191`, `:252-263`

`deleteRows` leaves the row's pending update in place, and `toChangeSet` sends both. The backend runs deletes first, so the UPDATE then matches no row and the whole transaction rolls back.

Repro: edit `firstname` on a row of `admin_user`, press Cmd+Backspace on that row, then Save. Preview shows `DELETE ... WHERE user_id = 5` followed by `UPDATE ... WHERE user_id = 5`. Apply returns `UPDATE matched no row (expected exactly 1)`. The pending count also shows 2 for one row. The only way out is to un-delete the row and undo the edit by hand.

Fix: in `toChangeSet`, skip updates whose row key is in `deletes` (and count them once). Block edits on deleted rows in `onRecordEdit` too; the grid blocks them, the record panel does not.

### 4. Touching a cell in a new row turns DEFAULT into `''`, including the auto-increment key
**VERIFIED** · `components/grid/DataGrid.vue:581-591`, `lib/format.ts:104`

Same root cause as item 2. In a new row every untouched cell shows DEFAULT. Opening and leaving one stages `""` for that column. For a NOT NULL int, `coerceInput` returns `""` as well (`col.nullable ? null : ""`).

Repro: Cmd+N in `sales_order`, press Enter on `entity_id`, click away; do the same on `state`. Preview: `INSERT INTO sales_order (entity_id, state) VALUES ('', '')`. In strict mode this errors on Save; in non-strict MySQL it inserts 0 and `''`.

Fix: same as item 2. In addition, for a non-nullable numeric, date or bool column with empty text, keep DEFAULT (new rows) or reject the edit (existing rows) instead of sending `""`.

### 5. Text can be pasted or typed into binary columns and is written as literal text
**VERIFIED** (paste, record panel) / **SUSPECTED** (duplicate of a blob over 256 bytes; no such data in the test set) · `components/grid/DataGrid.vue:729-742`, `components/table/RecordPanel.vue:72-76`, `components/table/TableView.vue:459-484`

`startEdit` blocks binary cells, but `paste()` does not check `isBinary`, and the record panel renders a blob as an editable input holding `0x…`.

Repro: select a `payment_payload` cell and paste `0xdeadbeef`. Preview: `UPDATE ... SET payment_payload = '0xdeadbeef'`, which stores the ten characters, not four bytes. Copying a row with a blob and pasting it onto another row does the same, so a row copy corrupts the blob.

Also: `duplicateRows` skips truncated long text (line 474) but copies binary values as-is. A blob over 256 bytes arrives with `truncated: true` and only its first 256 bytes, so the duplicate would be inserted with a cut blob.

Fix: skip binary target cells in `put()` unless the text parses as hex, and then send `{$type:"binary", hex}`. Make blob fields read-only in the record panel. In `duplicateRows`, skip `isBinary(v) && v.truncated` like truncated text, or load the full values first.

### 6. Opening a JSON cell and clicking outside stages a change nobody made
**VERIFIED** · `components/grid/CellPopover.vue:44-47`, `:29`, `:53-59`

On mount the popover pretty-prints JSON into `text`. `dirty` compares `text` with the raw `initial`, so it is true at once, and a click outside commits when dirty.

Repro: double-click an `extra_data` cell in `sales_order`, then click the toolbar. The cell turns amber and an UPDATE with the re-indented JSON is staged.

Fix: compare against the text as shown after formatting (store the formatted baseline), or compare parsed JSON. On commit of a JSON column, compare semantically with the original and send the minified form.

### 7. Copy and paste does not round-trip NULL
**VERIFIED** · `lib/format.ts:69-76` (`cellText`), `components/grid/DataGrid.vue:721-725`

TSV and CSV copy write NULL as an empty field. Paste turns an empty field into `""` for string, text and json columns. So copying a row and pasting it onto another row converts every NULL text value to `''`. The reverse also holds: a pasted literal `NULL` always becomes SQL NULL, so the string "NULL" cannot be pasted.

Repro: copy a NULL `coupon_code` cell, paste it on a cell that has a value: staged `coupon_code = ''`.

Fix: copy NULL as `NULL` in TSV for in-app use, or put a private clipboard format next to the plain text (a custom MIME type with the typed values) and prefer it on paste. At minimum, treat an empty pasted field as NULL when the column is nullable and the source was the app.

### 8. Cmd+V depends on `navigator.clipboard.readText`; the paste-event path can never run
**SUSPECTED** for the desktop build (not run in WKWebView); the dead path is confirmed by code and in the browser · `components/grid/DataGrid.vue:782`, `:832-840`, `lib/clipboard.ts:30-36`

The keydown handler calls `preventDefault()` on Cmd+V, which suppresses the native `paste` event, so `onPasteEvent` is unreachable from the keyboard. Every paste goes through `readText()`. In WKWebView that shows a "Paste" confirmation bubble on each call or rejects, and then the user gets "Clipboard access was denied. Allow paste in the browser prompt", which makes no sense in a desktop app. In the preview browser the permission state was `denied`.

Fix: do not handle Cmd+V in keydown. Let the `paste` event deliver `clipboardData` (no prompt, works in WKWebView). Keep a Wails `Clipboard.Text()` call for the context menu and toolbar paste, where there is no paste event.

### 9. "Delete" in the Edit connection dialog does not delete, and Cancel closes the dialog
**VERIFIED** · `components/connection/ConnectionModal.vue:18`, `:198-215`, `stores/ui.ts:122-128`

`connId` is computed from `ui.modal.props.connId`. `ui.confirm()` replaces `ui.modal` with the confirm dialog, so the connection modal unmounts and `connId` becomes `""`.

Repro: Edit connection, click Delete, confirm. The calls are `tabs.closeConnection("")` and `projects.deleteConnection("")`; the connection stays. Click Delete and then Cancel instead: the edit dialog is gone, with any unsaved field changes.

Fix: capture the id in a local constant at mount. Make `ui.confirm` stack on top of the current modal (keep a modal stack, or render confirm separately from `ui.modal`) so the caller's dialog survives.

### 10. One stray Backspace after a header click stages NULL for the whole column
**VERIFIED** · `components/grid/DataGrid.vue:364-369`, `:818-821`, `:649-672`

A plain header click selects the entire column and also sorts, which re-queries and resets to page 1. With the column selected, Delete or Backspace sets every nullable cell in the selection to NULL.

Repro: click the `customer_firstname` header (the grid re-sorts), press Backspace: "500 pending changes". Cmd+A followed by Backspace stages NULL for every nullable cell on the page. It is staged and undoable in one step, but nothing asks first, and a 500-row UPDATE is one Cmd+S and Enter away.

Evaluation of the three design choices:
- Header click doing both select and sort: drop it. Make a header click sort only (like TablePlus and DataGrip), and select the column with Cmd-click or from the header context menu. A mis-click now costs a query and the scroll position.
- Delete/Backspace = set NULL: keep it for a single cell at most. For more than one cell, require an explicit action (context menu "Set NULL", or a dedicated shortcut such as Alt+Backspace), or confirm above a threshold.
- `navigator.clipboard.readText`: see item 8.

### 11. Table view starts work after it was closed, and leaks a window key listener
**VERIFIED** · `components/table/TableView.vue:208-221`

`onMounted` awaits `restoreState()` and `ensureConnected()` before it registers the key listener and calls `load()`. If the tab closes during that wait, `onBeforeUnmount` runs first (removing nothing), then the continuation adds the listener and loads data for a dead view.

Repro: open a table on the not-yet-connected Postgres connection and close the tab at once. A status-bar entry for the closed tab id appears after the connect completes, which shows `load()` ran after unmount. The `keydown` listener stays on `window` for the lifetime of the app, with the component's closure.

Related: until the connection resolves, the tab has no shortcuts at all (Cmd+F, Cmd+R, Cmd+S). For an "ask" connection that means for as long as the password prompt is open.

Fix: register the listener synchronously in `onMounted`, keep an `unmounted` flag, and return after each await when it is set.

### 12. SSH host-key trust is not wired up, so a first connection through a tunnel cannot succeed
**SUSPECTED** (no SSH host to test; confirmed that no code handles it) · `api/index.ts:143-145`, no callers

`onSshHostKey`, `connectionApi.TrustHostKey` and the error codes `host_key_unknown` and `host_key_mismatch` are not referenced anywhere outside `api/index.ts`. A first connect to an unknown host rejects with `host_key_unknown`; the user sees a raw error toast and has no way to accept the key.

Fix: in `ensureConnected` and in the connection modal's Test, catch `host_key_unknown`, show the fingerprint in a confirm dialog, call `TrustHostKey` and retry. Show `host_key_mismatch` as a hard error with no trust option.

---

## Medium

### 13. Cmd+S does nothing while a cell editor or the value popover is open
**VERIFIED** · `components/grid/DataGrid.vue:616-617`, `components/grid/CellPopover.vue:78`

Both handlers call `stopPropagation()` on every key, so the window-level Cmd+S handler in TableView never sees it. Type a value, press Cmd+S: nothing happens and the value is not committed. In the browser the native "Save page" dialog opens.

Fix: on Cmd+S in the editor, commit the edit and then let the event through (or call save directly). Only stop the keys the editor handles itself.

### 14. A bad WHERE is saved before it is validated, and the grid keeps showing the old rows
**VERIFIED** · `components/table/TableView.vue:242-247`, `:125-128`

`applyClauses` calls `saveFilters()` before `load()`. With `nope_col = 1` on `admin_user`: the error bar and a toast both show the error, the grid still shows the previous unfiltered 5 rows with "1–5 of 5", and the bad filter is stored. Close and reopen the table: a full-pane "Could not load" with a Retry button that can never work until the WHERE input is cleared.

Fix: persist filters only after a successful load. On a WHERE error, dim or clear the stale rows so they are not read as the filtered result, and show the error once.

### 15. TSV export header option is inverted and drops a data row
**VERIFIED** · `components/table/TableView.vue:688-702` (line 700)

`formatCopy("tsv")` never writes a header, yet `exportText` strips the first line when "Include column names" is off. With the box on, the TSV has no header. With it off, the first data row is missing. CSV is correct.

Fix: use `tsv_header` or `tsv` according to the flag and strip nothing.

### 16. Export defaults to "Selection" whenever a cell is active
**VERIFIED** · `components/table/TableView.vue:685`, `components/table/ExportModal.vue:20`

`hasSelection` is `cellCount > 0`, which is true after any click. Opening Export with one active cell preselects "Selection", so Download writes a one-cell file.

Fix: default to "All rows"; preselect "Selection" only for more than one row or a multi-cell range.

### 17. Saved column widths are overwritten by the last resize
**VERIFIED** · `components/grid/DataGrid.vue:66`, `:395-396`, `:411-412`

`userWidths` starts empty for each grid instance, and only that object is emitted and stored. Resize `category_id`: stored `{"category_id":236}`. Reopen the table and resize `product_id`: stored `{"product_id":221}`. The first width is gone.

Fix: seed `userWidths` from `initialWidths`, or merge in `onWidths`.

### 18. Connection accent colors collide with the staged-change colors
**VERIFIED** (screenshots, dark and light) · `styles/theme.css:108-119`, `:21-23`

Under `data-accent`, `--selbg` is the accent at 22%. `--chg-new` is green at 20%, `--chg-del` red at 20%, `--chg-edit` amber. In a green connection a selected range looks the same as new rows; in a red one it looks like rows marked for deletion; in yellow and orange it looks like edited cells. On the production (red) connection this is the worst possible ambiguity.

Fix: keep the selection fill neutral (the default blue-grey, or a neutral overlay) and use the accent only for the selection border, tab stripe, active sidebar row and focus ring.

### 19. Keyboard focus is lost after the palette, so the app is not keyboard-first
**VERIFIED** · `components/palette/CommandPalette.vue:39-41`, `:225-230`, `components/table/TableView.vue` (no focus on activate)

Cmd+K then Esc leaves focus on `<body>`. Cmd+K, type a table name, Enter: the tab opens and focus is again on `<body>`. Arrow keys, typing and Cmd+C do nothing until the user clicks the grid. The same holds for Ctrl+Tab and Cmd+1..9. There is a shortcut to focus the sidebar (Cmd+E) but none for the grid.

Fix: restore the previous focus when the palette closes without a pick. Focus the grid (CodeMirror for consoles) when a view becomes active and after its first load, selecting cell 0,0 when nothing is selected.

### 20. Keys reach the grid while a context menu is open
**VERIFIED** · `components/grid/ContextMenu.vue:46-69`

The menu handles Esc, arrows and Enter only. With the menu open, Backspace staged a NULL in the cell underneath, and a letter key opens the cell editor behind the menu.

Fix: swallow all key events while a menu is open (`stopPropagation` and `preventDefault` for everything not handled), or move focus into the menu.

### 21. Popovers sit above modals and the palette, and lose the connection accent
**VERIFIED** · `components/grid/CellPopover.vue` (z-index 250), `ColumnFilterPopover.vue` (260), `grid/ContextMenu.vue` (300), `common/Modal.vue:64` (60), `CommandPalette.vue:260` (65)

With the value popover open, Cmd+K opens the palette underneath it. Because these components are teleported to `<body>`, they also fall outside `data-accent`: in the red production tab the context menu highlight is the default blue (see the light-theme screenshot).

Fix: one z-index scale in `theme.css` (menus above popovers above modals, or close popovers when an overlay opens). Pass the accent to teleported layers by setting `data-accent` on their root.

### 22. Confirm dialogs put default focus on the destructive button
**VERIFIED** · `components/common/ConfirmModal.vue:17`

`data-autofocus` is on the confirm button, also when `danger` is set. Enter confirms "Discard", "Delete connection", "Revert all" and "Save changes to a production connection". For the red-connection save, the extra confirmation is defeated by Cmd+S, Enter.

Fix: focus Cancel when `danger` is true. For production saves, require a deliberate action (Cmd+Enter, or typing the connection name).

### 23. Structure view: rename input is not focused, and key icons break the row height
**VERIFIED** · `components/structure/StructureView.vue:359`, `:357`

Rename relies on the `autofocus` attribute, which does nothing for an element inserted later. After clicking Rename, focus stays on the button; the user has to click the input, and since commit happens on blur, the input stays open until they do.

Tailwind's preflight sets `svg { display: block }`. In the Name column the PK or FK icon therefore sits on its own line above the name, and those rows are 29px instead of 24px (first screenshot).

Fix: focus via a template ref in `nextTick`. Give the grid `Icon` component `display: inline-block; vertical-align: middle`, or wrap the cell content in a flex row.

### 24. Structure preview can show SQL that is not what will run
**SUSPECTED** (not triggered) · `components/structure/StructureView.vue:199-207`

`previewStatements` catches every error from `PreviewStructureChanges`, including validation errors such as an invalid type, and falls back to SQL generated on the client. The dialog then shows statements the backend will not run, and the real error only appears on Run.

Fix: fall back only for `not_implemented` (which no longer occurs); show every other error in the dialog.

### 25. Copy formats lose or distort values
**VERIFIED** (JSON, multi-range, long text) / **SUSPECTED** (blobs over 256 bytes) · `lib/copyFormats.ts:81-87`, `:50-59`, `:102-108`

- JSON copy writes json columns as escaped strings (`"extra_data": "{\"utm\": ...}"`) and decimals as strings. The backend export embeds JSON and writes exact numbers.
- Truncated values are copied as their cut form in every format with no warning: 8,192 of 11,400 characters for long text, and the first 256 bytes for blobs. In SQL INSERT that gives a statement that silently writes a shorter value.
- With a multi-range selection, SQL INSERT fills unselected cells with NULL: `(increment_id, status) VALUES ('100000001', NULL)`.
- SQL literals quote decimals (`'89.2600'`); `column.kind` is not used.

Fix: use `column.kind` (embed json, emit numerics unquoted). Before copying, load full values for truncated cells or refuse with a toast. For multi-range SQL, emit only the selected columns per row.

### 26. Column filters are lost on restart, and filters reappear when a table is reopened
**SUSPECTED** (code) · `components/table/TableView.vue:176-193`, `:201-206`

`saveFilters` writes `where: ""` into the tab, so a restored tab never satisfies `tab.where === undefined` and skips the stored filters; its column filters are gone after a restart. A table opened fresh from the sidebar does apply the last filters. FK navigation also clears `columnFilters` without saving (`:231`).

Fix: keep column filters in the tab state, or always restore from the `filters:` key unless the tab was opened with an explicit where.

### 27. Exact count can show a number for the wrong filter
**SUSPECTED** (timing not reproduced) · `components/table/TableView.vue:163-173`

`countExact` has no sequence guard and is not cancelled. If the filter, sort or page changes while it runs, the old count lands in `total` and the range text shows it for the new filter. The call also keeps running after the tab closes.

Fix: keep the promise, cancel it in `load()` and on unmount, and ignore results from an older sequence.

### 28. Export keeps running after Cancel and may close a different dialog
**SUSPECTED** (code) · `components/table/ExportModal.vue:28-53`

Cancel only closes the modal; the `Export` promise is never cancelled. When it later resolves, `run()` calls `ui.closeModal()`, which closes whatever modal is open at that time, for example a Save preview.

Fix: keep the promise, cancel it on close, and close only if this modal is still the open one.

### 29. Dialogs that bypass `ui.modal` leave global shortcuts active
**SUSPECTED** (code) · `components/structure/StructureView.vue:464-470`, `components/sidebar/Sidebar.vue:882-891`, `App.vue:77`

The structure Save dialog and the project name dialog are local `<Modal>` instances. `ui.modal` stays null, so the App handler still acts on Cmd+W (closes the tab under the dialog), Cmd+T and Cmd+1..9, and Cmd+K opens the palette on top.

Fix: route these through `ui.openModal`, or have `Modal.vue` register itself in an open-overlay counter that the global handlers check.

---

## Low

### 30. Read-only connections: row actions are enabled and blocked attempts spam toasts
**VERIFIED** · `components/table/TableView.vue:751-753`, `:631-674`

Writes are impossible: every path was tried on the read-only `store` table (Enter, typing, double-click, Delete, paste by API and by event, Cmd+N, Cmd+D, Cmd+Backspace, Set NULL, Set default, toolbar buttons, record panel, Cmd+S) and nothing was staged. But Add, Duplicate and Delete row are enabled, and the context menu offers Edit, Set NULL, Set default, Duplicate and Delete. Every blocked keypress adds another "Connection is read-only" toast; 17 were stacked after the test.

Fix: disable those controls when `!editable`, and show one toast at a time for the same message.

### 31. Speed search and the palette only cover expanded or connected connections, without saying so
**SUSPECTED** (code) · `components/sidebar/Sidebar.vue:118-125`, `components/palette/CommandPalette.vue:209-222`

Speed search uses the index of expanded connections and schemas only. With nothing expanded, typing shows "0 matches". The palette says "No results." when no connection is open.

Fix: say so in the empty state ("Connect to search 3 more connections", with Enter to connect), or connect lazily for `passwordMode: "save"` connections.

### 32. Closing the connection dialog by clicking the backdrop discards the form
**VERIFIED** · `components/common/Modal.vue:54-58`, `components/connection/ConnectionModal.vue:287`

Fix: `:close-on-overlay="false"` for the connection form, or confirm when the form is dirty.

### 33. Closing a console tab discards its SQL without asking
**SUSPECTED** (code) · `components/console/SqlConsole.vue:336-340`, `:357-363`

Console tabs are never dirty, so Cmd+W closes them at once. `onBeforeUnmount` also clears the 400 ms save timer, so the last keystrokes are not written to the tab state on a project switch.

Fix: flush the pending save on unmount; consider a "reopen closed tab" action or a confirm for non-empty unsaved SQL.

### 34. Ctrl is treated as Cmd on macOS
**SUSPECTED** (code) · `App.vue:68`, `components/table/TableView.vue:592`, `components/grid/DataGrid.vue:769`

`mod = metaKey || ctrlKey` everywhere. On macOS Ctrl+K (kill line), Ctrl+W, Ctrl+T, Ctrl+F and Ctrl+A are text-editing keys in inputs and CodeMirror; here they open the palette, close the tab, open a console, open find and select all.

Fix: use `metaKey` on macOS and `ctrlKey` elsewhere, from one shared helper.

### 35. Leaked `blur` listener per context menu
**SUSPECTED** (code) · `components/common/ContextMenu.vue:63`

`window.addEventListener("blur", () => emit("close"))` uses an anonymous function and is never removed. Each sidebar or tab menu adds one for the lifetime of the app.

Fix: name the handler and remove it in `onBeforeUnmount`, as the grid menu does.

### 36. Password prompts cancel each other
**SUSPECTED** (no "ask" connection in the test data) · `stores/ui.ts:139-146`, `stores/projects.ts:134-152`, `components/table/TableView.vue:139-141`

Every restored tab of an "ask" connection calls `ensureConnected` on mount; each `askPassword` resolves the previous one with null. After a disconnect, the Retry button calls `load()` only, which fails again with `password_required` and never prompts.

Fix: share one pending connect promise per connection id; make Retry go through `ensureConnected`.

### 37. Stale schema data
**SUSPECTED** (code) · `components/sidebar/schemaCache.ts:93-98`, `components/console/SqlConsole.vue:349`

`invalidate()` does not drop in-flight loads, so a load that finishes afterwards writes old data back. The console loads its autocomplete index once at mount, under the key `""`, while the sidebar uses the real schema name; that is a second `SearchSchema` call and a second copy, and neither is refreshed after a structure change.

Fix: bump a generation per connection in `invalidate` and discard older results; resolve `""` to `defaultSchema` before keying.

### 38. Find in database lists connections from every project and shows no progress
**SUSPECTED** (code) · `components/table/FindInDatabaseModal.vue:21`, `:123-124`, `:45`

The picker uses `projects.connections`, not `projectConnections`, and defaults to the first of all. `onFindProgress` is never used, so a long search shows only "Searching…". Line 45 has a filter that is always true.

### 39. Selection details
**SUSPECTED** (code) · `components/grid/DataGrid.vue:313-316`, `:244`, `:783`

Cmd-click on a selected cell adds a second range instead of deselecting, and overlapping ranges are counted twice in `cellCount`. Cmd+X copies but does not clear, so it behaves like Cmd+C.

### 40. Quick find misses text that is not rendered
**SUSPECTED** (code) · `lib/format.ts:40`, `:79-84`

Find matches the rendered text, which is cut at 400 characters, and skips blobs. A match further into a long value is not found and the count gives no hint.

### 41. The project name changes before the discard question is answered
**VERIFIED** · `components/sidebar/Sidebar.vue:651`, `App.vue:50-58`

Switching project with dirty tabs sets the active project first, then asks. While the dialog is open, the sidebar shows the new project next to the old project's tabs; Cancel switches back and writes `ui:activeProject` twice.

Fix: ask before calling `setActiveProject`.

### 42. Duplicate components and helpers from the two authors
**VERIFIED** (code) · `components/common/Icon.vue` and `components/grid/Icon.vue`; `components/common/ContextMenu.vue` and `components/grid/ContextMenu.vue`

- Icons: a 16-unit grid at stroke 1.4 versus a 24-unit grid at stroke 1.9, and different glyphs for the same names (`lock`, `key`, `trash`, `copy`, `table`, `search`, `x`). The sidebar and tab bar look lighter than the toolbar and grid.
- Menus: an id-based API with `@select` versus `run()` callbacks; z-index 70 versus 300; `text-white` versus `--ac-fg`; only the grid menu has submenus.
- `quoteIdent` and a qualified-name helper exist in both `schemaCache.ts` and `lib/copyFormats.ts`, with different handling of SQLite `main`. The sidebar has its own clipboard copy next to `lib/clipboard.ts` (`Sidebar.vue:506-521`).

Fix: one Icon set and one ContextMenu (the grid one has more features) in `components/common`.

### 43. Polling, dead code and casts that hide errors
**VERIFIED** (code)

- `components/table/registerModals.ts:9-20` polls every 20 ms for the Pinia instance, as a side effect of importing TableView. If a modal opens before any table view was imported, the user gets "This dialog is not available yet". Register from `App.vue` or `main.ts` after `app.use(pinia)`.
- `components/palette/CommandPalette.vue:37` keeps state on `globalThis.__soOk`.
- Fallbacks for `not_implemented` that can no longer run: `StructureView.vue:60-89` and `:217-225` (this one runs DDL through `runQuery`), `localStatements` (`:170-187`), and several error strings.
- `stores/changes.ts:108-114` has an empty `if` in `cleanup`; `save()` is unused; `history: unknown[]` is cast back on use.
- `TableView.vue:711` uses `as unknown as CellValue`, `FindInDatabaseModal.vue:78` uses `as unknown as FindResult[]`, and several `QueryResult` literals are cast instead of built with a helper.
- `Sidebar.vue:221` writes a ref inside a computed.
- `styles/theme.css` still defines `--ctxbar-h`; CONTRACT.md §7 and §8 still describe a context bar that the requirements removed.
- `connectionApi.UseDatabase` is not used: there is no database switcher.

---

## UX polish

Visual
- **White text on accent colors.** `TabBar.vue:189`, `TableView.vue:856`, `StructureView.vue:478`, `SqlConsole.vue:506`, `ConnectionModal.vue:492`, and the inline `kbd` styles in `SaveChangesModal.vue:53`, `ColumnFilterPopover.vue:92` and `CellPopover.vue:117` use `#fff`. On green, yellow and blue accents the button text is dark (`--ac-fg`), so the badge and the key hint are white on a light fill. Use `--ac-fg` with `color-mix` for the muted variants.
- **Light theme accents.** Red and purple have no light override; white on `#a371f7` is about 3.4:1. Connection dots and tab stripes use the dark-theme values in light mode.
- **Column widths.** Default widths come from header text plus type, so `entity_id int unsigned` is 179px wide for one-digit values. Size from content first and let the type label truncate. Widths are fractional (`179.476px`), which blurs the 1px grid lines; round them.
- **Header height.** The header is 34px with one line of text. Either stack name and type, as the height suggests, or go to 26px.
- **Placeholders read as content.** The WHERE and ORDER BY placeholders (`status = 'pending' AND grand_total > 100`, `created_at DESC`) look like an active filter in the screenshots. Use a lighter placeholder color and shorter hints.
- **Toasts.** Bottom centre, over the grid's last rows and the horizontal scrollbar. Move them above the status bar on the right, collapse repeats ("Connection is read-only ×5"), and give errors a close button.
- **MySQL tree.** Each MySQL connection lists every database on the server as a sibling of the configured one (40 here). Show the configured database only, with an "other databases" group that is collapsed.
- **Loading.** The only signal is a 2px bar at the top of the grid. On first load show skeleton rows; on reload dim the old rows.
- **Empty table.** Only "This table is empty". Add a hint for the next step (Cmd+N to add a row) when the table is editable.
- **Error after a bad WHERE.** Shown twice (bar and toast). Keep the bar and underline the input.
- **Tab titles.** `sales_order (struct…` is cut at 220px. Use a structure icon instead of the suffix.
- **Status bar.** "Query: 3 ms" and "500 rows of 2,000" repeat the toolbar range. Show the cursor position and selection summary there instead (row, column, "3 × 2 cells").

Interaction
- **Enter after editing** moves down. Tab does not open the next cell for editing; offer Enter to commit and stay, and Tab to commit and edit the next cell, which is what makes filling a new row fast.
- **New rows** are appended after the last loaded row, so on a 500-row page the user is scrolled to the bottom and sees the row out of context. Pin new rows in a strip at the top or bottom of the viewport.
- **Staged changes off screen.** Updates on other pages are counted but cannot be seen. Add a "Show changes" filter, or make the pending badge open the SQL preview.
- **Redo.** There is Cmd+Z but no Cmd+Shift+Z.
- **Context menus.** No type-ahead, no keyboard navigation inside submenus, and a long submenu ("Show referencing rows" on a central table) runs off the bottom of the window (`grid/ContextMenu.vue:128`).
- **Filter popover.** Does not follow the column on scroll. Offer "Filter by this value" on Alt-click of a cell.
- **Value popover.** Its position is computed once, so it stays put when the grid scrolls. Close it on scroll or re-anchor.
- **WHERE autocomplete.** Enter applies the clause even when a suggestion is highlighted; only Tab accepts. DataGrip accepts with Enter. It also suggests column names only, no operators or keywords.
- **Console.** Cmd+Enter works only with focus in the editor, not from the result grid. Results have no quick find. Running a query replaces all result tabs; allow pinning one.
- **Sidebar.** After Enter opens a table, focus stays in the tree. That suits browsing, but there is no key to jump to the grid. Add one (for example Cmd+L for the WHERE input and Esc from the tree to return to the grid).
- **Save preview.** Long lists of statements are not virtualized or collapsed; 500 UPDATEs become 500 DOM blocks. Group them ("500 × UPDATE sales_order SET customer_firstname = NULL") with expand.
