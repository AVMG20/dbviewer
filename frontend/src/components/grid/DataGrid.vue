<script setup lang="ts">
// Virtualized data grid (rows + columns on @tanstack/vue-virtual).
// Spreadsheet selection, inline + popover editing (edits are only *emitted*; the parent stages
// them), copy/paste, context menu, resizable columns, quick-find highlighting.
// Used by TableView (editable) and SqlConsole results (read-only).
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from "vue";
import { kbd } from "@/lib/kbd";
import { useVirtualizer } from "@tanstack/vue-virtual";
import type { CellValue, Driver, ResultColumn } from "@/api";
import { isBinary, isLongText, SQL_DEFAULT } from "@/api";
import { useUiStore } from "@/stores/ui";
import { copyText, readClipboardText } from "@/lib/clipboard";
import { isMod } from "@/lib/keys";
import { toastOnce } from "@/lib/notify";
import { formatCopy, type CopyFormatId } from "@/lib/copyFormats";
import { PASTE_DEFAULT, PASTE_NULL, parseDelimited } from "@/lib/delimited";
import {
  cellEquals, cellText, coerceInput, isBlankInvalid, isDefaultValue, isNumericKind, measureText, minifyJson, needsPopover, NON_TEXT,
  renderCell, searchText, validateInput, type Rendered,
} from "@/lib/format";
import type { AppliedEdits, CellEdit, CopyContext, GridRow, MenuItem, SelectionInfo, SortSpec } from "@/lib/gridTypes";
import CellPopover from "./CellPopover.vue";
import { rowHeight as rowH, settings } from "@/lib/settings";
import ContextMenu from "@/components/common/ContextMenu.vue";
import Icon from "@/components/common/Icon.vue";

const HEADER_H = 26;

const props = withDefaults(
  defineProps<{
    columns: ResultColumn[];
    rows: GridRow[];
    editable?: boolean;
    rowOffset?: number;
    sorts?: SortSpec[];
    initialWidths?: Record<string, number>;
    highlight?: string;
    filteredCols?: string[];
    copyContext?: CopyContext;
    contextMenu?: (cell: { row: number; col: number }) => MenuItem[];
    loadFull?: (row: number, col: number) => Promise<CellValue | null>;
    /** key cells (FK / referenced) that navigate on Cmd-click */
    navigable?: (row: number, col: number) => boolean;
    navTitle?: (row: number, col: number) => string;
    loading?: boolean;
    emptyText?: string;
  }>(),
  { editable: false, rowOffset: 0, sorts: () => [], filteredCols: () => [], highlight: "", emptyText: "No rows" },
);

const emit = defineEmits<{
  sort: [col: number, additive: boolean];
  "sort-set": [col: number, dir: "asc" | "desc" | null, additive: boolean];
  "alt-click": [row: number, col: number];
  nav: [row: number, col: number, x: number, y: number];
  scroll: [];
  "apply-edits": [payload: AppliedEdits];
  "edit-blocked": [];
  "filter-column": [col: number, rect: DOMRect];
  selection: [info: SelectionInfo];
  "widths-change": [widths: Record<string, number>];
  "find-count": [n: number];
  "open-record": [row: number];
  "delete-rows": [];
}>();

const ui = useUiStore();

// ---- geometry ---------------------------------------------------------------
const scroller = ref<HTMLElement | null>(null);
const root = ref<HTMLElement | null>(null);
const nCols = computed(() => props.columns.length);
const rnW = computed(() => Math.max(44, String(props.rowOffset + props.rows.length).length * 8 + 22));

const widths = ref<number[]>([]);
const userWidths: Record<string, number> = { ...props.initialWidths };
watch(() => props.initialWidths, (w) => Object.assign(userWidths, w));

const DEFAULT_W: Record<string, number> = {
  int: 90, float: 100, decimal: 110, bool: 80, date: 105, time: 95, datetime: 160, uuid: 290, json: 200, text: 220, string: 160, binary: 120, other: 140,
};

/** The type label only shows when at least ~3ch of it fit next to the full name; it never shows as a lone "v…". */
function showType(ci: number, width: number): boolean {
  const c = props.columns[ci];
  return !!c.type && width - headerWidth(c) >= 30;
}

/** Width needed for the column name + icons (the type label truncates and never drives the width). */
function headerWidth(c: ResultColumn): number {
  // padding 16 + key/link icon + sort arrow (+ index) + resize handle: the name is never cut
  return Math.ceil(measureText(c.name, "600 11px -apple-system, sans-serif")) + 16 + (c.pk || c.fk ? 15 : 0) + 20 + 6;
}

function computeWidths() {
  const sample = props.rows.slice(0, 60);
  widths.value = props.columns.map((c, ci) => {
    const saved = props.initialWidths?.[c.name] ?? userWidths[c.name];
    if (saved) return Math.round(saved);
    let w = DEFAULT_W[c.kind] ?? 140;
    let content = 0;
    for (const r of sample) {
      const t = renderCell(r.values[ci], c).text;
      content = Math.max(content, measureText(t.length > 80 ? t.slice(0, 80) : t));
      if (content > 340) break;
    }
    if (sample.length) w = Math.min(Math.max(content + 22, 60), 360);
    return Math.round(Math.max(w, headerWidth(c), 60));
  });
}

const colSig = computed(() => props.columns.map((c) => c.name + ":" + c.type).join("|"));
computeWidths();
// Recompute default widths when the first rows arrive for a fresh grid.
watch(
  () => props.rows.length === 0,
  (empty, wasEmpty) => {
    if (wasEmpty && !empty && Object.keys(userWidths).length === 0) computeWidths();
  },
);

const rowVirt = useVirtualizer(
  computed(() => ({
    count: props.rows.length,
    getScrollElement: () => scroller.value,
    estimateSize: () => rowH(),
    overscan: 10,
    paddingStart: HEADER_H,
  })),
);
const colVirt = useVirtualizer(
  computed(() => ({
    horizontal: true,
    count: nCols.value,
    getScrollElement: () => scroller.value,
    estimateSize: (i: number) => widths.value[i] ?? 140,
    overscan: 3,
    paddingStart: rnW.value,
  })),
);
watch(widths, () => colVirt.value.measure(), { deep: true });
watch(rnW, () => colVirt.value.measure());

const rowItems = computed(() => rowVirt.value.getVirtualItems());
const colItems = computed(() => colVirt.value.getVirtualItems());
const totalH = computed(() => rowVirt.value.getTotalSize());
const totalW = computed(() => colVirt.value.getTotalSize());

const colStarts = computed(() => {
  const out: number[] = [];
  let x = rnW.value;
  for (let i = 0; i < nCols.value; i++) {
    out.push(x);
    x += widths.value[i] ?? 140;
  }
  return out;
});

// ---- selection --------------------------------------------------------------
interface Range { r1: number; c1: number; r2: number; c2: number }
const ranges = ref<Range[]>([]);
const active = ref<{ r: number; c: number } | null>(null);
let anchor: { r: number; c: number } | null = null;

function mk(a: { r: number; c: number }, b: { r: number; c: number }): Range {
  return { r1: Math.min(a.r, b.r), c1: Math.min(a.c, b.c), r2: Math.max(a.r, b.r), c2: Math.max(a.c, b.c) };
}
function clearSelection() {
  ranges.value = [];
  active.value = null;
  anchor = null;
}
function selectCell(r: number, c: number, reveal = true) {
  if (!props.rows.length || !nCols.value) return;
  r = Math.max(0, Math.min(r, props.rows.length - 1));
  c = Math.max(0, Math.min(c, nCols.value - 1));
  anchor = { r, c };
  active.value = { r, c };
  ranges.value = [mk(anchor, anchor)];
  if (reveal) revealCell(r, c);
}
function extendTo(r: number, c: number) {
  if (!anchor) return selectCell(r, c);
  r = Math.max(0, Math.min(r, props.rows.length - 1));
  c = Math.max(0, Math.min(c, nCols.value - 1));
  const next = ranges.value.slice();
  next[Math.max(0, next.length - 1)] = mk(anchor, { r, c });
  ranges.value = next;
  active.value = { r, c };
  revealCell(r, c);
}
function selectAll() {
  if (!props.rows.length || !nCols.value) return;
  ranges.value = [{ r1: 0, c1: 0, r2: props.rows.length - 1, c2: nCols.value - 1 }];
  anchor = { r: 0, c: 0 };
  active.value ??= { r: 0, c: 0 };
}
function selectColumn(c: number, additive: boolean) {
  if (!props.rows.length) return;
  const rg = { r1: 0, c1: c, r2: props.rows.length - 1, c2: c };
  ranges.value = additive ? [...ranges.value, rg] : [rg];
  anchor = { r: 0, c };
  active.value = { r: active.value?.r ?? 0, c };
}
function selectRow(r: number, mode: "single" | "extend" | "add") {
  const full = (a: number, b: number): Range => ({ r1: Math.min(a, b), c1: 0, r2: Math.max(a, b), c2: nCols.value - 1 });
  if (mode === "extend" && anchor) ranges.value = [full(anchor.r, r)];
  else if (mode === "add") {
    ranges.value = [...ranges.value, full(r, r)];
    anchor = { r, c: 0 };
  } else {
    ranges.value = [full(r, r)];
    anchor = { r, c: 0 };
  }
  active.value = { r, c: active.value?.c ?? 0 };
}
function inRange(rg: Range, r: number, c: number) {
  return r >= rg.r1 && r <= rg.r2 && c >= rg.c1 && c <= rg.c2;
}
function isSelected(r: number, c: number): boolean {
  const rs = ranges.value;
  for (let i = 0; i < rs.length; i++) if (inRange(rs[i], r, c)) return true;
  return false;
}
function rowTouched(r: number): boolean {
  const rs = ranges.value;
  for (let i = 0; i < rs.length; i++) if (r >= rs[i].r1 && r <= rs[i].r2) return true;
  return false;
}
function selStyle(r: number, c: number): string | undefined {
  const rs = ranges.value;
  for (let i = rs.length - 1; i >= 0; i--) {
    const g = rs[i];
    if (!inRange(g, r, c)) continue;
    const s: string[] = [];
    if (r === g.r1) s.push("inset 0 1px 0 var(--selbd)");
    if (r === g.r2) s.push("inset 0 -1px 0 var(--selbd)");
    if (c === g.c1) s.push("inset 1px 0 0 var(--selbd)");
    if (c === g.c2) s.push("inset -1px 0 0 var(--selbd)");
    return s.length ? "box-shadow:" + s.join(",") : undefined;
  }
  return undefined;
}
const selectedRowSet = computed(() => {
  const s = new Set<number>();
  for (const g of ranges.value) for (let r = g.r1; r <= g.r2; r++) s.add(r);
  return s;
});
const selectedColSet = computed(() => {
  const s = new Set<number>();
  for (const g of ranges.value) for (let c = g.c1; c <= g.c2; c++) s.add(c);
  return s;
});
function selectedRowIndexes(): number[] {
  return [...selectedRowSet.value].sort((a, b) => a - b);
}

/** Number of distinct selected cells (overlapping ranges are counted once). */
function uniqueCellCount(): number {
  const rs = ranges.value;
  const area = rs.reduce((n, g) => n + (g.r2 - g.r1 + 1) * (g.c2 - g.c1 + 1), 0);
  if (rs.length <= 1 || area > 300_000) return area;
  const seen = new Set<number>();
  const n = nCols.value;
  for (const g of rs) for (let r = g.r1; r <= g.r2; r++) for (let c = g.c1; c <= g.c2; c++) seen.add(r * n + c);
  return seen.size;
}

watch([ranges, active], () => {
  const info: SelectionInfo = {
    active: active.value ? { row: active.value.r, col: active.value.c } : null,
    rowCount: selectedRowSet.value.size,
    colCount: selectedColSet.value.size,
    cellCount: uniqueCellCount(),
  };
  emit("selection", info);
  scheduleStats();
});

// ---- selection aggregates (status bar: count / sum / avg / min / max) ----------------
/** Id of the tab this grid lives in (App.vue marks each tab view with data-tab-id). */
function ownTabId(): string {
  return (root.value?.closest("[data-tab-view]") as HTMLElement | null)?.dataset.tabId ?? "";
}
let statsTimer: ReturnType<typeof setTimeout> | undefined;
function scheduleStats() {
  clearTimeout(statsTimer);
  statsTimer = setTimeout(computeStats, 60);
}
const STATS_MAX_CELLS = 500_000;
function computeStats() {
  const tabId = ownTabId();
  const rs = ranges.value;
  const cells = uniqueCellCount();
  if (!tabId || cells < 2 || cells > STATS_MAX_CELLS) {
    if (ui.selectionStats?.tabId === tabId) ui.setSelectionStats(null);
    return;
  }
  const n = nCols.value;
  const seen = rs.length > 1 ? new Set<number>() : null;
  let values = 0, numbers = 0, sum = 0, min = Infinity, max = -Infinity;
  for (const g of rs) {
    for (let r = g.r1; r <= g.r2; r++) {
      const row = props.rows[r];
      if (!row) continue;
      for (let c = g.c1; c <= g.c2; c++) {
        if (seen) {
          const k = r * n + c;
          if (seen.has(k)) continue;
          seen.add(k);
        }
        const v = row.values[c];
        if (v === null || v === undefined || isDefaultValue(v)) continue;
        values++;
        if (!isNumericKind(props.columns[c]?.kind ?? "")) continue;
        const x = typeof v === "number" ? v : typeof v === "string" ? Number(v) : NaN;
        if (!Number.isFinite(x)) continue;
        numbers++;
        sum += x;
        if (x < min) min = x;
        if (x > max) max = x;
      }
    }
  }
  ui.setSelectionStats(
    numbers ? { tabId, cells, values, numbers, sum, avg: sum / numbers, min, max } : { tabId, cells, values, numbers },
  );
}
// rows edited / reloaded under an unchanged selection
watch(() => props.rows, scheduleStats);
onBeforeUnmount(() => {
  clearTimeout(statsTimer);
  const id = ownTabId();
  if (id && ui.selectionStats?.tabId === id) ui.setSelectionStats(null);
});
// density changed: re-measure the virtual rows
watch(() => settings.density, () => rowVirt.value.measure());
watch(
  () => props.rows.length,
  (n) => {
    if (!ranges.value.length) return;
    if (n === 0) return clearSelection();
    ranges.value = ranges.value.filter((g) => g.r1 < n).map((g) => ({ ...g, r2: Math.min(g.r2, n - 1) }));
    if (active.value && active.value.r >= n) active.value = { r: n - 1, c: active.value.c };
  },
);

watch(colSig, () => {
  computeWidths();
  clearSelection();
  cancelEdit();
  nextTick(() => colVirt.value.measure());
});

// ---- scrolling helpers ------------------------------------------------------
function revealCell(r: number, c: number) {
  const s = scroller.value;
  if (!s) return;
  const top = HEADER_H + r * rowH();
  if (top < s.scrollTop + HEADER_H) s.scrollTop = top - HEADER_H;
  else if (top + rowH() > s.scrollTop + s.clientHeight) s.scrollTop = top + rowH() - s.clientHeight;
  const left = colStarts.value[c] ?? 0;
  const w = widths.value[c] ?? 100;
  if (left < s.scrollLeft + rnW.value) s.scrollLeft = left - rnW.value;
  else if (left + w > s.scrollLeft + s.clientWidth) s.scrollLeft = left + w - s.clientWidth;
}
/** Row indexes of the selected cells in column c (for "Filter IN selected values"). */
function selectedRowsInColumn(c: number): number[] {
  const out = new Set<number>();
  for (const g of ranges.value) if (c >= g.c1 && c <= g.c2) for (let r = g.r1; r <= g.r2; r++) out.add(r);
  return [...out].sort((a, b) => a - b);
}
/** Selects column c and scrolls it into view (palette / sidebar column jumps). */
function focusColumn(c: number) {
  if (c < 0 || c >= nCols.value) return;
  if (props.rows.length) selectColumn(c, false);
  else active.value = null;
  const s = scroller.value;
  if (s) {
    const left = colStarts.value[c] ?? 0;
    const w = widths.value[c] ?? 100;
    // center the column when it is off-screen
    if (left < s.scrollLeft + rnW.value || left + w > s.scrollLeft + s.clientWidth) s.scrollLeft = Math.max(0, left - (s.clientWidth - w) / 2);
    s.scrollTop = 0;
  }
  focus();
}
function visibleRows(): number {
  return Math.max(1, Math.floor(((scroller.value?.clientHeight ?? 400) - HEADER_H) / rowH()) - 1);
}

// ---- mouse ------------------------------------------------------------------
function pointToCell(clientX: number, clientY: number): { r: number; c: number } | null {
  const s = scroller.value;
  if (!s || !props.rows.length || !nCols.value) return null;
  const rect = s.getBoundingClientRect();
  const x = clientX - rect.left + s.scrollLeft;
  const y = clientY - rect.top + s.scrollTop - HEADER_H;
  const r = Math.max(0, Math.min(props.rows.length - 1, Math.floor(y / rowH())));
  const starts = colStarts.value;
  let lo = 0;
  let hi = starts.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (starts[mid] <= x) lo = mid;
    else hi = mid - 1;
  }
  return { r, c: lo };
}

let dragging = false;
let lastMouse = { x: 0, y: 0 };
let dragTimer: number | undefined;

function onCellDown(e: MouseEvent, r: number, c: number) {
  if (e.button === 2) {
    if (!isSelected(r, c)) selectCell(r, c, false);
    else active.value = { r, c };
    return;
  }
  if (e.button !== 0) return;
  root.value?.focus({ preventScroll: true });
  // Cmd-click on a key cell navigates (go to referenced / referencing rows); Cmd+Shift-click multi-selects as before
  const navHit = isMod(e) && !!props.navigable?.(r, c);
  if (navHit && !e.shiftKey) {
    e.preventDefault();
    emit("nav", r, c, e.clientX, e.clientY);
    return;
  }
  if (e.shiftKey && anchor && !navHit) extendTo(r, c);
  else if (isMod(e)) {
    const single = ranges.value.findIndex((g) => g.r1 === r && g.r2 === r && g.c1 === c && g.c2 === c);
    if (single >= 0 && ranges.value.length > 1) {
      // Cmd-click on a selected single cell toggles it off
      ranges.value = ranges.value.filter((_, i) => i !== single);
      const last = ranges.value[ranges.value.length - 1];
      active.value = { r: last.r2, c: last.c2 };
      return;
    }
    anchor = { r, c };
    ranges.value = [...ranges.value, mk(anchor, anchor)];
    active.value = { r, c };
  } else if (e.altKey) {
    selectCell(r, c, false);
    emit("alt-click", r, c);
    return;
  } else selectCell(r, c, false);
  dragging = true;
  lastMouse = { x: e.clientX, y: e.clientY };
  window.addEventListener("mousemove", onDragMove);
  window.addEventListener("mouseup", onDragEnd);
  dragTimer = window.setInterval(autoScroll, 40);
}
function onDragMove(e: MouseEvent) {
  if (!dragging) return;
  lastMouse = { x: e.clientX, y: e.clientY };
  const p = pointToCell(e.clientX, e.clientY);
  if (p && (active.value?.r !== p.r || active.value?.c !== p.c)) {
    const next = ranges.value.slice();
    if (anchor) next[Math.max(0, next.length - 1)] = mk(anchor, p);
    ranges.value = next;
    active.value = p;
  }
}
function autoScroll() {
  const s = scroller.value;
  if (!s || !dragging) return;
  const rect = s.getBoundingClientRect();
  const { x, y } = lastMouse;
  let moved = false;
  if (y > rect.bottom - 8) (s.scrollTop += Math.min(40, y - rect.bottom + 14)), (moved = true);
  else if (y < rect.top + HEADER_H) (s.scrollTop -= Math.min(40, rect.top + HEADER_H - y + 6)), (moved = true);
  if (x > rect.right - 8) (s.scrollLeft += Math.min(40, x - rect.right + 14)), (moved = true);
  else if (x < rect.left + rnW.value) (s.scrollLeft -= Math.min(40, rect.left + rnW.value - x + 6)), (moved = true);
  if (moved) onDragMove({ clientX: x, clientY: y } as MouseEvent);
}
function onDragEnd() {
  dragging = false;
  window.removeEventListener("mousemove", onDragMove);
  window.removeEventListener("mouseup", onDragEnd);
  clearInterval(dragTimer);
}
onBeforeUnmount(onDragEnd);

// link affordance: while Cmd (Ctrl off macOS) is held, navigable cells look like links
const modHeld = ref(false);
const onModKey = (e: KeyboardEvent) => (modHeld.value = isMod(e));
const onModReset = () => (modHeld.value = false);
window.addEventListener("keydown", onModKey);
window.addEventListener("keyup", onModKey);
window.addEventListener("blur", onModReset);
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onModKey);
  window.removeEventListener("keyup", onModKey);
  window.removeEventListener("blur", onModReset);
});

function onRnDown(e: MouseEvent, r: number) {
  if (e.button === 2) {
    if (!selectedRowSet.value.has(r)) selectRow(r, "single");
    return;
  }
  if (e.button !== 0) return;
  root.value?.focus({ preventScroll: true });
  selectRow(r, e.shiftKey ? "extend" : isMod(e) ? "add" : "single");
}
function onHeaderClick(e: MouseEvent, ci: number) {
  root.value?.focus({ preventScroll: true });
  // A header click only sorts (shift = secondary sort). Alt-click (or the header menu) selects the column.
  if (e.altKey) return selectColumn(ci, isMod(e));
  emit("sort", ci, e.shiftKey);
}
function headerMenu(ci: number): MenuItem[] {
  const c = props.columns[ci];
  const s = sortOf(ci);
  return [
    { label: "Sort ascending", run: () => emit("sort-set", ci, "asc", false) },
    { label: "Sort descending", run: () => emit("sort-set", ci, "desc", false) },
    { label: "Clear sort", disabled: !s, run: () => emit("sort-set", ci, null, false) },
    { separator: true },
    { label: "Select column", hint: kbd("⌥-click"), run: () => selectColumn(ci, false) },
    { label: "Cmd+Shift-click multi-selects key cells", disabled: true },
    { label: "Copy column", run: () => { selectColumn(ci, false); void copy("tsv"); } },
    { label: "Copy column name", run: () => void copyText(c.name) },
    { label: "Autofit width", run: () => autofit(ci) },
    { separator: true },
    { label: "Filter column…", run: () => emit("filter-column", ci, headerRect(ci)) },
  ];
}
function headerRect(ci: number): DOMRect {
  const r = scroller.value?.getBoundingClientRect() ?? new DOMRect(100, 100, 0, 0);
  const x = r.left + (colStarts.value[ci] ?? 0) - (scroller.value?.scrollLeft ?? 0);
  return new DOMRect(x, r.top, widths.value[ci] ?? 100, HEADER_H);
}
function onHeaderContext(e: MouseEvent, ci: number) {
  menu.value = { x: e.clientX, y: e.clientY, items: headerMenu(ci) };
}

// focus state: the selection looks muted while the grid isn't focused (kept, so copy / menu work after refocus)
const gridFocused = ref(false);
function onFocusOut(e: FocusEvent) {
  if (!root.value?.contains(e.relatedTarget as Node | null)) gridFocused.value = false;
}
/** a click on empty grid space (below the last row / right of the last column) clears the selection */
function onScrollerDown(e: MouseEvent) {
  if (e.button !== 0) return;
  const s = scroller.value;
  const t = e.target as HTMLElement;
  if (!s || (t !== s && !t.classList.contains("dg-inner"))) return;
  if (e.offsetX > s.clientWidth || e.offsetY > s.clientHeight) return; // scrollbar
  root.value?.focus({ preventScroll: true });
  clearSelection();
}
function onScroll() {
  if (editing.value?.popover && Date.now() - popoverOpenedAt > 250) cancelEdit();
  emit("scroll");
}

// context menu
const menu = ref<{ x: number; y: number; items: MenuItem[] } | null>(null);
function onContext(e: MouseEvent) {
  const t = e.target as HTMLElement;
  if (t.closest(".dg-head")) return;
  const rnEl = t.closest<HTMLElement>(".dg-rn");
  const p = pointToCell(e.clientX, e.clientY);
  if (!p) return;
  const cell = { row: p.r, col: rnEl ? (active.value?.c ?? 0) : p.c };
  if (!props.contextMenu) return;
  const items = props.contextMenu(cell);
  if (items.length) menu.value = { x: e.clientX, y: e.clientY, items };
}

// column resize
function startResize(e: MouseEvent, ci: number) {
  const startX = e.clientX;
  const startW = widths.value[ci] ?? 100;
  const move = (ev: MouseEvent) => {
    widths.value[ci] = Math.max(40, Math.round(startW + ev.clientX - startX));
  };
  const up = () => {
    window.removeEventListener("mousemove", move);
    window.removeEventListener("mouseup", up);
    userWidths[props.columns[ci].name] = widths.value[ci];
    emit("widths-change", { ...userWidths });
  };
  window.addEventListener("mousemove", move);
  window.addEventListener("mouseup", up);
}
function autofit(ci: number) {
  const c = props.columns[ci];
  let w = headerWidth(c);
  const step = Math.max(1, Math.floor(props.rows.length / 400));
  for (let i = 0; i < props.rows.length; i += step) {
    const t = renderCell(props.rows[i].values[ci], c).text;
    w = Math.max(w, measureText(t.length > 120 ? t.slice(0, 120) : t) + 22);
    if (w > 700) break;
  }
  widths.value[ci] = Math.round(Math.min(w, 700));
  userWidths[c.name] = widths.value[ci];
  emit("widths-change", { ...userWidths });
}

// ---- sort indicators --------------------------------------------------------
function sortOf(ci: number): { dir: "asc" | "desc"; idx: number } | null {
  const i = props.sorts.findIndex((s) => s.col === ci);
  return i < 0 ? null : { dir: props.sorts[i].dir, idx: i + 1 };
}
function onFilterClick(e: MouseEvent, ci: number) {
  emit("filter-column", ci, (e.currentTarget as HTMLElement).getBoundingClientRect());
}

// ---- find highlight -----------------------------------------------------------
const matches = computed<number[]>(() => {
  const q = props.highlight.toLowerCase();
  if (!q) return [];
  const out: number[] = [];
  const n = nCols.value;
  for (let r = 0; r < props.rows.length; r++) {
    const vals = props.rows[r].values;
    for (let c = 0; c < n; c++) {
      if (searchText(vals[c], props.columns[c]).toLowerCase().includes(q)) out.push(r * n + c);
    }
  }
  return out;
});
const matchSet = computed(() => new Set(matches.value));
const matchIdx = ref(-1);
watch(matches, (m) => {
  matchIdx.value = -1;
  emit("find-count", m.length);
});
function gotoMatch(dir: 1 | -1) {
  const m = matches.value;
  if (!m.length) return;
  matchIdx.value = (matchIdx.value + dir + m.length) % m.length;
  const k = m[matchIdx.value];
  selectCell(Math.floor(k / nCols.value), k % nCols.value);
}
function curMatchKey(): number {
  return matchIdx.value >= 0 ? (matches.value[matchIdx.value] ?? -1) : -1;
}
const hl = computed(() => props.highlight.toLowerCase());
function segs(text: string): { t: string; m: boolean }[] {
  const q = hl.value;
  const out: { t: string; m: boolean }[] = [];
  if (!q) return [{ t: text, m: false }];
  const low = text.toLowerCase();
  let i = 0;
  for (;;) {
    const j = low.indexOf(q, i);
    if (j < 0) break;
    if (j > i) out.push({ t: text.slice(i, j), m: false });
    out.push({ t: text.slice(j, j + q.length), m: true });
    i = j + q.length;
  }
  if (i < text.length) out.push({ t: text.slice(i), m: false });
  return out;
}

// ---- cell rendering ------------------------------------------------------------
function rc(r: number, c: number): Rendered {
  return renderCell(props.rows[r]?.values[c], props.columns[c]);
}

// ---- editing ---------------------------------------------------------------------
interface EditState {
  row: number;
  col: number;
  text: string;
  /** text the editor opened with: an untouched editor never emits an edit */
  initial: string;
  popover: boolean;
  loading: boolean;
  note?: string;
  canEdit: boolean;
  full?: CellValue;
  /** number of selected cells the committed value applies to (> 1 = multi-cell edit) */
  multi?: number;
  /** the user typed something (multi edits apply even when the text equals the original) */
  touched?: boolean;
}
type Move = "down" | "right" | "left" | "none" | "edit-right" | "edit-left" | "none-keep";
const editing = ref<EditState | null>(null);
const inlineInput = ref<HTMLInputElement | null>(null);
let escaping = false;
let popoverOpenedAt = 0;

function cancelEdit() {
  editing.value = null;
}
// popovers and menus must not outlive an overlay opening above them
watch(
  () => ui.overlayOpen,
  (open) => {
    if (!open) return;
    if (editing.value?.popover) cancelEdit();
    menu.value = null;
  },
);

function blockedBy(r: number, c: number): boolean {
  const row = props.rows[r];
  if (!props.editable) {
    emit("edit-blocked");
    return true;
  }
  if (row?.state === "deleted") {
    toastOnce("Row is marked for deletion", "info");
    return true;
  }
  const v = row?.values[c];
  if (props.columns[c]?.kind === "binary" || (v !== undefined && isBinary(v as CellValue))) {
    toastOnce("Binary columns can't be edited as text", "info");
    return true;
  }
  return false;
}

async function startEdit(r: number, c: number, typed?: string) {
  if (r < 0 || r >= props.rows.length) return;
  const col = props.columns[c];
  const v = props.rows[r].values[c];
  const pop = typed === undefined && needsPopover(v, col);
  if (!props.editable) {
    // read-only: long/JSON values can still be viewed; everything else is an edit attempt
    if (typed === undefined && pop) return openPopover(r, c, false);
    emit("edit-blocked");
    return;
  }
  // typing / Enter / F2 with several cells selected edits all of them (per-cell checks happen on commit)
  const multiN = isSelected(r, c) ? uniqueCellCount() : 1;
  if (multiN > 1) {
    if (props.rows[r].state === "deleted") return void toastOnce("Row is marked for deletion", "info");
  } else if (blockedBy(r, c)) return;
  if (pop) return openPopover(r, c, true, multiN > 1 ? multiN : undefined);
  const orig = isDefaultValue(v) ? "" : cellText(v);
  const text = typed ?? orig;
  escaping = false;
  editing.value = { row: r, col: c, text, initial: orig, popover: false, loading: false, canEdit: true, multi: multiN > 1 ? multiN : undefined, touched: typed !== undefined };
  revealCell(r, c);
  await nextTick();
  inlineInput.value?.focus();
  if (typed === undefined) inlineInput.value?.select();
  else inlineInput.value?.setSelectionRange(text.length, text.length);
}

async function openPopover(r: number, c: number, canEdit: boolean, multi?: number) {
  const v = props.rows[r].values[c];
  const truncated = isLongText(v as CellValue) && (v as { truncated: boolean }).truncated;
  const orig = isDefaultValue(v) ? "" : cellText(v);
  const st: EditState = {
    row: r, col: c, text: orig, initial: orig, popover: true, loading: truncated && !!props.loadFull, canEdit,
    note: undefined, multi,
  };
  if (truncated && !props.loadFull) {
    st.canEdit = false;
    st.note = "Value is truncated; the full value is not available.";
  }
  popoverOpenedAt = Date.now();
  editing.value = st;
  revealCell(r, c);
  if (truncated && props.loadFull) {
    try {
      const full = await props.loadFull(r, c);
      if (editing.value === st) {
        if (full === null) {
          st.note = "Could not load the full value; showing the truncated text (read-only).";
          st.canEdit = false;
        } else {
          st.text = st.initial = cellText(full);
          st.full = full;
        }
        st.loading = false;
      }
    } catch {
      if (editing.value === st) {
        st.note = "Could not load the full value; showing the truncated text (read-only).";
        st.canEdit = false;
        st.loading = false;
      }
    }
  }
}

function popoverAnchor(): { x: number; y: number } {
  const e = editing.value;
  const s = scroller.value;
  if (!e || !s) return { x: 100, y: 100 };
  const rect = s.getBoundingClientRect();
  return {
    x: rect.left + (colStarts.value[e.col] ?? 0) - s.scrollLeft,
    y: rect.top + HEADER_H + (e.row + 1) * rowH() - s.scrollTop + 2,
  };
}

/** inline validation message of the open editor ("Not a valid integer"); the edit is not staged while set */
const editError = ref("");
watch(editing, (v) => {
  if (!v) editError.value = "";
});
const editPkWarning = computed(() => {
  const e = editing.value;
  return e && props.columns[e.col]?.pk ? "Changing the primary key" : "";
});

function commitText(text: string, move: Move, refocus = true) {
  const e = editing.value;
  if (!e) return;
  const col = props.columns[e.col];
  if (e.multi && e.multi > 1) return commitMulti(text, e, move);
  if (text !== e.initial) {
    const err = validateInput(col, text);
    if (err) {
      // keep the editor open with the error and don't stage anything
      editError.value = err;
      if (refocus && !e.popover) nextTick(() => inlineInput.value?.focus());
      return;
    }
  }
  editing.value = null;
  const row = props.rows[e.row];
  let changed = text !== e.initial;
  if (changed && col.kind === "json") {
    const a = minifyJson(text);
    if (a !== null && a === minifyJson(e.initial)) changed = false;
  }
  if (changed && e.full === undefined && isLongText(row?.values[e.col] as CellValue) && (row.values[e.col] as { truncated: boolean }).truncated) {
    changed = false; // never stage truncated text
  }
  if (changed) {
    if (isBlankInvalid(col, text)) {
      if (row.state === "new") {
        if (!isDefaultValue(row.values[e.col])) emit("apply-edits", { edits: [{ row: e.row, col: e.col, value: SQL_DEFAULT }], appended: [] });
      } else toastOnce(`"${col.name}" cannot be empty (NOT NULL)`, "warning");
    } else {
      emit("apply-edits", { edits: [{ row: e.row, col: e.col, value: coerceInput(col, text) }], appended: [] });
    }
  }
  after(e.row, e.col, move);
}
/** Applies one typed value to every selected cell as a single staged batch (one undo step). */
function commitMulti(text: string, e: EditState, move: Move) {
  const unchanged = !e.touched && text === e.initial;
  editing.value = null;
  if (unchanged) return after(e.row, e.col, "none-keep");
  const edits: CellEdit[] = [];
  const skips = new Map<string, number>();
  const skip = (why: string) => skips.set(why, (skips.get(why) ?? 0) + 1);
  for (const r of selectedRowIndexes()) {
    const row = props.rows[r];
    for (const c of selectedColSet.value) {
      if (!isSelected(r, c)) continue;
      const col = props.columns[c];
      const typeName = col.type.replace(/\(.*\)/, "") || col.kind;
      if (row.state === "deleted") { skip("deleted row"); continue; }
      if (col.kind === "binary" || isBinary(row.values[c] as CellValue)) { skip("binary"); continue; }
      if (isLongText(row.values[c] as CellValue) && (row.values[c] as { truncated: boolean }).truncated) { skip("truncated value"); continue; }
      let v: CellValue;
      if (text.trim() === "") {
        if (col.nullable) v = null;
        else if (NON_TEXT.includes(col.kind)) { skip(`NOT NULL ${typeName}`); continue; }
        else v = "";
      } else {
        if (validateInput(col, text)) { skip(`not valid for ${typeName}`); continue; }
        v = coerceInput(col, text);
      }
      if (cellEquals(row.values[c], v) && row.state !== "new") continue;
      edits.push({ row: r, col: c, value: v });
    }
  }
  if (edits.length) emit("apply-edits", { edits, appended: [] });
  const skippedN = [...skips.values()].reduce((a, b) => a + b, 0);
  if (edits.length || skippedN) {
    const why = [...skips.keys()].join("; ");
    toastOnce(
      `${edits.length ? `Applied to ${edits.length} cell${edits.length > 1 ? "s" : ""}` : "Nothing applied"}${skippedN ? ` · ${skippedN} skipped (${why})` : ""}`,
      edits.length ? "success" : "warning", 4000, 0,
    );
  }
  after(e.row, e.col, "none-keep");
}
function commitValue(value: CellValue) {
  const e = editing.value;
  if (!e) return;
  editing.value = null;
  const cur = props.rows[e.row]?.values[e.col];
  if (!(cellEquals(cur, value) && props.rows[e.row]?.state !== "new")) {
    emit("apply-edits", { edits: [{ row: e.row, col: e.col, value }], appended: [] });
  }
  root.value?.focus({ preventScroll: true });
}
function after(r: number, c: number, move: Move) {
  nextTick(() => {
    root.value?.focus({ preventScroll: true });
    if (move === "none-keep") return; // multi-cell edit: the selection stays as it is
    if (move === "down") selectCell(Math.min(r + 1, props.rows.length - 1), c);
    else if (move === "right") moveHoriz(r, c, 1);
    else if (move === "left") moveHoriz(r, c, -1);
    else if (move === "edit-right" || move === "edit-left") {
      moveHoriz(r, c, move === "edit-right" ? 1 : -1);
      const a = active.value;
      if (a && props.editable) void startEdit(a.r, a.c);
    } else selectCell(r, c);
  });
}
function moveHoriz(r: number, c: number, d: 1 | -1) {
  let nr = r;
  let nc = c + d;
  if (nc >= nCols.value) (nc = 0), nr++;
  else if (nc < 0) (nc = nCols.value - 1), nr--;
  nr = Math.max(0, Math.min(nr, props.rows.length - 1));
  selectCell(nr, nc);
}
function onInlineKey(e: KeyboardEvent) {
  const ed = editing.value;
  if (!ed) return e.stopPropagation();
  const el = e.target as HTMLInputElement;
  if (isMod(e) && e.key.toLowerCase() === "s") {
    // commit first, then let the event through so the view opens the save preview
    commitText(el.value, "none");
    return;
  }
  e.stopPropagation();
  if (e.key === "Enter") {
    e.preventDefault();
    if (e.shiftKey || e.altKey) {
      // expand into the popover editor
      escaping = true;
      editing.value = { ...ed, text: el.value, popover: true, loading: false };
      popoverOpenedAt = Date.now();
      return;
    }
    commitText(el.value, "down");
  } else if (e.key === "Tab") {
    e.preventDefault();
    commitText(el.value, e.shiftKey ? "edit-left" : "edit-right");
  } else if (e.key === "Escape") {
    e.preventDefault();
    escaping = true;
    editing.value = null;
    root.value?.focus({ preventScroll: true });
  }
}
/** keep editing.text in sync so a re-render (error bubble) never resets what the user typed */
function onInlineInput(e: Event) {
  if (editing.value) {
    editing.value.text = (e.target as HTMLInputElement).value;
    editing.value.touched = true;
  }
  editError.value = "";
}
function onInlineBlur(e: FocusEvent) {
  if (escaping) {
    escaping = false;
    return;
  }
  if (editing.value && !editing.value.popover) commitText((e.target as HTMLInputElement).value, "none", false);
}

/** Stages NULL / DEFAULT for every selected cell (context menu: "Set NULL" / "Set default"). */
function setSelection(value: CellValue | "default") {
  if (!props.editable) return emit("edit-blocked");
  const edits: CellEdit[] = [];
  let skipped = 0;
  for (const r of selectedRowIndexes()) {
    if (props.rows[r].state === "deleted") continue;
    for (const c of selectedColSet.value) {
      if (!isSelected(r, c)) continue;
      const col = props.columns[c];
      const cur = props.rows[r].values[c];
      if (col.kind === "binary" || isBinary(cur as CellValue)) {
        skipped++;
        continue;
      }
      const v: CellValue = value === "default" ? SQL_DEFAULT : value;
      if (v === null && !col.nullable) {
        skipped++;
        continue;
      }
      if (cellEquals(cur, v) && props.rows[r].state !== "new") continue;
      edits.push({ row: r, col: c, value: v });
    }
  }
  if (!edits.length) {
    if (skipped) toastOnce(value === null ? "The selected columns are not nullable" : "Binary columns can't be edited", "info");
    return;
  }
  emit("apply-edits", { edits, appended: [] });
}
const setNull = () => setSelection(null);
const setDefault = () => setSelection("default");

/** Delete / Backspace: stages NULL for a single nullable cell only. Larger selections need the menu. */
function deleteKey() {
  if (!props.editable) return emit("edit-blocked");
  // whole rows selected, or a selection spanning several rows: stage those rows for deletion (toggle)
  const fullWidth = nCols.value > 1 && ranges.value.some((g) => g.c1 === 0 && g.c2 === nCols.value - 1);
  if (fullWidth || selectedRowSet.value.size > 1) return void emit("delete-rows");
  const cells = ranges.value.reduce((n, g) => n + (g.r2 - g.r1 + 1) * (g.c2 - g.c1 + 1), 0);
  if (cells !== 1 || !active.value) {
    toastOnce("To set several cells to NULL use the right-click menu: Set NULL", "info");
    return;
  }
  const col = props.columns[active.value.c];
  if (!col.nullable) return toastOnce(`"${col.name}" is not nullable`, "info");
  setSelection(null);
}

// ---- copy / paste --------------------------------------------------------------------
type CopyInp = Parameters<typeof formatCopy>[1];
function copyInput(rowsOverride?: CellValue[][]): CopyInp | null {
  const rowsIdx = selectedRowIndexes();
  const cols = [...selectedColSet.value].sort((a, b) => a - b);
  if (!rowsIdx.length || !cols.length) return null;
  const single = ranges.value.length === 1;
  const ctx = props.copyContext;
  return {
    columns: props.columns,
    cols,
    rows: rowsOverride ?? rowsIdx.map((r) => props.rows[r].values),
    mask: single ? undefined : (i, c) => isSelected(rowsIdx[i], c),
    driver: (ctx?.driver ?? "") as Driver | "",
    schema: ctx?.schema ?? "",
    table: ctx?.table ?? "",
  };
}
function isTruncated(v: CellValue | undefined): boolean {
  return (isLongText(v as CellValue) || isBinary(v as CellValue)) && !!(v as { truncated?: boolean }).truncated;
}
function hasTruncated(): boolean {
  for (const r of selectedRowIndexes()) for (const c of selectedColSet.value) if (isSelected(r, c) && isTruncated(props.rows[r].values[c])) return true;
  return false;
}
/** Rows for the selection with truncated values replaced by their full value (CellValue); null = aborted. */
async function fullRows(): Promise<CellValue[][] | null> {
  const rowsIdx = selectedRowIndexes();
  const rows = rowsIdx.map((r) => props.rows[r].values);
  const need: { i: number; r: number; c: number }[] = [];
  rowsIdx.forEach((r, i) => {
    for (const c of selectedColSet.value) if (isSelected(r, c) && isTruncated(rows[i][c])) need.push({ i, r, c });
  });
  if (!need.length) return rows;
  if (!props.loadFull) {
    toastOnce("The selection contains truncated values that can't be loaded here, so nothing was copied.", "warning");
    return null;
  }
  if (need.length > 500) {
    toastOnce(`The selection contains ${need.length} truncated values (limit 500). Select fewer rows to copy them in full.`, "warning");
    return null;
  }
  if (need.length > 25) {
    const ok = await ui.confirm({
      title: "Load long values?",
      message: `${need.length} selected cells hold long values that are cut in the grid. Each one is loaded in full before copying (${need.length} requests).`,
      confirmText: "Load and copy",
    });
    if (!ok) return null;
  }
  const out = rows.map((r) => r.slice());
  let failed: string | null = null;
  let next = 0;
  const worker = async () => {
    while (next < need.length && !failed) {
      const n = need[next++];
      const full = await props.loadFull!(n.r, n.c);
      if (full === null) failed = props.columns[n.c].name;
      else out[n.i][n.c] = full;
    }
  };
  await Promise.all(Array.from({ length: Math.min(8, need.length) }, worker));
  if (failed) {
    toastOnce(`Could not load the full value of "${failed}", so nothing was copied.`, "warning");
    return null;
  }
  return out;
}
/** Text for a format from the current selection, with full values (async). */
async function selectionText(format: CopyFormatId): Promise<string | null> {
  const rows = await fullRows();
  const inp = rows && copyInput(rows);
  return inp ? formatCopy(format, inp) : null;
}
async function copy(format: CopyFormatId = "tsv") {
  if (!ranges.value.length) selectAll();
  const cells = uniqueCellCount();
  if (cells > 10_000 && format !== "column_names") {
    const ok = await ui.confirm({
      title: "Copy a large selection?",
      message: `Copy ${cells.toLocaleString("en-US")} cells to the clipboard?`,
      confirmText: "Copy",
    });
    if (!ok) return;
  }
  const text = await selectionText(format);
  if (text === null) return;
  const ok = await copyText(text);
  const rows = selectedRowSet.value.size;
  const cols = selectedColSet.value.size;
  if (ok) ui.toast(format === "column_names" ? `Copied ${cols} column name${cols > 1 ? "s" : ""}` : `Copied ${rows} row${rows > 1 ? "s" : ""} × ${cols} column${cols > 1 ? "s" : ""}`, "success", 1800);
  else ui.toast("Could not access the clipboard", "error");
  return ok;
}
/** Cut: copy, then clear the selected cells (NULL where nullable, '' for text columns). */
async function cut() {
  if (!props.editable) return void (await copy("tsv"));
  const ok = await copy("tsv");
  if (!ok) return;
  const edits: CellEdit[] = [];
  for (const r of selectedRowIndexes()) {
    if (props.rows[r].state === "deleted") continue;
    for (const c of selectedColSet.value) {
      if (!isSelected(r, c)) continue;
      const col = props.columns[c];
      if (col.kind === "binary") continue;
      const cur = props.rows[r].values[c];
      const v: CellValue | undefined = col.nullable ? null : NON_TEXT.includes(col.kind) ? undefined : "";
      if (v === undefined || cellEquals(cur, v)) continue;
      edits.push({ row: r, col: c, value: v });
    }
  }
  if (edits.length) emit("apply-edits", { edits, appended: [] });
}

async function paste(textArg?: string) {
  if (!props.editable) return emit("edit-blocked");
  const text = textArg ?? (await readClipboardText());
  if (text === null) return toastOnce(kbd("Could not read the clipboard. Use ⌘V with the grid focused."), "warning");
  if (text === "") return;
  const m = parseDelimited(text);
  if (!ranges.value.length && !active.value) return ui.toast("Select a cell to paste into", "info");
  const rs = ranges.value.length ? ranges.value : [mk(active.value!, active.value!)];
  const r0 = Math.min(...rs.map((g) => g.r1));
  const c0 = Math.min(...rs.map((g) => g.c1));
  const r1 = Math.max(...rs.map((g) => g.r2));
  const c1 = Math.max(...rs.map((g) => g.c2));
  let skipped = 0;
  let invalid = 0;
  const invalidTypes = new Set<string>();
  /** undefined = skip this cell */
  const conv = (c: number, t: string): CellValue | undefined => {
    const col = props.columns[c];
    if (col.kind === "binary") return undefined;
    if (t === PASTE_NULL) return col.nullable ? null : NON_TEXT.includes(col.kind) ? undefined : "";
    if (t === PASTE_DEFAULT) return SQL_DEFAULT;
    if (isBlankInvalid(col, t)) return undefined;
    if (validateInput(col, t)) {
      invalid++;
      invalidTypes.add(col.type.replace(/\(.*\)/, "") || col.kind);
      return undefined;
    }
    return coerceInput(col, t);
  };
  const edits: CellEdit[] = [];
  const appended: (CellValue | undefined)[][] = [];
  const n = props.rows.length;
  const put = (r: number, c: number, t: string) => {
    if (c >= nCols.value) return;
    const before = invalid;
    const v = conv(c, t);
    if (v === undefined) {
      if (invalid === before) skipped++; // invalid values are reported separately
      return;
    }
    if (r >= n) {
      const k = r - n;
      appended[k] ??= [];
      appended[k][c] = v;
      return;
    }
    const row = props.rows[r];
    if (row.state === "deleted") return;
    if (cellEquals(v, row.values[c]) && row.state !== "new") return;
    edits.push({ row: r, col: c, value: v });
  };
  let sel: Range;
  if (m.length === 1 && m[0].length === 1 && (r1 > r0 || c1 > c0)) {
    for (let r = r0; r <= r1; r++) for (let c = c0; c <= c1; c++) if (isSelected(r, c)) put(r, c, m[0][0]);
    sel = { r1: r0, c1: c0, r2: r1, c2: c1 };
  } else {
    const sr = ranges.value.length ? r0 : active.value!.r;
    const sc = ranges.value.length ? c0 : active.value!.c;
    m.forEach((line, i) => line.forEach((t, j) => put(sr + i, sc + j, t)));
    const w = Math.max(...m.map((l) => l.length));
    sel = { r1: sr, c1: sc, r2: sr + m.length - 1, c2: Math.min(nCols.value - 1, sc + w - 1) };
  }
  if (skipped) toastOnce(`${skipped} value${skipped > 1 ? "s" : ""} skipped (binary or empty NOT NULL columns)`, "info");
  if (invalid) toastOnce(`${invalid} value${invalid > 1 ? "s" : ""} skipped: not valid for ${[...invalidTypes].join(", ")}`, "warning", 5000);
  if (!edits.length && !appended.length) return skipped ? undefined : ui.toast("Nothing to paste (values unchanged)", "info");
  emit("apply-edits", { edits, appended: appended.filter(Boolean) });
  await nextTick();
  const maxR = Math.max(props.rows.length - 1, 0);
  sel.r2 = Math.min(sel.r2, Math.max(maxR, n + appended.length - 1));
  ranges.value = [sel];
  anchor = { r: sel.r1, c: sel.c1 };
  active.value = { r: sel.r1, c: sel.c1 };
}

// ---- keyboard ---------------------------------------------------------------------------
let pasteTimer: number | undefined;
function onKeydown(e: KeyboardEvent) {
  const t = e.target as HTMLElement;
  if (t.closest("input,textarea,select")) return;
  if (editing.value) return;
  const mod = isMod(e);
  const nr = props.rows.length;
  const nc = nCols.value;
  const k = e.key;
  const handled = () => {
    e.preventDefault();
    e.stopPropagation();
  };

  if (mod && !e.altKey) {
    const lk = k.toLowerCase();
    if (lk === "a") return handled(), selectAll();
    if (lk === "c") return handled(), void copy(e.shiftKey ? "tsv_header" : "tsv");
    if (lk === "x") return handled(), void cut();
    if (lk === "v") {
      if (!props.editable) return handled(), emit("edit-blocked");
      // Don't preventDefault: the native `paste` event delivers clipboardData (no permission prompt).
      // If it never arrives (no Edit menu / key equivalent), fall back to the async Clipboard API.
      e.stopPropagation();
      clearTimeout(pasteTimer);
      pasteTimer = window.setTimeout(() => void paste(), 150);
      return;
    }
    if (k === "ArrowUp" && nr) return handled(), e.shiftKey ? extendTo(0, active.value?.c ?? 0) : selectCell(0, active.value?.c ?? 0);
    if (k === "ArrowDown" && nr) return handled(), e.shiftKey ? extendTo(nr - 1, active.value?.c ?? 0) : selectCell(nr - 1, active.value?.c ?? 0);
    if (k === "ArrowLeft" && nc) return handled(), e.shiftKey ? extendTo(active.value?.r ?? 0, 0) : selectCell(active.value?.r ?? 0, 0);
    if (k === "ArrowRight" && nc) return handled(), e.shiftKey ? extendTo(active.value?.r ?? 0, nc - 1) : selectCell(active.value?.r ?? 0, nc - 1);
    if (k === "Home") return handled(), selectCell(0, 0);
    if (k === "End") return handled(), selectCell(nr - 1, nc - 1);
    return; // other combos bubble (Cmd+S, Cmd+Z, Cmd+Backspace ... handled by the view)
  }
  if (!nr || !nc) return;
  if (!active.value) {
    // nothing selected yet: the first navigation key selects the first cell
    if (["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "PageUp", "PageDown", "Home", "End", "Tab"].includes(k)) {
      handled();
      return selectCell(0, 0);
    }
    if (["Enter", "F2", "Delete", "Backspace", "Escape"].includes(k) || (k.length === 1 && !e.altKey)) return;
  }
  const cur = active.value ?? { r: 0, c: 0 };
  const go = (r: number, c: number) => (handled(), e.shiftKey ? extendTo(r, c) : selectCell(r, c));
  switch (k) {
    case "ArrowUp": return go(cur.r - 1, cur.c);
    case "ArrowDown": return go(cur.r + 1, cur.c);
    case "ArrowLeft": return go(cur.r, cur.c - 1);
    case "ArrowRight": return go(cur.r, cur.c + 1);
    case "PageUp": return go(cur.r - visibleRows(), cur.c);
    case "PageDown": return go(cur.r + visibleRows(), cur.c);
    case "Home": return go(cur.r, 0);
    case "End": return go(cur.r, nc - 1);
    case "Tab": {
      handled();
      return moveHoriz(cur.r, cur.c, e.shiftKey ? -1 : 1);
    }
    case "Enter":
    case "F2":
      handled();
      return void startEdit(cur.r, cur.c);
    case "Escape":
      if (ranges.value.length) {
        handled();
        clearSelection(); // Esc clears the selection (the editor handles its own Esc)
      }
      return;
    case "Delete":
    case "Backspace":
      handled();
      return deleteKey();
    case " ":
      if (e.shiftKey) return handled(), selectRow(cur.r, "single");
      return;
  }
  if (k.length === 1 && !e.altKey && !e.ctrlKey && !e.metaKey) {
    handled();
    void startEdit(cur.r, cur.c, k);
  }
}

function onPasteEvent(e: ClipboardEvent) {
  const t = e.target as HTMLElement;
  if (t.closest("input,textarea")) return;
  clearTimeout(pasteTimer);
  e.preventDefault();
  if (!props.editable) return emit("edit-blocked");
  const text = e.clipboardData?.getData("text/plain");
  if (text) void paste(text);
}
function onCopyEvent(e: ClipboardEvent) {
  const t = e.target as HTMLElement;
  if (t.closest("input,textarea")) return;
  e.preventDefault();
  if (!ranges.value.length) return;
  if (hasTruncated() || uniqueCellCount() > 10_000) return void copy("tsv"); // needs async loading / a confirm
  const inp = copyInput();
  if (inp) e.clipboardData?.setData("text/plain", formatCopy("tsv", inp));
}
function onCutEvent(e: ClipboardEvent) {
  const t = e.target as HTMLElement;
  if (t.closest("input,textarea")) return;
  e.preventDefault();
  void cut();
}

function focus() {
  root.value?.focus({ preventScroll: true });
}

defineExpose({
  copy, cut, paste, setNull, setDefault, selectionText, selectedRowIndexes, selectCell, selectRow, startEdit, focus,
  gotoMatch, scrollToTop: () => scroller.value && (scroller.value.scrollTop = 0), clearSelection,
  activeCell: () => (active.value ? { row: active.value.r, col: active.value.c } : null),
  viewValue: (r: number, c: number) => openPopover(r, c, false),
  selectedRowsInColumn, focusColumn,
});

const editStyle = computed(() => {
  const e = editing.value;
  if (!e || e.popover) return null;
  return { left: (colStarts.value[e.col] ?? 0) + "px", top: HEADER_H + e.row * rowH() + "px", width: Math.max(widths.value[e.col] ?? 100, 90) + "px", height: rowH() + "px" };
});
const popTitle = computed(() => (editing.value ? props.columns[editing.value.col]?.name ?? "" : ""));
const filteredSet = computed(() => new Set(props.filteredCols));
</script>

<template>
  <div
    ref="root"
    class="dg"
    :class="{ mod: modHeld, inactive: !gridFocused && !editing && !menu }"
    data-focus-target
    tabindex="0"
    @focusin="gridFocused = true"
    @focusout="onFocusOut"
    @keydown="onKeydown"
    @paste="onPasteEvent"
    @copy="onCopyEvent"
    @cut="onCutEvent"
  >
    <div
      ref="scroller" class="dg-scroll" :class="{ stale: loading && rows.length > 0 }" @contextmenu.prevent="onContext"
      @scroll.passive="onScroll" @mousedown="onScrollerDown"
    >
      <div class="dg-inner" :style="{ width: totalW + 'px', height: Math.max(totalH, HEADER_H + 1) + 'px' }">
        <!-- sticky header -->
        <div class="dg-head" :style="{ width: totalW + 'px', height: HEADER_H + 'px' }">
          <div class="dg-corner" :style="{ width: rnW + 'px', height: HEADER_H + 'px' }" title="Select all" @click="selectAll(); focus()"></div>
          <div
            v-for="vc in colItems" :key="String(vc.key)"
            class="dg-th"
            :class="{ sel: selectedColSet.has(vc.index) && ranges.some((g) => g.r1 === 0 && g.r2 === rows.length - 1 && vc.index >= g.c1 && vc.index <= g.c2) }"
            :style="{ left: vc.start + 'px', width: vc.size + 'px', height: HEADER_H + 'px' }"
            :title="`${columns[vc.index].name}  ${columns[vc.index].type}`.trim()"
            @click="onHeaderClick($event, vc.index)"
            @contextmenu.stop.prevent="onHeaderContext($event, vc.index)"
          >
            <div class="dg-th-in">
              <Icon v-if="columns[vc.index].pk" name="key" :size="11" class="pk" />
              <Icon v-else-if="columns[vc.index].fk" name="link" :size="11" class="fk" />
              <span class="dg-name">{{ columns[vc.index].name }}</span>
              <span v-if="showType(vc.index, vc.size)" class="dg-type">{{ columns[vc.index].type }}</span>
              <span v-if="sortOf(vc.index)" class="dg-sort">
                <Icon :name="sortOf(vc.index)!.dir === 'asc' ? 'sortUp' : 'sortDown'" :size="11" />
                <sub v-if="sorts.length > 1">{{ sortOf(vc.index)!.idx }}</sub>
              </span>
              <span class="flex-1"></span>
              <button
                class="dg-fi" :class="{ on: filteredSet.has(columns[vc.index].name) }" title="Filter column"
                @click.stop="onFilterClick($event, vc.index)" @mousedown.stop
              ><Icon name="filter" :size="11" /></button>
            </div>
            <span class="dg-rz" @mousedown.stop.prevent="startResize($event, vc.index)" @click.stop @dblclick.stop="autofit(vc.index)"></span>
          </div>
        </div>

        <!-- rows -->
        <div
          v-for="vr in rowItems" :key="String(vr.key)"
          class="dg-row"
          :class="{ 'r-new': rows[vr.index]?.state === 'new', 'r-del': rows[vr.index]?.state === 'deleted', 'r-chg': rows[vr.index]?.changed?.some(Boolean) }"
          :style="{ top: vr.start + 'px', height: rowH() + 'px', width: totalW + 'px' }"
        >
          <template v-if="rows[vr.index]">
            <div
              class="dg-rn" :class="{ touched: rowTouched(vr.index) }" :style="{ width: rnW + 'px' }"
              @mousedown="onRnDown($event, vr.index)" @dblclick="emit('open-record', vr.index)"
            >{{ rows[vr.index].state === 'new' ? '+' : rowOffset + vr.index + 1 }}</div>
            <template v-for="vc in colItems" :key="String(vc.key)">
              <template v-for="rd in [rc(vr.index, vc.index)]" :key="0">
                <div
                  class="dg-cell"
                  :class="[
                    rd.cls, rd.num && 'num',
                    rows[vr.index].changed?.[vc.index] && 'chg',
                    isSelected(vr.index, vc.index) && 'sel',
                    modHeld && navigable?.(vr.index, vc.index) && 'nav',
                    active && active.r === vr.index && active.c === vc.index && 'act',
                    matchSet.has(vr.index * nCols + vc.index) && (curMatchKey() === vr.index * nCols + vc.index ? 'mark-cur' : 'mark'),
                  ]"
                  :style="[{ left: vc.start + 'px', width: vc.size + 'px' }, selStyle(vr.index, vc.index) ?? '']"
                  :title="rd.cls === 'dim' ? 'Binary value (not editable as text)' : navigable?.(vr.index, vc.index) ? navTitle?.(vr.index, vc.index) : undefined"
                  @mousedown="onCellDown($event, vr.index, vc.index)"
                  @dblclick="startEdit(vr.index, vc.index)"
                >
                  <template v-if="highlight && matchSet.has(vr.index * nCols + vc.index) && rd.cls !== 'null'">
                    <template v-for="(s, i) in segs(rd.text)" :key="i"><mark v-if="s.m">{{ s.t }}</mark><template v-else>{{ s.t }}</template></template>
                  </template>
                  <template v-else>{{ rd.text }}</template><span v-if="rd.truncated" class="dg-ell" title="Truncated - open to load the full value">…</span>
                </div>
              </template>
            </template>
          </template>
        </div>

        <input
          v-if="editing && !editing.popover && editStyle"
          ref="inlineInput"
          class="dg-edit mono selectable"
          :style="editStyle"
          :value="editing.text"
          spellcheck="false"
          @keydown="onInlineKey"
          @input="onInlineInput"
          @blur="onInlineBlur"
          @mousedown.stop
        />

        <div
          v-if="editing && !editing.popover && editStyle && editing.multi" class="dg-edit-badge"
          :style="{ left: `calc(${editStyle.left} + ${editStyle.width} + 6px)`, top: editStyle.top }"
        >→ {{ editing.multi }} cells</div>
        <div
          v-if="editing && !editing.popover && editStyle && (editError || editPkWarning) && !editing.multi" class="dg-edit-msg" :class="{ err: !!editError }"
          :style="{ left: editStyle.left, top: HEADER_H + (editing.row + 1) * rowH() + 2 + 'px' }"
        >{{ editError || editPkWarning }}</div>
        <div v-if="!rows.length && !loading" class="dg-empty dim" :style="{ top: HEADER_H + 24 + 'px' }">{{ emptyText }}</div>
        <div v-if="!rows.length && loading" class="dg-skel" :style="{ top: HEADER_H + 'px' }">
          <div v-for="i in 14" :key="i" class="dg-skel-row"><span :style="{ width: 20 + ((i * 37) % 55) + '%' }"></span></div>
        </div>
      </div>
    </div>
    <div v-if="loading" class="dg-loading"><span class="dg-bar"></span></div>

    <CellPopover
      v-if="editing && editing.popover"
      :x="popoverAnchor().x" :y="popoverAnchor().y"
      :title="popTitle" :type="columns[editing.col].type" :kind="columns[editing.col].kind"
      :initial="editing.text" :loading="editing.loading" :note="editing.note"
      :can-edit="editing.canEdit && editable" :nullable="columns[editing.col].nullable"
      :error="editError" :warning="editPkWarning" :multi-count="editing.multi"
      @commit="(t) => commitText(t, 'none')"
      @set-null="commitValue(null)"
      @set-default="commitValue(SQL_DEFAULT)"
      @cancel="cancelEdit(); focus()"
    />
    <ContextMenu v-if="menu" :x="menu.x" :y="menu.y" :items="menu.items" @close="menu = null; focus()" />
  </div>
</template>

<style scoped>
.dg { position: relative; flex: 1; min-height: 0; min-width: 0; display: flex; outline: none; font-family: var(--mono); }
.dg-scroll { flex: 1; overflow: auto; position: relative; transition: opacity .12s; }
.dg-scroll.stale { opacity: .55; }
.dg-skel { position: absolute; left: 0; right: 0; }
.dg-skel-row { height: 22px; display: flex; align-items: center; padding: 0 12px; border-bottom: 1px solid var(--bd); }
.dg-skel-row span { height: 8px; border-radius: 4px; background: var(--bg3); animation: dgpulse 1.2s ease-in-out infinite; }
@keyframes dgpulse { 50% { opacity: .4; } }
.dg-inner { position: relative; }
.dg-head { position: sticky; top: 0; z-index: 6; background: var(--head); border-bottom: 1px solid var(--bd2); }
.dg-corner { position: sticky; left: 0; z-index: 8; background: var(--head); border-right: 1px solid var(--bd); }
.dg-th {
  position: absolute; top: 0; background: var(--head); border-right: 1px solid var(--cell-bd); font: 600 11.5px var(--sans);
  white-space: nowrap; overflow: hidden; cursor: default;
}
.dg-th:hover { background: var(--hov); }
.dg-th.sel { background: color-mix(in srgb, var(--selbg), var(--head)); box-shadow: inset 0 -2px 0 var(--selbd); }
.dg-th-in { display: flex; align-items: center; gap: 4px; height: 100%; padding: 0 8px; }
.dg-name { flex: 0 1 auto; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.dg-type { font: 10px var(--mono); color: var(--dim); font-weight: 400; overflow: hidden; text-overflow: ellipsis; flex: 0 1 auto; min-width: 0; }
.dg-th :deep(.pk) { color: var(--pk); }
.dg-th :deep(.fk) { color: var(--fk); }
.dg-sort { flex: none; display: inline-flex; align-items: flex-end; color: var(--ac); }
.dg-sort sub { font: 9px var(--mono); }
.dg-fi { position: absolute; right: 6px; top: 50%; transform: translateY(-50%); opacity: 0; background: var(--bg2); border: 0; padding: 2px; border-radius: 3px; color: var(--dim); display: inline-flex; cursor: default; }
.dg-th:hover .dg-fi, .dg-fi.on { opacity: 1; }
.dg-fi.on { color: var(--ac); }
.dg-fi:hover { background: var(--bg3); color: var(--tx); }
.dg-rz { position: absolute; right: 0; top: 0; bottom: 0; width: 6px; cursor: col-resize; }
.dg-rz:hover { background: var(--ac); opacity: .5; }

.dg-row { position: absolute; left: 0; font: 12px var(--mono); }
.dg-rn {
  position: sticky; left: 0; z-index: 4; height: 100%; background: var(--head); color: var(--dim); text-align: right;
  font-size: 10px; padding: 0 8px 0 0; line-height: 22px; border-right: 1px solid var(--bd); border-bottom: 1px solid var(--row-bd);
  cursor: default; box-sizing: border-box; overflow: hidden;
}
.dg-rn.touched { background: var(--bg3); color: var(--tx); }
.r-new .dg-rn { color: var(--chg-new); box-shadow: inset 2px 0 0 var(--chg-new); }
.r-del .dg-rn { box-shadow: inset 2px 0 0 var(--chg-del); }
.r-chg .dg-rn { box-shadow: inset 2px 0 0 var(--chg-upd); } /* amber only ever means a pending change */
/* inactive (unfocused) grid: muted neutral selection, no accent */
.dg.inactive { --cellbg: transparent; --selbg: color-mix(in srgb, var(--dim) 16%, transparent); --selbd: color-mix(in srgb, var(--dim) 55%, transparent); }
.dg.inactive .dg-cell.act { box-shadow: inset 0 0 0 1px var(--selbd) !important; }
.dg.inactive .dg-cell.act.sel { background-image: none; }
.dg.inactive .dg-th.sel { box-shadow: inset 0 -2px 0 var(--selbd); }
.dg-cell {
  position: absolute; top: 0; height: 100%; padding: 0 8px; line-height: 21px; white-space: nowrap; overflow: hidden;
  text-overflow: ellipsis; border-bottom: 1px solid var(--row-bd); border-right: 1px solid var(--cell-bd); cursor: cell;
  box-sizing: border-box;
}
.dg-cell.num { text-align: right; }
.dg-cell.null, .dg-cell.default { color: var(--dim); font-style: italic; font-size: 11px; }
.dg-cell.dim { color: var(--dim); }
.dg-cell.bool-t { color: var(--ok); }
.dg-cell.bool-f { color: var(--dim); }
.dg-cell.json { color: var(--syn-string, var(--tx)); }
.dg.mod .dg-cell.nav:hover { color: var(--ac); text-decoration: underline; cursor: pointer; }
.dg-cell.sel { background-image: linear-gradient(var(--selbg), var(--selbg)); }
.dg-cell.act { box-shadow: inset 0 0 0 1.5px var(--selbd) !important; background-color: var(--cellbg); }
.dg-cell.chg { background-color: var(--chg-upd-bg); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--chg-upd) 55%, transparent); }
.r-new .dg-cell { background-color: var(--chg-new-bg); }
.r-del .dg-cell { background-color: var(--chg-del-bg); text-decoration: line-through; color: var(--dim); }
.dg-cell mark, .dg-cell.mark { background: var(--mark); color: inherit; border-radius: 2px; }
.dg-cell mark { padding: 0; }
.dg-cell.mark-cur { outline: 1px solid var(--warn); outline-offset: -1px; }
.dg-ell { color: var(--ac); font-weight: 700; margin-left: 1px; }
.dg-edit {
  position: absolute; z-index: 5; box-sizing: border-box; background: var(--bg); border: 1px solid var(--ac); outline: none;
  padding: 0 7px; font: 12px var(--mono); color: var(--tx); border-radius: 0;
}
.dg-edit-badge { position: absolute; z-index: 6; height: 22px; line-height: 22px; padding: 0 8px; border-radius: 4px; font: 600 11px var(--sans); background: var(--ac); color: var(--ac-fg, #fff); white-space: nowrap; pointer-events: none; }
.dg-edit-msg { position: absolute; z-index: 6; padding: 2px 8px; border-radius: 4px; font: 11px var(--sans); background: var(--bg2); border: 1px solid var(--bd); color: var(--warn); box-shadow: 0 4px 12px var(--shadow); pointer-events: none; white-space: nowrap; }
.dg-edit-msg.err { color: #fff; background: var(--danger); border-color: var(--danger); }
.dg-empty { position: absolute; left: 0; right: 0; text-align: center; font-family: var(--sans); }
.dg-loading { position: absolute; top: 0; left: 0; right: 0; height: 2px; overflow: hidden; z-index: 20; pointer-events: none; }
.dg-bar { display: block; height: 100%; width: 30%; background: var(--ac); animation: dgslide 1s ease-in-out infinite; }
@keyframes dgslide { 0% { margin-left: -30%; } 100% { margin-left: 100%; } }
</style>
