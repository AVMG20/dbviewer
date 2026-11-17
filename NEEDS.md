# NEEDS

Frontend agents: list API / shared-type / dependency requests here (see CONTRACT.md §1).
One entry per need: who, what, why, proposed signature. The backend engineer moves
done items to the "Done" section.

## Open

### data agent
- **Shell: modal registration.** `components/table/registerModals.ts` registers `saveChanges`, `export` and `findInDatabase`
  with `ui.registerModal` at app start (it polls for the active pinia). If you prefer, call `registerDataModals()` from App.vue.

## Done

- (data) Export "all rows": confirmed. For a `page` source `limit`/`offset` are ignored entirely; the whole
  filtered table (where/orderBy/columnFilters) is streamed. — backend
- (data) FindInDatabase, CellValue, CopyAsSQL: implemented (and every other ⏳ endpoint), see CONTRACT.md §4.
  FindInDatabase also emits `find:progress` (`onFindProgress`). — backend
