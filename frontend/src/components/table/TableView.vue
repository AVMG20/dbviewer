<script setup lang="ts">
// Table data tab (data agent): toolbar (refresh, paging, WHERE, ORDER BY, quick find, column
// filters, add/duplicate/delete row, revert, Save, Copy as, Export, record panel) + DataGrid.
// Nothing is written until Save: edits go to stores/changes and are previewed first.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import type { CancellablePromise } from "@wailsio/runtime";
import {
  apiError, dataApi, type Driver, projectApi, schemaApi, SQL_DEFAULT,
  type CellValue, type Column, type ColumnFilter, type ForeignKey, type PageRequest, type QueryResult,
} from "@/api";
import { isBinary, isLongText } from "@/api";
import { useChangesStore, makeRowKey } from "@/stores/changes";
import { useProjectsStore } from "@/stores/projects";
import { useTabsStore } from "@/stores/tabs";
import { useUiStore } from "@/stores/ui";
import { isMod } from "@/lib/keys";
import { toastOnce } from "@/lib/notify";
import type { Tab } from "@/types";
import { COPY_FORMATS, formatCopy, qualifiedTable, quoteIdent, sqlLiteral } from "@/lib/copyFormats";
import { cellText, fmtNum, isDefaultValue } from "@/lib/format";
import type { AppliedEdits, GridRow, MenuItem, SelectionInfo, SortSpec } from "@/lib/gridTypes";
import DataGrid from "@/components/grid/DataGrid.vue";
import ContextMenu from "@/components/common/ContextMenu.vue";
import Icon from "@/components/common/Icon.vue";
import ColumnFilterPopover from "./ColumnFilterPopover.vue";
import RecordPanel from "./RecordPanel.vue";
import NavMenu from "./NavMenu.vue";
import { incomingForRow, loadLinks, navEntries, navigableColumns, type Links, type NavEntry } from "@/lib/fkNav";
import SqlClauseInput from "./SqlClauseInput.vue";

const props = defineProps<{ tab: Tab; active: boolean }>();

const projects = useProjectsStore();
const tabs = useTabsStore();
const ui = useUiStore();
const changes = useChangesStore();

const connId = computed(() => props.tab.connId);
const schema = computed(() => props.tab.schema ?? "");
const table = computed(() => props.tab.table ?? "");
const conn = computed(() => projects.getConnection(connId.value));
const driver = computed(() => (conn.value?.driver ?? "") as Driver | "");
const readOnly = computed(() => projects.isReadOnly(connId.value));
const tint = computed(() => projects.connColor(connId.value));
const hasTint = computed(() => !!conn.value && conn.value.color !== "none");
const storageKey = computed(() => `${connId.value}:${schema.value}.${table.value}`);

changes.ensure(props.tab.id, { connId: connId.value, schema: schema.value, table: table.value });

// ---- state --------------------------------------------------------------------------
const grid = ref<InstanceType<typeof DataGrid> | null>(null);
const result = shallowRef<QueryResult | null>(null);
const colMeta = ref<Column[]>([]);
const loading = ref(false);
/** the last load failed because of the WHERE / filters: the rows shown are stale */
const stale = ref(false);
const loadError = ref("");
const whereError = ref("");
const whereDraft = ref(props.tab.where ?? "");
const orderDraft = ref(props.tab.orderBy ?? "");
const applied = ref({ where: props.tab.where ?? "", orderBy: props.tab.orderBy ?? "" });
const sorts = ref<SortSpec[]>([]);
const columnFilters = ref<ColumnFilter[]>([]);
const limit = ref(500);
const offset = ref(0);
const total = ref(-1);
const estRows = ref(-1);
const counting = ref(false);
const findOpen = ref(false);
const findText = ref("");
const findCount = ref(0);
const findInput = ref<HTMLInputElement | null>(null);
const recordOpen = ref(false);
const sel = ref<SelectionInfo>({ active: null, rowCount: 0, colCount: 0, cellCount: 0 });
const initialWidths = ref<Record<string, number>>({});
const refs = ref<ForeignKey[] | null>(null);
const links = ref<Links | null>(null);
const navMenu = ref<{ x: number; y: number; entries: NavEntry[] } | null>(null);
const filterPop = ref<{ col: number; rect: DOMRect } | null>(null);
const copyMenu = ref<{ x: number; y: number } | null>(null);
const gridMenu = ref<{ x: number; y: number } | null>(null);

const columns = computed(() => result.value?.columns ?? []);
const colNames = computed(() => columns.value.map((c) => c.name));
const pkIdx = computed(() => columns.value.map((c, i) => (c.pk ? i : -1)).filter((i) => i >= 0));
const hasPk = computed(() => pkIdx.value.length > 0);
const editable = computed(() => !!result.value && hasPk.value && !readOnly.value);
const q = (name: string) => quoteIdent(driver.value, name);
const pending = computed(() => changes.count(props.tab.id));

// ---- loading ------------------------------------------------------------------------
let inflight: CancellablePromise<QueryResult> | null = null;
let countReq: CancellablePromise<number> | null = null;
let loadSeq = 0;
let unmounted = false;
/** persist where/order/filters only once a load with them succeeded */
let persistPending = false;
let restoredFilters = false;

function pageRequest(): PageRequest {
  return {
    connId: connId.value,
    schema: schema.value,
    table: table.value,
    where: applied.value.where,
    orderBy: applied.value.orderBy,
    limit: limit.value,
    offset: offset.value,
    columnFilters: columnFilters.value,
    countTotal: false,
  };
}

async function load(opts: { keepTotal?: boolean } = {}) {
  if (unmounted) return;
  inflight?.cancel();
  countReq?.cancel();
  counting.value = false;
  const seq = ++loadSeq;
  loading.value = true;
  loadError.value = "";
  whereError.value = "";
  const filtered = !!applied.value.where.trim() || columnFilters.value.length > 0;
  const req = pageRequest();
  req.countTotal = (estRows.value >= 0 && estRows.value <= 100_000) || (estRows.value < 0 && !filtered);
  if (opts.keepTotal && total.value >= 0) req.countTotal = false;
  const p = dataApi.FetchPage(req);
  inflight = p;
  try {
    const r = await p;
    if (seq !== loadSeq) return;
    result.value = r;
    stale.value = false;
    restoredFilters = false;
    ui.setStatus({ tabId: props.tab.id, message: undefined });
    if (persistPending) {
      persistPending = false;
      saveFilters();
    }
    if (r.total >= 0) total.value = r.total;
    else if (!opts.keepTotal || total.value < 0) total.value = r.hasMore ? -1 : r.offset + r.rows.length;
    ui.setStatus({ tabId: props.tab.id, durationMs: r.durationMs, rows: r.rows.length, total: total.value >= 0 ? total.value : undefined });
  } catch (e) {
    if (seq !== loadSeq) return;
    const err = apiError(e);
    if (err.code === "canceled") return;
    const hasClauses = !!(applied.value.where || applied.value.orderBy || columnFilters.value.length);
    if (err.code === "db_error" && hasClauses && restoredFilters) {
      // a saved filter that no longer works must not lock the table: drop it and reload
      restoredFilters = false;
      applied.value = { where: "", orderBy: "" };
      whereDraft.value = orderDraft.value = "";
      columnFilters.value = [];
      sorts.value = [];
      saveFilters();
      ui.toast("The saved filter no longer works and was removed: " + err.message, "warning");
      return void load();
    }
    if (err.code === "db_error" && hasClauses && result.value) {
      // drop the generated query tail the driver echoes ("...) ORDER BY `id` LIMIT 501 OFFSET 0")
      whereError.value = err.message.replace(/\s*\)?\s*ORDER BY\s[\s\S]*$/, "").replace(/\s+$/, "");
      stale.value = true;
      ui.setStatus({ tabId: props.tab.id, message: "Query failed", rows: undefined, total: undefined });
    } else loadError.value = err.code === "not_connected" || err.code === "password_required" ? `${err.message || err.code}` : err.message || err.code;
  } finally {
    if (seq === loadSeq) loading.value = false;
  }
}

function resetAndLoad() {
  offset.value = 0;
  total.value = -1;
  load();
}
function refresh() {
  load();
}
async function retry() {
  if (await projects.ensureConnected(connId.value)) {
    loadError.value = "";
    loadMeta();
    load();
  }
}
/**
 * The grid only takes focus when the opener asked for it (tabs.consumeFocus: Enter, double-click, palette, FK nav).
 * Loads, refreshes and plain tab switches never steal focus from the sidebar or an input.
 */
let wantFocus = false;
function requestFocus() {
  wantFocus = !!((tabs as unknown as { consumeFocus?: (id: string) => boolean }).consumeFocus?.(props.tab.id) ?? false);
}
function focusGridSoon() {
  if (!wantFocus) return;
  nextTick(() => {
    if (!props.active || overlayOpen.value || !grid.value) return;
    wantFocus = false;
    grid.value.focus(); // focus only: the selection stays empty until a click or an arrow key
  });
}
watch(() => props.active, (a) => {
  if (!a) return;
  requestFocus();
  if (result.value) focusGridSoon();
});

async function loadMeta() {
  try {
    colMeta.value = await schemaApi.Columns(connId.value, schema.value, table.value);
  } catch {
    colMeta.value = [];
  }
  try {
    const ts = await schemaApi.Tables(connId.value, schema.value, false);
    estRows.value = ts.find((t) => t.name === table.value)?.estRows ?? -1;
  } catch {
    estRows.value = -1;
  }
  const l = await loadLinks(connId.value, schema.value, table.value);
  links.value = l;
  refs.value = l.inn;
}

async function countExact() {
  if (counting.value) return;
  counting.value = true;
  const seq = loadSeq;
  const p = dataApi.CountRows(pageRequest());
  countReq = p;
  try {
    const n = await p;
    if (seq === loadSeq && !unmounted) total.value = n;
  } catch (e) {
    const err = apiError(e);
    if (err.code !== "canceled") ui.toast(err.message || "Count failed", "error");
  } finally {
    if (countReq === p) {
      countReq = null;
      counting.value = false;
    }
  }
}

// ---- persistence (column widths, last filters) -----------------------------------------
async function restoreState() {
  try {
    const w = await projectApi.GetUIState(`colwidths:${storageKey.value}`);
    if (w) initialWidths.value = JSON.parse(w);
  } catch { /* ignore */ }
  // A fresh table starts unfiltered. Only a tab restored after an app restart (same tab id) gets its column
  // filters back; its WHERE / ORDER BY already live in the tab state.
  try {
    const f = await projectApi.GetUIState(`colfilters:${props.tab.id}`);
    if (f) columnFilters.value = JSON.parse(f);
  } catch { /* ignore */ }
  restoredFilters = !!(props.tab.where || props.tab.orderBy || columnFilters.value.length);
}
let widthTimer: number | undefined;
/** Interacting with a table tab turns a preview tab into a permanent one. */
function promote() {
  (tabs as unknown as { promote?: (id: string) => void }).promote?.(props.tab.id);
}
function onWidths(w: Record<string, number>) {
  promote();
  clearTimeout(widthTimer);
  widthTimer = window.setTimeout(() => {
    projectApi.SetUIState(`colwidths:${storageKey.value}`, JSON.stringify(w)).catch(() => undefined);
  }, 400);
}
function saveFilters() {
  const s = { where: applied.value.where, orderBy: applied.value.orderBy, columnFilters: columnFilters.value };
  projectApi.SetUIState(`colfilters:${props.tab.id}`, s.columnFilters.length ? JSON.stringify(s.columnFilters) : "").catch(() => undefined);
  tabs.update(props.tab.id, { where: s.where, orderBy: s.orderBy });
}

onMounted(async () => {
  if (props.active) requestFocus();
  // shortcuts work from the first moment (also while connecting / waiting for a password)
  window.addEventListener("keydown", onWinKey);
  await restoreState();
  if (unmounted) return;
  const connected = await projects.ensureConnected(connId.value);
  if (unmounted) return;
  syncSortsFromOrder();
  if (connected) {
    loadMeta();
    load().then(() => focusGridSoon());
  } else {
    loadError.value = "Not connected. Connect to this database and retry.";
  }
});
onBeforeUnmount(() => {
  unmounted = true;
  window.removeEventListener("keydown", onWinKey);
  inflight?.cancel();
  countReq?.cancel();
  changes.dispose(props.tab.id);
  ui.clearStatus?.(props.tab.id);
});

// tab.where / tab.orderBy changed from outside (FK navigation, palette): apply them
watch(
  () => [props.tab.where, props.tab.orderBy] as const,
  ([w, o]) => {
    if ((w ?? "") === applied.value.where && (o ?? "") === applied.value.orderBy) return;
    applied.value = { where: w ?? "", orderBy: o ?? "" };
    whereDraft.value = applied.value.where;
    orderDraft.value = applied.value.orderBy;
    columnFilters.value = [];
    syncSortsFromOrder();
    persistPending = true;
    resetAndLoad();
  },
);
// connection reconnects / config changes
watch(() => projects.statuses[connId.value]?.connected, (c, prev) => {
  if (c && !prev && loadError.value) load();
});

// ---- where / order by / sort ------------------------------------------------------------
function applyClauses() {
  promote();
  applied.value = { where: whereDraft.value.trim(), orderBy: orderDraft.value.trim() };
  syncSortsFromOrder();
  persistPending = true;
  resetAndLoad();
}
function sortsToOrder(s: SortSpec[]): string {
  return s.map((x) => `${q(colNames.value[x.col])}${x.dir === "desc" ? " DESC" : ""}`).join(", ");
}
function syncSortsFromOrder() {
  const text = applied.value.orderBy.trim();
  if (!text) return void (sorts.value = []);
  const out: SortSpec[] = [];
  for (const part of text.split(",")) {
    const m = /^\s*[`"]?([A-Za-z_][\w$]*)[`"]?\s*(asc|desc)?\s*$/i.exec(part);
    const idx = m ? colNames.value.indexOf(m[1]) : -1;
    if (!m || idx < 0) return void (sorts.value = []);
    out.push({ col: idx, dir: m[2]?.toLowerCase() === "desc" ? "desc" : "asc" });
  }
  sorts.value = out;
}
watch(colNames, syncSortsFromOrder);

function onSortSet(col: number, dir: "asc" | "desc" | null, additive: boolean) {
  promote();
  const cur = additive ? sorts.value.filter((s) => s.col !== col) : [];
  const next: SortSpec[] = dir ? [...cur, { col, dir }] : cur;
  sorts.value = next;
  const text = sortsToOrder(next);
  orderDraft.value = text;
  applied.value = { ...applied.value, orderBy: text };
  persistPending = true;
  resetAndLoad();
}
function onSort(col: number, additive: boolean) {
  promote();
  const cur = sorts.value;
  const i = cur.findIndex((s) => s.col === col);
  let next: SortSpec[];
  if (additive && cur.length) {
    if (i < 0) next = [...cur, { col, dir: "asc" }];
    else if (cur[i].dir === "asc") next = cur.map((s) => (s.col === col ? { ...s, dir: "desc" as const } : s));
    else next = cur.filter((s) => s.col !== col);
  } else if (cur.length === 1 && i === 0) {
    next = cur[0].dir === "asc" ? [{ col, dir: "desc" }] : [];
  } else next = [{ col, dir: "asc" }];
  sorts.value = next;
  const text = sortsToOrder(next);
  orderDraft.value = text;
  applied.value = { ...applied.value, orderBy: text };
  persistPending = true;
  resetAndLoad();
}

// ---- column filters ---------------------------------------------------------------------------
const filteredCols = computed(() => columnFilters.value.map((f) => f.column));
function onFilterColumn(col: number, rect: DOMRect) {
  filterPop.value = { col, rect };
}
function setColumnFilter(f: ColumnFilter) {
  promote();
  columnFilters.value = [...columnFilters.value.filter((x) => x.column !== f.column), f];
  filterPop.value = null;
  persistPending = true;
  resetAndLoad();
}
function removeColumnFilter(column: string) {
  promote();
  columnFilters.value = columnFilters.value.filter((x) => x.column !== column);
  filterPop.value = null;
  persistPending = true;
  resetAndLoad();
}
function clearAllFilters() {
  promote();
  columnFilters.value = [];
  persistPending = true;
  resetAndLoad();
}
const OP_LABEL: Record<string, string> = {
  eq: "=", ne: "≠", gt: ">", gte: "≥", lt: "<", lte: "≤", contains: "contains", not_contains: "does not contain",
  starts_with: "starts with", ends_with: "ends with", like: "like", not_like: "not like", is_null: "is NULL",
  not_null: "is not NULL", in: "in", not_in: "not in", between: "between",
};
function chipText(f: ColumnFilter): string {
  if (f.op === "is_null" || f.op === "not_null") return `${f.column} ${OP_LABEL[f.op]}`;
  if (f.op === "in" || f.op === "not_in") return `${f.column} ${OP_LABEL[f.op]} (${f.values.length})`;
  if (f.op === "between") return `${f.column} between ${f.values[0]} and ${f.values[1]}`;
  return `${f.column} ${OP_LABEL[f.op] ?? f.op} ${f.value}`;
}
function addCellFilter(row: number, col: number, op: "eq" | "ne" | "is_null" | "not_null") {
  const v = displayRows.value[row]?.values[col];
  const f: ColumnFilter = { column: columns.value[col].name, op, value: op === "eq" || op === "ne" ? cellText(v) : "", values: [] };
  setColumnFilter(f);
}

// ---- paging --------------------------------------------------------------------------------------
const rowCount = computed(() => result.value?.rows.length ?? 0);
const hasMore = computed(() => result.value?.hasMore ?? false);
const rangeText = computed(() => {
  if (!result.value) return "";
  if (stale.value) return "—";
  if (!rowCount.value) return "0 rows";
  const from = offset.value + 1;
  const to = offset.value + rowCount.value;
  let of: string;
  if (total.value >= 0) of = fmtNum(total.value);
  else if (!hasMore.value) of = fmtNum(to);
  else if (estRows.value > 0 && !applied.value.where && !columnFilters.value.length) of = "~" + fmtNum(estRows.value);
  else of = "many";
  return `${fmtNum(from)}–${fmtNum(to)} of ${of}`;
});
const isEstimate = computed(() => total.value < 0 && hasMore.value);
const canNext = computed(() => hasMore.value);
function page(dir: 1 | -1) {
  promote();
  const next = offset.value + dir * limit.value;
  if (next < 0) return;
  offset.value = next;
  load({ keepTotal: true });
}
function onLimit() {
  promote();
  offset.value = 0;
  load({ keepTotal: true });
}

// ---- display rows (loaded rows + staged changes + new rows) -------------------------------------------
const colIndex = computed(() => new Map(colNames.value.map((n, i) => [n, i])));

function rowKeyOf(vals: CellValue[], idx: number): string {
  return hasPk.value ? makeRowKey(pkIdx.value.map((i) => vals[i])) : `#${offset.value + idx}`;
}
function pkOf(vals: CellValue[]): Record<string, CellValue> {
  const pk: Record<string, CellValue> = {};
  for (const i of pkIdx.value) pk[columns.value[i].name] = vals[i];
  return pk;
}

const displayRows = computed<GridRow[]>(() => {
  const r = result.value;
  if (!r) return [];
  const ch = changes.byTab[props.tab.id];
  const n = columns.value.length;
  const out: GridRow[] = r.rows.map((vals, idx) => {
    const key = rowKeyOf(vals, idx);
    const upd = ch?.updates[key];
    const del = !!ch && key in ch.deletes;
    let values = vals;
    let changed: boolean[] | undefined;
    if (upd) {
      values = vals.slice();
      changed = new Array(n).fill(false);
      for (const c in upd.set) {
        const ci = colIndex.value.get(c);
        if (ci !== undefined) {
          values[ci] = upd.set[c];
          changed[ci] = true;
        }
      }
    }
    return { key, values, state: del ? "deleted" : "normal", changed };
  });
  if (ch) {
    for (const ins of ch.inserts) {
      out.push({
        key: ins.tempId,
        values: columns.value.map((c) => (c.name in ins.values ? ins.values[c.name] : SQL_DEFAULT)),
        state: "new",
        changed: columns.value.map((c) => c.name in ins.values),
      });
    }
  }
  return out;
});

// ---- staging -------------------------------------------------------------------------------------------------
function blockMessage(): string | null {
  if (readOnly.value) return "Connection is read-only";
  if (!hasPk.value) return "This table has no primary key, so it is read-only";
  return null;
}
function guardEdit(): boolean {
  const m = blockMessage();
  if (m) {
    toastOnce(m, "warning");
    return false;
  }
  return true;
}
function onEditBlocked() {
  guardEdit();
}

function onApplyEdits({ edits, appended }: AppliedEdits) {
  promote();
  if (!guardEdit() || !result.value) return;
  const r = result.value;
  const ch = changes.byTab[props.tab.id];
  changes.batch(props.tab.id, () => {
    for (const e of edits) {
      const name = columns.value[e.col].name;
      if (e.row < r.rows.length) {
        const vals = r.rows[e.row];
        const key = rowKeyOf(vals, e.row);
        if (ch && key in ch.deletes) continue; // rows marked for deletion are not editable
        changes.setCell(props.tab.id, key, pkOf(vals), name, e.value, vals[e.col], true);
      } else {
        const ins = ch?.inserts[e.row - r.rows.length];
        if (ins) changes.setInsertCell(props.tab.id, ins.tempId, name, e.value);
      }
    }
    for (const a of appended) {
      const values: Record<string, CellValue> = {};
      a.forEach((v, ci) => {
        if (v !== undefined && columns.value[ci]) values[columns.value[ci].name] = v;
      });
      changes.insertRow(props.tab.id, values);
    }
  });
}

async function addRow() {
  promote();
  if (!guardEdit() || !result.value) return;
  changes.insertRow(props.tab.id);
  await nextTick();
  const idx = displayRows.value.length - 1;
  const firstCol = Math.max(0, columns.value.findIndex((c) => !c.pk));
  grid.value?.selectCell(idx, firstCol);
  grid.value?.focus();
}

function selectedDisplayRows(): number[] {
  const rows = grid.value?.selectedRowIndexes() ?? [];
  if (!rows.length && sel.value.active) return [sel.value.active.row];
  return rows;
}

function duplicateRows() {
  if (!guardEdit() || !result.value) return;
  const idxs = selectedDisplayRows();
  if (!idxs.length) return ui.toast("Select a row to duplicate", "info");
  let skipped = false;
  changes.batch(props.tab.id, () => {
    for (const i of idxs) {
      const row = displayRows.value[i];
      if (!row) continue;
      const values: Record<string, CellValue> = {};
      columns.value.forEach((c, ci) => {
        const meta = colMeta.value.find((m) => m.name === c.name);
        if (meta?.autoIncrement || meta?.generated) return;
        const v = row.values[ci];
        if (isDefaultValue(v)) return;
        if ((isLongText(v) || isBinary(v)) && v.truncated) {
          skipped = true;
          return;
        }
        values[c.name] = v;
      });
      changes.insertRow(props.tab.id, values);
    }
  });
  if (skipped) ui.toast("Truncated long values and large blobs were not copied; the new row uses their defaults", "info");
}

function deleteSelected() {
  if (!guardEdit() || !result.value) return;
  const idxs = selectedDisplayRows();
  if (!idxs.length) return ui.toast("Select the row(s) to delete", "info");
  const n = result.value.rows.length;
  const ch = changes.byTab[props.tab.id];
  promote();
  const wasDeleted = idxs.filter((i) => i < n).every((i) => ch && rowKeyOf(result.value!.rows[i], i) in ch.deletes);
  const removing = idxs.filter((i) => i >= n).length;
  changes.batch(props.tab.id, () => {
    const loaded: { rowKey: string; pk: Record<string, CellValue> }[] = [];
    const newIds: string[] = [];
    for (const i of idxs) {
      if (i < n) loaded.push({ rowKey: rowKeyOf(result.value!.rows[i], i), pk: pkOf(result.value!.rows[i]) });
      else if (ch?.inserts[i - n]) newIds.push(ch.inserts[i - n].tempId);
    }
    for (const id of newIds) changes.removeInsert(props.tab.id, id);
    if (loaded.length) changes.deleteRows(props.tab.id, loaded);
  });
  const loadedCount = idxs.length - removing;
  const plural = (k: number) => `${k} row${k === 1 ? "" : "s"}`;
  const message = loadedCount && wasDeleted
    ? `${plural(loadedCount)} restored`
    : loadedCount
      ? `${plural(loadedCount)} marked for deletion — ⌘S to save`
      : `${plural(removing)} removed`;
  ui.setStatus({ tabId: props.tab.id, message });
}

/** The "N rows marked for deletion" status text goes stale after undo / redo / revert / save. */
function clearStatusMessage() {
  ui.setStatus({ tabId: props.tab.id, message: undefined });
}
function undo() {
  if (!changes.undo(props.tab.id)) toastOnce("Nothing to undo", "info", 1500);
  else clearStatusMessage();
}
function redo() {
  if (!changes.redo(props.tab.id)) toastOnce("Nothing to redo", "info", 1500);
  else clearStatusMessage();
}

async function revertAll() {
  if (!pending.value) return;
  const ok = await ui.confirm({
    title: "Revert all changes?",
    message: `Discard ${pending.value} pending change${pending.value > 1 ? "s" : ""} in ${table.value}?`,
    confirmText: "Revert all",
    danger: true,
  });
  if (ok) {
    changes.revertAll(props.tab.id);
    clearStatusMessage();
  }
}

// ---- save ----------------------------------------------------------------------------------------------------------
async function save() {
  if (readOnly.value) return void ui.toast("Connection is read-only", "warning");
  if (!pending.value) return void ui.toast("No pending changes", "info", 1500);
  let statements: string[];
  try {
    statements = await changes.preview(props.tab.id);
  } catch (e) {
    const err = apiError(e);
    return void ui.toast(err.code === "not_implemented" ? "The backend can't preview changes yet" : err.message || err.code, "error");
  }
  if (conn.value?.color === "red" && !conn.value.readOnly) {
    // Production connection: the confirm dialog doubles as the SQL preview.
    const ok = await ui.confirm({
      title: "Save changes to a production connection?",
      message: `"${conn.value.name}" is marked red. These statements will run in one transaction:`,
      sql: statements.join(";\n") + ";",
      confirmText: "Save",
      danger: true,
    });
    if (!ok) return;
    const count = pending.value;
    const res = await changes.apply(props.tab.id);
    if (res.ok) {
      ui.toast(`Saved ${count} change${count > 1 ? "s" : ""}`, "success");
      changes.notifySaved(props.tab.id);
    } else {
      ui.openModal("saveChanges", {
        tabId: props.tab.id, statements: res.statements?.length ? res.statements : statements,
        error: res.error || "Unknown error", failedIndex: res.failedIndex ?? -1, title: `Save changes to ${table.value}`,
      });
    }
    return;
  }
  ui.openModal("saveChanges", { tabId: props.tab.id, statements, title: `Save changes to ${table.value}` });
}
watch(
  () => changes.savedSeq[props.tab.id],
  (n, o) => {
    if (n !== o) {
      total.value = -1;
      clearStatusMessage();
      load();
    }
  },
);

// ---- find -------------------------------------------------------------------------------------------------------------
async function openFind() {
  promote();
  findOpen.value = true;
  await nextTick();
  findInput.value?.focus();
  findInput.value?.select();
}
function closeFind() {
  findOpen.value = false;
  findText.value = "";
  grid.value?.focus();
}
function onFindKey(e: KeyboardEvent) {
  if (e.key === "Escape") {
    e.preventDefault();
    e.stopPropagation();
    closeFind();
  } else if (e.key === "Enter") {
    e.preventDefault();
    grid.value?.gotoMatch(e.shiftKey ? -1 : 1);
  }
}

// ---- keyboard ----------------------------------------------------------------------------------------------------------------
const overlayOpen = computed(() => ui.overlayOpen);
function onWinKey(e: KeyboardEvent) {
  if (!props.active || overlayOpen.value) return;
  const mod = isMod(e);
  const t = e.target as HTMLElement | null;
  const inField = !!t?.closest("input,textarea,select,[contenteditable='true'],.cm-editor");
  const k = e.key.toLowerCase();
  if (mod && k === "s") return stop(e), void save();
  if ((mod && k === "r") || e.key === "F5") return stop(e), refresh();
  if (mod && k === "f" && !e.shiftKey) return stop(e), void openFind();
  if (inField) return;
  if (mod && k === "n") return stop(e), void addRow();
  if (mod && k === "z") return stop(e), e.shiftKey ? redo() : undo();
  if (mod && k === "y") return stop(e), redo();
  if (mod && e.key === "Backspace") return stop(e), deleteSelected();
  if (mod && k === "d") return stop(e), duplicateRows();
}
function stop(e: Event) {
  e.preventDefault();
  e.stopPropagation();
}

// ---- navigation (FK) ----------------------------------------------------------------------------------------------------------------
function whereFor(pairs: { col: string; v: CellValue }[]): string {
  return pairs.map(({ col, v }) => (v === null ? `${q(col)} IS NULL` : `${q(col)} = ${sqlLiteral(v, driver.value)}`)).join(" AND ");
}
function openFk(row: number, col: number) {
  const c = columns.value[col];
  const v = displayRows.value[row]?.values[col];
  if (!c?.fk || v === null || v === undefined) return;
  tabs.openTable(connId.value, c.fk.schema || schema.value, c.fk.table, { where: whereFor([{ col: c.fk.column, v }]), orderBy: "", focus: true });
}
function openReferencing(fk: ForeignKey, row: number) {
  const vals = displayRows.value[row]?.values;
  if (!vals) return;
  const pairs = fk.columns.map((col, i) => ({ col, v: vals[colIndex.value.get(fk.refColumns[i]) ?? -1] ?? null }));
  tabs.openTable(connId.value, fk.schema || schema.value, fk.table, { where: whereFor(pairs), orderBy: "", focus: true });
}

// ---- context menu / copy as ------------------------------------------------------------------------------------------------------------
function copyItems(): MenuItem[] {
  return COPY_FORMATS.map((f) => ({ label: f.label, run: () => grid.value?.copy(f.id) }));
}
function buildMenu({ row, col }: { row: number; col: number }): MenuItem[] {
  const gr = displayRows.value[row];
  const c = columns.value[col];
  if (!gr || !c) return [];
  const isNew = gr.state === "new";
  const v = gr.values[col];
  const isNull = v === null;
  const items: MenuItem[] = [];
  if (!isNew) {
    items.push(
      { label: "Filter = value", disabled: isNull, run: () => addCellFilter(row, col, "eq") },
      { label: "Filter ≠ value", disabled: isNull, run: () => addCellFilter(row, col, "ne") },
      isNull
        ? { label: "Filter IS NOT NULL", run: () => addCellFilter(row, col, "not_null") }
        : { label: "Filter IS NULL", run: () => addCellFilter(row, col, "is_null") },
      { separator: true },
    );
  }
  items.push(
    { label: "Copy", hint: "⌘C", run: () => grid.value?.copy("tsv") },
    { label: "Copy with headers", hint: "⇧⌘C", run: () => grid.value?.copy("tsv_header") },
    { label: "Copy as", children: copyItems() },
    { separator: true },
    { label: "Paste", hint: "⌘V", disabled: !editable.value, run: pasteFromMenu },
    { separator: true },
    { label: "Edit", hint: "F2", disabled: !editable.value, run: () => grid.value?.startEdit(row, col) },
    { label: "Set NULL", disabled: !editable.value, run: () => grid.value?.setNull() },
    { label: "Set default", disabled: !editable.value, run: () => grid.value?.setDefault() },
    { label: "View value…", run: () => grid.value?.viewValue(row, col) },
  );
  const fkItems: MenuItem[] = [];
  if (c.fk) fkItems.push({ label: `Open referenced row (${c.fk.table}.${c.fk.column})`, disabled: isNull || isNew, run: () => openFk(row, col) });
  if (refs.value && refs.value.length && !isNew) {
    fkItems.push({
      label: "Show referencing rows",
      children: refs.value.map((fk) => ({ label: `${fk.table} (${fk.columns.join(", ")})`, run: () => openReferencing(fk, row) })),
    });
  }
  if (fkItems.length) items.push({ separator: true }, ...fkItems);
  items.push(
    { separator: true },
    { label: "Duplicate row", hint: "⌘D", disabled: !editable.value, run: duplicateRows },
    { label: gr.state === "deleted" ? "Restore row" : "Delete row", hint: "⌘⌫", danger: true, disabled: !editable.value, run: deleteSelected },
  );
  return items;
}

function openCopyMenu(e: MouseEvent) {
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
  copyMenu.value = { x: r.left, y: r.bottom + 2 };
}

function openExport() {
  const page: PageRequest = pageRequest();
  ui.openModal("export", {
    connId: connId.value, schema: schema.value, table: table.value, page,
    hasSelection: sel.value.cellCount > 1, loadedRows: rowCount.value, getText: exportText,
  });
}
async function exportText(scope: "page" | "selection", format: string, header: boolean): Promise<string | null> {
  const fmt = format === "sql" ? "sql_insert" : format === "csv" && !header ? "csv_body" : format === "tsv" ? (header ? "tsv_header" : "tsv") : (format as "csv" | "json" | "markdown");
  if (scope === "selection") return (await grid.value?.selectionText(fmt)) ?? null;
  const rows = displayRows.value.filter((r) => r.state !== "new").map((r) => r.values);
  if (rows.some((r) => r.some((v) => (isLongText(v) || isBinary(v)) && v.truncated))) {
    throw new Error("The loaded rows contain truncated long values. Export \"All rows\" to get them in full.");
  }
  return formatCopy(fmt, {
    columns: columns.value, cols: columns.value.map((_, i) => i), rows, driver: driver.value, schema: schema.value, table: table.value,
  });
}

// ---- misc -------------------------------------------------------------------------------------------------------------------------------------
async function loadFull(row: number, col: number): Promise<CellValue | null> {
  const r = result.value;
  if (!r || row >= r.rows.length || !hasPk.value) return null;
  try {
    return await dataApi.CellValue({
      connId: connId.value, schema: schema.value, table: table.value, pk: pkOf(r.rows[row]), column: columns.value[col].name,
    });
  } catch {
    return null;
  }
}

function onSelection(info: SelectionInfo) {
  sel.value = info;
  // status bar: cursor + selection summary
  const a = info.active;
  const parts: string[] = [];
  if (a && columns.value[a.col]) parts.push(`Row ${fmtNum(offset.value + a.row + 1)}, ${columns.value[a.col].name}`);
  if (info.cellCount > 1) parts.push(`${fmtNum(info.rowCount)} × ${fmtNum(info.colCount)} cells`);
  ui.setStatus({ tabId: props.tab.id, message: parts.join(" · ") || undefined });
}
function onRecordEdit(col: number, value: CellValue) {
  const row = sel.value.active?.row;
  if (row === undefined) return;
  if (displayRows.value[row]?.state === "deleted") return void toastOnce("Row is marked for deletion", "info");
  onApplyEdits({ edits: [{ row, col, value }], appended: [] });
}
const recordRow = computed(() => (sel.value.active ? displayRows.value[sel.value.active.row] ?? null : null));
function recordLoadFull(col: number): Promise<CellValue | null> {
  return sel.value.active ? loadFull(sel.value.active.row, col) : Promise.resolve(null);
}
function editTitle(base: string): string {
  return blockMessage() ? `${base}: ${blockMessage()}` : base;
}
function toggleRecord() {
  promote();
  recordOpen.value = !recordOpen.value;
}
function onOpenRecord() {
  promote();
  recordOpen.value = true;
}

const copyContext = computed(() => ({ driver: driver.value, schema: schema.value, table: table.value }));
const qualified = computed(() => qualifiedTable(driver.value, schema.value, table.value));
const emptyText = computed(() =>
  loadError.value ? "" : applied.value.where || columnFilters.value.length ? "No rows match the filters" : editable.value ? "This table is empty. Press ⌘N to add a row." : "This table is empty",
);

// overlays opening above (modal, palette) close the popovers owned by this view
watch(() => ui.overlayOpen, (o) => {
  if (!o) return;
  filterPop.value = null;
  copyMenu.value = null;
});

/** Paste from the context menu (no paste event there): Clipboard API. */
function pasteFromMenu() {
  void grid.value?.paste();
}
// ---- Cmd+click navigation (go to referenced / referencing rows) ------------------------------------
const navCols = computed(() => navigableColumns(links.value, columns.value));
function navigable(row: number, col: number): boolean {
  const gr = displayRows.value[row];
  const c = columns.value[col];
  if (!gr || !c || gr.state === "new" || !navCols.value.has(c.name)) return false;
  const v = gr.values[col];
  return v !== null && v !== undefined && !isDefaultValue(v);
}
function entriesAt(row: number, col: number): NavEntry[] {
  const gr = displayRows.value[row];
  return gr ? navEntries(links.value, columns.value, gr.values, col, driver.value, schema.value) : [];
}
function navTitle(row: number, col: number): string {
  const e = entriesAt(row, col);
  if (!e.length) return "";
  if (e.length === 1) return e[0].heading === "References" ? `⌘-click to open ${e[0].table}` : `⌘-click to show rows in ${e[0].label}`;
  return `⌘-click: ${e.length} related tables (⌘⇧-click multi-selects)`;
}
function openEntry(en: NavEntry) {
  navMenu.value = null;
  tabs.openTable(connId.value, en.schema || schema.value, en.table, { where: en.where, orderBy: "", focus: true });
}
function onNav(row: number, col: number, x: number, y: number) {
  const e = entriesAt(row, col);
  if (!e.length) return;
  if (e.length === 1) return openEntry(e[0]);
  navMenu.value = { x, y, entries: e };
}
const incoming = computed(() => {
  const gr = recordRow.value;
  return gr && gr.state !== "new" ? incomingForRow(links.value, columns.value, gr.values, driver.value, schema.value) : [];
});
function onRecordNav(col: number, x: number, y: number) {
  if (sel.value.active) onNav(sel.value.active.row, col, x, y);
}

function onAltClick(row: number, col: number) {
  const v = displayRows.value[row]?.values[col];
  if (displayRows.value[row]?.state === "new") return;
  addCellFilter(row, col, v === null ? "is_null" : "eq");
}
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col" :style="{ '--conn': tint }">
    <!-- toolbar row 1 -->
    <div class="tb">
      <button class="btn ghost" title="Refresh (⌘R)" @click="refresh"><Icon name="refresh" /></button>
      <span class="mono dim range">{{ rangeText }}</span>
      <button v-if="isEstimate" class="btn ghost cnt" :disabled="counting" title="Count all rows exactly" @click="countExact">{{ counting ? "Counting…" : "Count" }}</button>
      <button class="btn ghost" :disabled="offset === 0" title="Previous page" @click="page(-1)"><Icon name="chevL" /></button>
      <button class="btn ghost" :disabled="!canNext" title="Next page" @click="page(1)"><Icon name="chevR" /></button>
      <select v-model.number="limit" class="input sel" title="Rows per page" @change="onLimit">
        <option v-for="n in [100, 250, 500, 1000, 5000]" :key="n" :value="n">{{ n }} rows</option>
      </select>
      <span class="sep"></span>
      <button class="btn ghost" :disabled="!editable" :title="editTitle('Add row (⌘N)')" @click="addRow"><Icon name="plus" /></button>
      <button class="btn ghost" :disabled="!editable" :title="editTitle('Duplicate row (⌘D)')" @click="duplicateRows"><Icon name="dup" /></button>
      <button class="btn ghost" :disabled="!editable" :title="editTitle('Delete row(s) (⌘⌫)')" @click="deleteSelected"><Icon name="trash" /></button>
      <button class="btn ghost" :disabled="!pending" title="Revert all changes (⌘Z undoes the last one)" @click="revertAll"><Icon name="undo" /> Revert</button>
      <button class="btn primary" :disabled="!pending || readOnly" title="Save changes (⌘S)" @click="save">
        <Icon name="save" /> Save <span v-if="pending" class="bdg">{{ pending }}</span>
      </button>
      <span v-if="readOnly" class="pill ro" title="This connection is read-only. Editing is disabled."><Icon name="lock" :size="10" /> READ-ONLY</span>
      <span
        v-else-if="result && !hasPk" class="pill neutral" style="color: var(--warn)"
        title="Rows are identified by their primary key. This table has none, so edits, deletes and pastes are disabled."
      >No primary key — read-only</span>
      <span class="flex-1"></span>
      <button class="btn" :disabled="!result" @click="openCopyMenu"><Icon name="copy" /> Copy as <Icon name="chevD" :size="10" /></button>
      <button class="btn ghost" :disabled="!result" @click="openExport"><Icon name="export" /> Export</button>
      <button class="btn ghost" :class="{ on: recordOpen }" title="Record view" @click="toggleRecord"><Icon name="panel" /></button>
    </div>
    <!-- toolbar row 2 -->
    <div class="tb">
      <SqlClauseInput v-model="whereDraft" label="WHERE" :driver="driver" placeholder="e.g. status = 'new'" :columns="colNames" :error="whereError" @apply="applyClauses" />
      <SqlClauseInput v-model="orderDraft" label="ORDER BY" :driver="driver" placeholder="e.g. id DESC" :columns="colNames" @apply="applyClauses" />
      <button class="btn" title="Apply WHERE / ORDER BY (↵)" @click="applyClauses">Apply</button>
    </div>
    <div v-if="whereError" class="errbar mono selectable">{{ whereError }}</div>
    <!-- filter chips -->
    <div v-if="columnFilters.length" class="tb chips">
      <span class="dim">Filters</span>
      <span v-for="f in columnFilters" :key="f.column" class="chip mono" title="Edit filter" @click="onFilterColumn(Math.max(0, colNames.indexOf(f.column)), ($event.currentTarget as HTMLElement).getBoundingClientRect())">
        {{ chipText(f) }}
        <button class="chip-x" title="Remove filter" @click.stop="removeColumnFilter(f.column)"><Icon name="x" :size="10" /></button>
      </span>
      <button class="btn ghost" @click="clearAllFilters">Clear all</button>
    </div>
    <!-- find bar -->
    <div v-if="findOpen" class="tb find">
      <Icon name="search" class="dim" />
      <input ref="findInput" v-model="findText" class="input mono" placeholder="Find in loaded rows" spellcheck="false" style="width: 220px" @keydown="onFindKey" />
      <span class="dim mono">{{ findText ? `${findCount} match${findCount === 1 ? "" : "es"}` : "" }}</span>
      <button class="btn ghost" :disabled="!findCount" title="Previous (⇧↵)" @click="grid?.gotoMatch(-1)"><Icon name="chevD" style="transform: rotate(180deg)" /></button>
      <button class="btn ghost" :disabled="!findCount" title="Next (↵)" @click="grid?.gotoMatch(1)"><Icon name="chevD" /></button>
      <button class="btn ghost" title="Close (Esc)" @click="closeFind"><Icon name="x" /></button>
    </div>

    <div class="flex min-h-0 flex-1">
      <div class="relative flex min-w-0 flex-1 flex-col">
        <div v-if="loadError" class="loaderr selectable">
          <b>Could not load {{ qualified }}</b>
          <div class="mono">{{ loadError }}</div>
          <button class="btn" @click="retry">Retry</button>
        </div>
        <div v-if="stale && !loadError" class="tv-banner" role="alert">Showing previous results — query failed</div>
        <DataGrid
          v-show="!loadError"
          ref="grid"
          :class="{ 'tv-stale': stale }"
          :columns="columns"
          :rows="displayRows"
          :editable="editable"
          :row-offset="offset"
          :sorts="sorts"
          :initial-widths="initialWidths"
          :highlight="findOpen ? findText : ''"
          :filtered-cols="filteredCols"
          :copy-context="copyContext"
          :context-menu="buildMenu"
          :load-full="loadFull"
          :navigable="navigable"
          :nav-title="navTitle"
          :loading="loading"
          :empty-text="emptyText"
          @sort="onSort"
          @sort-set="onSortSet"
          @alt-click="onAltClick"
          @nav="onNav"
          @delete-rows="deleteSelected"
          @scroll="filterPop = null"
          @apply-edits="onApplyEdits"
          @edit-blocked="onEditBlocked"
          @filter-column="onFilterColumn"
          @selection="onSelection"
          @widths-change="onWidths"
          @find-count="findCount = $event"
          @open-record="onOpenRecord"
        />
      </div>
      <RecordPanel
        v-if="recordOpen"
        :columns="columns"
        :row="recordRow"
        :row-number="sel.active ? offset + sel.active.row + 1 : 0"
        :editable="editable"
        :load-full="recordLoadFull"
        :incoming="incoming"
        :nav-cols="navCols"
        @nav="onRecordNav"
        @open-entry="openEntry"
        @edit="onRecordEdit"
        @open-fk="(c: number) => sel.active && openFk(sel.active.row, c)"
        @edit-blocked="onEditBlocked"
        @close="recordOpen = false"
      />
    </div>

    <ColumnFilterPopover
      v-if="filterPop && columns[filterPop.col]"
      :column="columns[filterPop.col].name" :type="columns[filterPop.col].type" :rect="filterPop.rect"
      :existing="columnFilters.find((f) => f.column === columns[filterPop!.col].name)"
      @apply="setColumnFilter" @clear="removeColumnFilter(columns[filterPop!.col].name)" @close="filterPop = null"
    />
    <NavMenu v-if="navMenu" :x="navMenu.x" :y="navMenu.y" :entries="navMenu.entries" :conn-id="connId" @pick="openEntry" @close="navMenu = null" />
    <ContextMenu v-if="copyMenu" :x="copyMenu.x" :y="copyMenu.y" :items="copyItems()" @close="copyMenu = null" />
  </div>
</template>

<style scoped>
.tb { display: flex; align-items: center; gap: 6px; padding: 4px 10px; border-bottom: 1px solid var(--bd); flex: none; flex-wrap: wrap; }
.sep { width: 1px; height: 16px; background: var(--bd); }
.range { min-width: 90px; }
.sel { height: 22px; padding: 0 4px; }
.bdg { background: color-mix(in srgb, var(--ac-fg, #fff) 22%, transparent); color: var(--ac-fg, #fff); border-radius: 8px; padding: 0 5px; font-size: 10px; line-height: 14px; }
.btn.on { background: var(--hov); border-color: var(--bd); }
.btn.cnt { font-size: 11px; color: var(--ac); padding: 0 6px; }
.pill.ro { display: inline-flex; align-items: center; gap: 4px; }
.errbar { padding: 4px 10px; color: var(--danger); background: color-mix(in srgb, var(--danger) 10%, transparent); border-bottom: 1px solid var(--bd); white-space: pre-wrap; font-size: 11px; }
.chips { background: var(--bg2); }
.tv-stale { opacity: .4; pointer-events: none; }
.tv-banner { position: absolute; z-index: 30; top: 34px; left: 50%; transform: translateX(-50%); padding: 3px 12px; border-radius: 4px; background: var(--danger); color: #fff; font-size: 11px; box-shadow: 0 2px 8px var(--shadow); pointer-events: none; }
.chip {
  display: inline-flex; align-items: center; gap: 4px; padding: 0 2px 0 8px; height: 20px; border-radius: 10px;
  background: color-mix(in srgb, var(--ac) 18%, transparent); border: 1px solid color-mix(in srgb, var(--ac) 45%, transparent); font-size: 11px; cursor: default;
}
.chip-x { background: none; border: 0; padding: 2px; border-radius: 50%; display: inline-flex; color: var(--dim); }
.chip-x:hover { background: var(--hov); color: var(--tx); }
.find { background: var(--bg2); }
.loaderr { padding: 24px; display: flex; flex-direction: column; gap: 8px; align-items: flex-start; color: var(--danger); }
.loaderr .mono { color: var(--tx); white-space: pre-wrap; }
</style>
