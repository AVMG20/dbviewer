<script setup lang="ts">
// Sidebar (shell agent): project switcher, lazy virtualized tree
// (Connection → schema → Tables/Views → columns), sticky ancestor rows, speed search over
// table and column names, context menus, resize handle.
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from "vue";
import { kbd } from "@/lib/kbd";
import { useVirtualizer } from "@tanstack/vue-virtual";
import { apiError, type ConnectionConfig, type Driver } from "@/api";
import { useProjectsStore } from "@/stores/projects";
import { useTabsStore } from "@/stores/tabs";
import { useUiStore } from "@/stores/ui";
import { DRIVER_BADGE } from "@/types";
import { isMod } from "@/components/common/keys";
import { shortError } from "@/components/common/errors";
import Icon from "@/components/common/Icon.vue";
import Modal from "@/components/common/Modal.vue";
import ContextMenu, { type MenuItem } from "@/components/common/ContextMenu.vue";
import { normQuery } from "@/components/common/fuzzy";
import {
  cache, indexedTable, invalidate, isViewKind, k, kt, loadColumns, loadIndex, loadSchemas, loadTables, qualifiedName, quoteIdent,
} from "./schemaCache";
import { columnMatch, searchTables, type ColHit, type Seg, type TableHit } from "./treeSearch";

const projects = useProjectsStore();
const tabs = useTabsStore();
const ui = useUiStore();

interface Row {
  key: string;
  kind: "conn" | "schema" | "group" | "table" | "col" | "more" | "msg";
  level: number;
  connId: string;
  schema?: string;
  name?: string;
  tkind?: string;
  group?: "t" | "v" | "o";
  /** msg row that connects the connection when activated */
  connect?: boolean;
  count?: number;
  segs?: Seg[];
  hint?: Seg[];
  text?: string;
  error?: boolean;
  estRows?: number;
  /** col / more rows: the owning table (name holds the column name) */
  table?: string;
  /** col rows: display type, pk flag, FK target ("table.column") */
  ctype?: string;
  pk?: boolean;
  fk?: string;
  /** table row in speed search that matched through its columns only: those are listed under it */
  byCol?: boolean;
}

// ---- expansion state ---------------------------------------------------------
const expanded = reactive(new Set<string>());
const collapsedGroups = reactive(new Set<string>());
/** expanded tables (tkey → [connId, schema, table]); the tuple lets loads / refreshes find them without parsing keys */
const openTables = reactive(new Map<string, readonly [string, string, string]>());
const ckey = (id: string) => `c:${id}`;
const skey = (id: string, s: string) => `s:${id}:${s}`;
const gkey = (id: string, s: string, g: "t" | "v") => `g:${id}:${s}:${g}`;
const tkey = (id: string, s: string, n: string) => `t:${id}:${s}:${n}`;
const colkey = (id: string, s: string, n: string, c: string) => `col:${id}:${s}:${n}:${c}`;

function visibleSchemas(connId: string) {
  const all = cache.schemas[connId] ?? [];
  const vis = all.filter((s) => !s.isSystem);
  return (vis.length ? vis : all).slice().sort((a, b) => Number(b.isDefault) - Number(a.isDefault) || a.name.localeCompare(b.name));
}

async function expandConn(id: string) {
  expanded.add(ckey(id));
  if (!(await projects.ensureConnected(id))) {
    // keep it open with the error on a row (readable cause, full text in the tooltip / "Show error")
    const err = projects.statuses[id]?.error;
    if (err) cache.errors[id] = err;
    else expanded.delete(ckey(id));
    return;
  }
  try {
    await loadSchemas(id);
  } catch {
    return; // error row is shown
  }
  const vis = visibleSchemas(id);
  const def = vis.find((s) => s.isDefault) ?? vis[0];
  for (const s of vis) {
    if (vis.length === 1 || s === def) {
      expanded.add(skey(id, s.name));
      loadTables(id, s.name).catch(() => {});
    }
  }
}
function toggleConn(id: string) {
  if (expanded.has(ckey(id))) expanded.delete(ckey(id));
  else void expandConn(id);
}
function toggleSchema(id: string, s: string) {
  const key = skey(id, s);
  if (expanded.has(key)) expanded.delete(key);
  else {
    expanded.add(key);
    loadTables(id, s).catch(() => {});
  }
}
function toggleGroup(id: string, s: string, g: "t" | "v") {
  const key = gkey(id, s, g);
  if (collapsedGroups.has(key)) collapsedGroups.delete(key);
  else collapsedGroups.add(key);
}

function toggleTable(id: string, s: string, n: string) {
  const key = tkey(id, s, n);
  if (openTables.has(key)) openTables.delete(key);
  else openTables.set(key, [id, s, n]);
}
// Expanded tables whose columns are not cached yet (first expand, refresh, reconnect) load them
// here; only once their schema's table list is there, i.e. the connection is up.
watch(
  () => [...openTables.values()].filter(([id, s, n]) => cache.tables[k(id, s)] && !cache.columns[kt(id, s, n)] && !cache.errors[`c:${kt(id, s, n)}`]),
  (need) => need.forEach(([id, s, n]) => loadColumns(id, s, n).catch(() => {})),
);
/** Drops cached column details (and their errors) of a schema so the watcher above reloads the open ones. */
function dropColumns(id: string, s: string) {
  const pre = k(id, s) + "\u0000";
  for (const key of Object.keys(cache.columns)) if (key.startsWith(pre)) delete cache.columns[key];
  for (const key of Object.keys(cache.errors)) if (key.startsWith("c:" + pre)) delete cache.errors[key];
}

function collapseAll() {
  clearSpeed();
  const sel = selRow();
  expanded.clear();
  openTables.clear();
  openOthers.clear();
  // keep the selection on the connection the selected row lived in
  if (sel) selected.value = ckey(sel.connId);
  nextTick(() => {
    if (scroller.value) scroller.value.scrollTop = 0;
    if (selected.value) scrollToKey(selected.value);
  });
}

async function refreshConn(id: string) {
  const was = expanded.has(ckey(id));
  const openSchemas = [...expanded].filter((x) => x.startsWith(`s:${id}:`)).map((x) => x.slice(`s:${id}:`.length));
  invalidate(id);
  if (!was) return;
  if (!(await projects.ensureConnected(id))) return;
  try {
    await loadSchemas(id, true);
    await Promise.all(openSchemas.map((s) => loadTables(id, s, true).catch(() => {})));
  } catch (e) {
    ui.toast(apiError(e).message, "error");
  }
}
async function refreshSchema(id: string, s: string) {
  dropColumns(id, s);
  try {
    await Promise.all([loadTables(id, s, true), loadIndex(id, s, true).catch(() => {})]);
  } catch (e) {
    ui.toast(apiError(e).message, "error");
  }
}

// ---- speed search --------------------------------------------------------------
// A filter over what the tree shows (PhpStorm style): table / view names and column names of
// expanded schemas match, nothing gets connected or expanded by it. Tables matching only
// through columns list those columns (best COL_CAP, then "N more") so Enter can open the exact
// column. It stays on while tables are opened from it; Esc clears it. Cmd+K searches everything.
const speed = ref("");
const searching = computed(() => speed.value.length > 0);
/** matched-column rows shown per table before a "N more" row */
const COL_CAP = 3;
/** tables whose "N more" row was activated (reset with every query change) */
const showAllCols = reactive(new Set<string>());
watch(speed, () => showAllCols.clear());

// Column search needs the schema index (one bulk query per schema, cached by the backend):
// fetch it for every expanded schema as soon as a search starts.
watch(
  () => searching.value && [...expanded].filter((x) => x.startsWith("s:")).join("\n"),
  (on) => {
    if (!on) return;
    for (const c of projects.projectConnections) {
      if (!expanded.has(ckey(c.id)) || !cache.schemas[c.id]) continue;
      for (const sc of cache.schemas[c.id]) if (expanded.has(skey(c.id, sc.name))) loadIndex(c.id, sc.name).catch(() => {});
    }
  },
);

/** conn ids whose "Other databases / schemas" group is open */
const openOthers = reactive(new Set<string>());

function schemaSplit(connId: string) {
  const vis = visibleSchemas(connId);
  const st = projects.statuses[connId];
  const primary = vis.filter((s) => s.isDefault || (!!st?.database && s.name === st.database));
  const main = vis.length > 1 ? (primary.length ? primary : vis.slice(0, 1)) : vis;
  const others = vis.length > 1 ? vis.filter((s) => !main.includes(s)) : [];
  return { vis, main, others };
}

// ---- rows ----------------------------------------------------------------------
const fkText = (fk?: { table: string; column: string } | null) => (fk ? `${fk.table}.${fk.column}` : undefined);

/** Column rows of an expanded table: full details (pk / fk) when loaded, else names + types from the index. */
function pushColumns(out: Row[], id: string, s: string, n: string, level: number, nq: string) {
  const tk = tkey(id, s, n);
  const base = { kind: "col" as const, level, connId: id, schema: s, table: n };
  const hl = (name: string) => (nq ? columnMatch(nq, name, name.toLowerCase())?.segs : undefined);
  const det = cache.columns[kt(id, s, n)];
  if (det) {
    for (const c of det) {
      out.push({ ...base, key: colkey(id, s, n, c.name), name: c.name, ctype: c.type, pk: c.pk, fk: fkText(c.fk), segs: hl(c.name) });
    }
    if (!det.length) out.push({ key: `n:${tk}`, kind: "msg", level, connId: id, text: "No columns" });
    return;
  }
  const it = indexedTable(id, s, n);
  if (it) {
    it.columns.forEach((c, i) => out.push({ ...base, key: colkey(id, s, n, c), name: c, ctype: it.types[i] || undefined, segs: hl(c) }));
    return;
  }
  const err = cache.errors[`c:${kt(id, s, n)}`];
  out.push(
    err
      ? { key: `e:${tk}`, kind: "msg", level, connId: id, schema: s, table: n, text: err, error: true }
      : { key: `l:${tk}`, kind: "msg", level, connId: id, text: "Loading…" },
  );
}
/** Speed search: the matched columns of a table that matched through them (capped, then a "N more" row). */
function pushMatchedColumns(out: Row[], id: string, s: string, n: string, level: number, cols: ColHit[]) {
  const tk = tkey(id, s, n);
  const it = indexedTable(id, s, n);
  const det = cache.columns[kt(id, s, n)];
  // no "1 more" row: showing that one column takes the same space
  const shown = showAllCols.has(tk) || cols.length <= COL_CAP + 1 ? cols : cols.slice(0, COL_CAP);
  for (const c of shown) {
    const d = det?.find((x) => x.name === c.name);
    out.push({
      key: colkey(id, s, n, c.name), kind: "col", level, connId: id, schema: s, table: n, name: c.name,
      ctype: d?.type ?? (it?.types[c.i] || undefined), pk: d?.pk, fk: fkText(d?.fk), segs: c.segs,
    });
  }
  if (shown.length < cols.length) {
    // name = the first hidden column: the selection moves there when the row is activated
    out.push({ key: `more:${tk}`, kind: "more", level, connId: id, schema: s, table: n, name: cols[shown.length].name, count: cols.length - shown.length });
  }
}

const built = computed(() => {
  const out: Row[] = [];
  let mTables = 0;
  let mCols = 0;
  const q = searching.value;
  const nq = q ? normQuery(speed.value) : "";
  for (const c of projects.projectConnections) {
    out.push({ key: ckey(c.id), kind: "conn", level: 0, connId: c.id, name: c.name });
    if (!expanded.has(ckey(c.id))) continue;
    if (!cache.schemas[c.id]) {
      out.push(
        cache.errors[c.id]
          ? { key: `e:${c.id}`, kind: "msg", level: 1, connId: c.id, text: cache.errors[c.id], error: true }
          : { key: `l:${c.id}`, kind: "msg", level: 1, connId: c.id, text: "Loading…" },
      );
      continue;
    }
    const { vis, main, others } = schemaSplit(c.id);
    const showSchemas = vis.length > 1;

    const pushSchema = (s: (typeof vis)[number], lvl: number) => {
      const sk = skey(c.id, s.name);
      const base = showSchemas ? lvl + 1 : lvl;
      if (showSchemas && (!q || expanded.has(sk))) out.push({ key: sk, kind: "schema", level: lvl, connId: c.id, schema: s.name, name: s.name });
      if (!expanded.has(sk)) return;
      const list = cache.tables[k(c.id, s.name)];
      if (!list) {
        out.push(
          cache.errors[k(c.id, s.name)]
            ? { key: `e:${sk}`, kind: "msg", level: base, connId: c.id, text: cache.errors[k(c.id, s.name)], error: true }
            : { key: `l:${sk}`, kind: "msg", level: base, connId: c.id, text: "Loading…" },
        );
        return;
      }
      const hits: Pick<TableHit, "table" | "segs" | "cols">[] = q
        ? searchTables(nq, list, (n) => indexedTable(c.id, s.name, n))
        : list.map((t) => ({ table: t, cols: [] }));
      if (q) {
        if (!hits.length) out.push({ key: `n:${sk}`, kind: "msg", level: base, connId: c.id, text: "No matches" });
      }
      const tbl = hits.filter((h) => !isViewKind(h.table.kind));
      const vws = hits.filter((h) => isViewKind(h.table.kind));
      for (const [g, items, label] of [["t", tbl, "Tables"], ["v", vws, "Views"]] as const) {
        if (!items.length && (g === "v" || q)) continue;
        const gk = gkey(c.id, s.name, g);
        out.push({ key: gk, kind: "group", level: base, connId: c.id, schema: s.name, group: g, name: label, count: items.length });
        if (collapsedGroups.has(gk)) continue;
        for (const { table: t, segs, cols } of items) {
          const tk = tkey(c.id, s.name, t.name);
          out.push({
            key: tk, kind: "table", level: base + 1, connId: c.id, schema: s.name,
            name: t.name, tkind: t.kind, estRows: t.estRows, segs, byCol: cols.length > 0,
          });
          if (openTables.has(tk)) pushColumns(out, c.id, s.name, t.name, base + 2, nq);
          else if (cols.length) pushMatchedColumns(out, c.id, s.name, t.name, base + 2, cols);
          if (q) {
            mTables++;
            mCols += cols.length;
          }
        }
      }
    };

    for (const s of main) pushSchema(s, 1);
    if (others.length) {
      const open = openOthers.has(c.id);
      if (!q || (open && others.some((s) => expanded.has(skey(c.id, s.name))))) {
        out.push({
          key: `og:${c.id}`, kind: "group", level: 1, connId: c.id, group: "o", count: others.length,
          name: c.driver === "postgres" ? "Other schemas" : "Other databases",
        });
      }
      if (open) for (const s of others) pushSchema(s, 2);
    }
  }
  return { rows: out, mTables, mCols };
});
const rows = computed<Row[]>(() => built.value.rows);
const plural = (n: number, w: string) => `${n.toLocaleString("en-US")} ${w}${n === 1 ? "" : "s"}`;
/** footer text: "9 tables · 42 columns" */
const matchText = computed(() => {
  const { mTables, mCols } = built.value;
  if (!mTables) return "No matches";
  return mCols ? `${plural(mTables, "table")} · ${plural(mCols, "column")}` : plural(mTables, "table");
});

// ---- selection / virtualization --------------------------------------------------
const selected = ref<string | null>(null);
const scroller = ref<HTMLElement | null>(null);
const focused = ref(false);
const ROW_H = 22;
const virt = useVirtualizer<HTMLElement, Element>(
  computed(() => ({
    count: rows.value.length,
    getScrollElement: () => scroller.value,
    estimateSize: () => ROW_H,
    overscan: 14,
    getItemKey: (i: number) => rows.value[i]?.key ?? i,
  })),
);
const vItems = computed(() => virt.value.getVirtualItems());
/** what the tree renders: sticky ancestor rows (in flow, CSS sticky) + the virtual window (absolute) */
const drawItems = computed(() => {
  const R = rows.value;
  const out = sticky.value.map((s, p) => ({ key: `st:${s.row.key}`, row: s.row, y: s.top, stuck: true, z: 20 - p }));
  for (const vi of vItems.value) {
    const row = R[vi.index];
    if (row) out.push({ key: String(vi.key), row, y: vi.start, stuck: false, z: 0 });
  }
  return out;
});
const totalSize = computed(() => virt.value.getTotalSize());

const PAD_TOP = 4; // tree padding-top (pt-1)

// ---- sticky ancestors ----------------------------------------------------------------
// The open connection / schema / group / table rows above the viewport stay pinned at the
// top (VS Code sticky scroll), pushed up by the next sibling as their subtree scrolls out.
// Row levels are contiguous along a branch, so a row's ancestor at level d is the nearest
// row above it with that level, and a row has `level` ancestors.
const STICKY_MAX = 4;
const scrollTop = ref(0);
const viewH = ref(0);
/** visible sticky rows; only replaced when the stack or an offset changes (not on every scroll pixel) */
const sticky = shallowRef<{ row: Row; top: number }[]>([]);
/** how many ancestor rows can stick for the current viewport (a third of it at most) */
const stickyCap = computed(() => Math.max(1, Math.min(STICKY_MAX, Math.floor(viewH.value / ROW_H / 3))));

function ancestorAt(i: number, lvl: number): number {
  const R = rows.value;
  if (R[i].level <= lvl) return -1;
  for (let j = i - 1; j >= 0; j--) if (R[j].level === lvl) return j;
  return -1;
}
function subtreeEnd(i: number): number {
  const R = rows.value;
  let j = i + 1;
  while (j < R.length && R[j].level > R[i].level) j++;
  return j - 1;
}
watch([scrollTop, rows, stickyCap], () => {
  const R = rows.value;
  const st = scrollTop.value;
  const out: { row: Row; top: number }[] = [];
  let parent = -1;
  for (let d = 0; d < stickyCap.value && R.length; d++) {
    // the row right under the current stack decides which ancestor sticks next
    const i = Math.min(R.length - 1, Math.floor((st + d * ROW_H - PAD_TOP) / ROW_H));
    if (i < 0) break;
    const a = ancestorAt(i, d);
    if (a < 0 || (d > 0 && ancestorAt(a, d - 1) !== parent)) break;
    if (!isOpen(R[a])) break;
    const endY = PAD_TOP + (subtreeEnd(a) + 1) * ROW_H - st;
    out.push({ row: R[a], top: Math.min(d * ROW_H, endY - ROW_H) });
    parent = a;
  }
  const sig = (l: { row: Row; top: number }[]) => l.map((x) => `${x.row.key}@${x.top}`).join("|");
  if (sig(out) !== sig(sticky.value)) sticky.value = out;
});
function onScroll() {
  scrollTop.value = scroller.value?.scrollTop ?? 0;
}

/** Height covered by sticky ancestors when row i sits right below them. */
function stickyAbove(i: number): number {
  return Math.min(rows.value[i]?.level ?? 0, stickyCap.value) * ROW_H;
}

/** Scrolls a row into view with ~3 rows of margin; centers it when it is off screen. */
function scrollToKeyMargin(key: string) {
  const i = rows.value.findIndex((r) => r.key === key);
  const el = scroller.value;
  if (i < 0 || !el) return;
  const top = i * ROW_H + PAD_TOP;
  const view = el.clientHeight;
  const head = stickyAbove(i);
  const margin = Math.min(3 * ROW_H, Math.max(0, (view - ROW_H) / 2));
  if (top < el.scrollTop + head || top + ROW_H > el.scrollTop + view) {
    el.scrollTop = Math.max(0, top - (view - ROW_H) / 2);
  } else if (top < el.scrollTop + head + margin) {
    el.scrollTop = Math.max(0, top - head - margin);
  } else if (top + ROW_H > el.scrollTop + view - margin) {
    el.scrollTop = top + ROW_H - view + margin;
  }
}

/** Minimal scroll that shows a row, keeping it clear of the sticky rows above it. */
function scrollToKey(key: string) {
  const i = rows.value.findIndex((r) => r.key === key);
  const el = scroller.value;
  if (i < 0 || !el) return;
  const top = i * ROW_H + PAD_TOP;
  const head = stickyAbove(i);
  if (top - head < el.scrollTop) el.scrollTop = Math.max(0, top - head);
  else {
    const foot = searching.value ? 36 : 0; // speed search overlay over the bottom
    if (top + ROW_H > el.scrollTop + el.clientHeight - foot) el.scrollTop = top + ROW_H - el.clientHeight + foot;
  }
}

const navRows = computed(() =>
  searching.value ? rows.value.filter((r) => r.kind === "table" || r.kind === "col" || r.kind === "more" || r.connect) : rows.value,
);
/** Best hit: the first table, or its first column when it matched through columns (Enter opens that column). */
function firstHit(): string | null {
  const list = navRows.value;
  const i = list.findIndex((r) => r.kind === "table");
  if (i < 0) return list[0]?.key ?? null;
  if (list[i].byCol) for (let j = i + 1; list[j]?.kind === "col"; j++) if (list[j].segs) return list[j].key;
  return list[i].key;
}
watch(rows, () => {
  if (searching.value) {
    if (!selected.value || !navRows.value.some((r) => r.key === selected.value)) selected.value = firstHit();
    nextTick(() => selected.value && scrollToKey(selected.value));
  }
});
watch(speed, () => {
  if (searching.value) {
    selected.value = firstHit();
    scroller.value && (scroller.value.scrollTop = 0);
  }
});

function moveSel(delta: number) {
  const list = navRows.value;
  if (!list.length) return;
  let i = list.findIndex((r) => r.key === selected.value);
  i = i < 0 ? (delta > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, i + delta));
  selected.value = list[i].key;
  scrollToKey(list[i].key);
}
function selRow(): Row | undefined {
  return rows.value.find((r) => r.key === selected.value);
}

// ---- actions ---------------------------------------------------------------------
/** focus: move keyboard focus into the opened view (Enter / double-click); a single click keeps it in the tree. */
function openTable(r: Row, structure = false, preview = true, focus = false) {
  if (r.kind !== "table" || !r.name) return;
  if (structure) tabs.openStructure(r.connId, r.schema ?? "", r.name, { focus });
  else tabs.openTable(r.connId, r.schema ?? "", r.name, { preview, focus });
}
/** Column row: opens its table like a table row would, then asks the grid to jump to the column. */
function openColumn(r: Row, structure = false, preview = true, focus = false) {
  if (r.kind !== "col" || !r.table || !r.name) return;
  if (structure) tabs.openStructure(r.connId, r.schema ?? "", r.table, { focus });
  else tabs.openTable(r.connId, r.schema ?? "", r.table, { preview, focus });
  ui.focusColumn(r.connId, r.schema ?? "", r.table, r.name);
}
function activate(r: Row, alt = false, viaKey = false) {
  switch (r.kind) {
    case "conn":
      toggleConn(r.connId);
      break;
    case "schema":
      toggleSchema(r.connId, r.schema!);
      break;
    case "group":
      if (r.group === "o") {
        if (openOthers.has(r.connId)) openOthers.delete(r.connId);
        else openOthers.add(r.connId);
      } else toggleGroup(r.connId, r.schema!, r.group as "t" | "v");
      break;
    case "table":
      openTable(r, alt, true, viaKey);
      break;
    case "col":
      // a click only selects a column; Enter / double-click opens it
      if (viaKey) openColumn(r, alt, true, true);
      break;
    case "more":
      showAllCols.add(tkey(r.connId, r.schema!, r.table!));
      selected.value = colkey(r.connId, r.schema!, r.table!, r.name!);
      break;
    case "msg":
      if (r.connect) void expandConn(r.connId);
      else if (r.error && r.table) delete cache.errors[`c:${kt(r.connId, r.schema!, r.table)}`]; // column load retry
      else if (r.error) void refreshConn(r.connId);
      break;
  }
}
function clearSpeed(keepSel = false) {
  const sel = selected.value;
  speed.value = "";
  if (keepSel && sel) nextTick(() => scrollToKey(sel));
}

function onRowClick(r: Row) {
  selected.value = r.key;
  scroller.value?.focus({ preventScroll: true });
  activate(r);
  // a click must leave the tree focused so typing still starts speed search
  nextTick(() => scroller.value?.focus({ preventScroll: true }));
}
function onRowDblClick(r: Row) {
  if (r.kind === "table") openTable(r, false, false, true);
  else if (r.kind === "col") openColumn(r, false, false, true);
}
/** Chevron: expand / collapse only (a table row click would open the table). */
function onChevClick(r: Row) {
  selected.value = r.key;
  toggle(r);
  nextTick(() => scroller.value?.focus({ preventScroll: true }));
}
/** A sticky row: same as a click, but collapsing it must not leave the view scrolled deep into nothing. */
function onStickyClick(r: Row, chev = false) {
  const wasOpen = isOpen(r);
  if (chev) onChevClick(r);
  else onRowClick(r);
  if (wasOpen && !isOpen(r)) nextTick(() => scrollToKeyMargin(r.key));
}

function toggle(r: Row) {
  if (r.kind === "table") toggleTable(r.connId, r.schema!, r.name!);
  else if (canExpand(r)) activate(r);
}

function parentIndex(i: number): number {
  const lvl = rows.value[i].level;
  for (let j = i - 1; j >= 0; j--) if (rows.value[j].level < lvl) return j;
  return -1;
}
function isOpen(r: Row): boolean {
  if (r.kind === "conn") return expanded.has(ckey(r.connId));
  if (r.kind === "schema") return expanded.has(skey(r.connId, r.schema!));
  if (r.kind === "group") return r.group === "o" ? openOthers.has(r.connId) : !collapsedGroups.has(gkey(r.connId, r.schema!, r.group as "t" | "v"));
  // speed search lists the matched columns of a column-only hit without expanding the table
  if (r.kind === "table") return openTables.has(r.key) || (searching.value && !!r.byCol);
  return false;
}
function canExpand(r: Row) {
  return r.kind === "conn" || r.kind === "schema" || r.kind === "group" || r.kind === "table";
}
/** open and closable by ArrowLeft (a column-only search hit shows its matches without being expanded) */
function canCollapse(r: Row) {
  return canExpand(r) && isOpen(r) && (r.kind !== "table" || openTables.has(r.key));
}

function onKeydown(e: KeyboardEvent) {
  const mod = isMod(e);
  if (e.metaKey || e.ctrlKey || e.altKey) {
    if (mod && e.key === "Enter") {
      const r = selRow();
      if (r) {
        e.preventDefault();
        activate(r, true, true);
      }
    } else if ((e.key === "Backspace" && mod) || (e.ctrlKey && e.key === "u")) {
      if (searching.value) {
        e.preventDefault();
        speed.value = "";
      }
    } else if (e.altKey && e.key === "Backspace" && searching.value) {
      e.preventDefault();
      speed.value = speed.value.replace(/\S*\s*$/, "");
    }
    return;
  }
  const r = selRow();
  switch (e.key) {
    case "ArrowDown": e.preventDefault(); moveSel(1); return;
    case "ArrowUp": e.preventDefault(); moveSel(-1); return;
    case "PageDown": e.preventDefault(); moveSel(10); return;
    case "PageUp": e.preventDefault(); moveSel(-10); return;
    case "Home": e.preventDefault(); moveSel(-1e6); return;
    case "End": e.preventDefault(); moveSel(1e6); return;
    case "Enter":
      if (r) {
        e.preventDefault();
        activate(r, e.shiftKey, true);
      }
      return;
    case "Escape":
      if (searching.value) {
        e.preventDefault();
        e.stopPropagation();
        clearSpeed(true);
      } else if (tabs.activeId) {
        e.preventDefault();
        ui.focusActiveView(); // Esc in the tree returns to the active tab
      }
      return;
    case "Backspace":
      if (searching.value) {
        e.preventDefault();
        speed.value = speed.value.slice(0, -1);
      }
      return;
    case "ArrowRight":
      // while searching only tables expand (the other tree levels are hidden / not navigable)
      if (r && canExpand(r) && (!searching.value || r.kind === "table")) {
        e.preventDefault();
        // a column-only search hit shows just its matches: → widens it to all columns first
        if (!isOpen(r) || (r.kind === "table" && !openTables.has(r.key))) toggle(r);
        else moveSel(1);
      }
      return;
    case "ArrowLeft":
      if (r && (!searching.value || r.kind === "table" || r.kind === "col" || r.kind === "more")) {
        e.preventDefault();
        if (canCollapse(r)) toggle(r);
        else if (!searching.value || r.kind !== "table") {
          const i = rows.value.findIndex((x) => x.key === r.key);
          const p = i >= 0 ? parentIndex(i) : -1;
          if (p >= 0) {
            selected.value = rows.value[p].key;
            scrollToKey(rows.value[p].key);
          }
        }
      }
      return;
    case "ContextMenu":
    case "F10":
      if (r && (e.key === "ContextMenu" || e.shiftKey)) {
        e.preventDefault();
        const el = scroller.value?.getBoundingClientRect();
        openMenu(r, (el?.left ?? 0) + 40, (el?.top ?? 0) + 40);
      }
      return;
  }
  if (e.key.length === 1) {
    if (e.key === " " && !searching.value) {
      e.preventDefault();
      if (r && canExpand(r)) toggle(r);
      return;
    }
    e.preventDefault();
    speed.value += e.key;
  }
}

function onPaste(e: ClipboardEvent) {
  const t = e.clipboardData?.getData("text")?.trim();
  if (t && !t.includes("\n")) {
    e.preventDefault();
    speed.value += t;
  }
}

// ---- reveal / focus ---------------------------------------------------------------
async function reveal(connId: string, schema?: string, table?: string, focus = true, column?: string) {
  if (focus) clearSpeed();
  expanded.add(ckey(connId));
  if (!cache.schemas[connId]) await expandConn(connId);
  if (!cache.schemas[connId]) return;
  const vis = visibleSchemas(connId);
  const s = schema && vis.some((x) => x.name === schema) ? schema : (vis.find((x) => x.isDefault) ?? vis[0])?.name;
  if (s !== undefined) {
    if (schemaSplit(connId).others.some((o) => o.name === s)) openOthers.add(connId);
    expanded.add(skey(connId, s));
    if (table) {
      await loadTables(connId, s).catch(() => {});
      collapsedGroups.delete(gkey(connId, s, "t"));
      collapsedGroups.delete(gkey(connId, s, "v"));
      if (column) {
        openTables.set(tkey(connId, s, table), [connId, s, table]);
        // details first (pk / fk); the index still yields the column rows if that fails
        await loadColumns(connId, s, table).catch(() => loadIndex(connId, s).catch(() => {}));
      }
    }
  }
  await nextTick();
  const key =
    table && s !== undefined ? (column ? colkey(connId, s, table, column) : tkey(connId, s, table)) : ckey(connId);
  // Following the active tab must not undo a column reveal of the same table (the palette opens
  // the table and reveals the column at once; whichever finishes last would win).
  if (!column && table && s !== undefined && selected.value?.startsWith(colkey(connId, s, table, ""))) return;
  if (!rows.value.some((r) => r.key === key) && column) {
    // column unknown (renamed / stale palette index): fall back to the table row
    return reveal(connId, schema, table, focus);
  }
  selected.value = key;
  scrollToKeyMargin(key);
  if (focus) scroller.value?.focus({ preventScroll: true });
}
watch(
  () => ui.revealRequest,
  (r) => r && void reveal(r.connId, r.schema, r.table, true, r.column),
);
// Follow the active tab: expand its connection / schema / group, select + scroll, no focus steal.
watch(
  () => [tabs.activeId, projects.loaded] as const,
  () => {
    const t = tabs.active;
    if (!t || searching.value || !projects.getConnection(t.connId)) return;
    if (projects.getConnection(t.connId)?.projectId !== projects.activeProjectId) return;
    if (t.kind === "console") {
      if (!expanded.has(ckey(t.connId)) && !projects.isConnected(t.connId)) {
        selected.value = ckey(t.connId);
        scrollToKey(selected.value);
        return;
      }
      void reveal(t.connId, undefined, undefined, false);
    } else void reveal(t.connId, t.schema, t.table, false);
  },
  { immediate: true },
);
watch(
  () => ui.refreshRequest,
  (r) => r && void refreshConn(r.connId),
);
watch(
  () => ui.focusSidebarSeq,
  () => nextTick(() => scroller.value?.focus({ preventScroll: true })),
);

// Collapse + drop caches when a connection closes or its config changes.
watch(
  () => projects.statuses,
  (now, before) => {
    for (const id of Object.keys(now)) {
      const a = now[id];
      const b = before?.[id];
      if (b?.connected && !a.connected && !a.connecting) {
        invalidate(id);
        expanded.delete(ckey(id));
      }
    }
  },
);

onMounted(() => {
  // Auto-expand connections that are already open.
  for (const c of projects.projectConnections) if (projects.isConnected(c.id)) void expandConn(c.id);
});
watch(
  () => projects.activeProjectId,
  () => {
    speed.value = "";
    selected.value = null;
    for (const c of projects.projectConnections) if (projects.isConnected(c.id) && !expanded.has(ckey(c.id))) void expandConn(c.id);
  },
);

// ---- context menus ------------------------------------------------------------------
const menu = ref<{ x: number; y: number; items: MenuItem[]; run: (id: string) => void } | null>(null);
/** the menu emits close before select, so the handler must outlive `menu` */
let menuRun: ((id: string) => void) | null = null;
watch(menu, (m) => m && (menuRun = m.run));

function copy(text: string, what = "Copied") {
  const done = () => ui.toast(`${what}: ${text.length > 60 ? text.slice(0, 57) + "…" : text}`, "success", 1800);
  if (navigator.clipboard?.writeText) navigator.clipboard.writeText(text).then(done, () => fallbackCopy(text, done));
  else fallbackCopy(text, done);
}
function fallbackCopy(text: string, done: () => void) {
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  document.execCommand("copy");
  ta.remove();
  done();
}

async function deleteConn(id: string) {
  const c = projects.getConnection(id);
  if (!c) return;
  const ok = await ui.confirm({
    title: "Delete connection?",
    message: `"${c.name}" and its saved password, query history and open tabs will be deleted. This cannot be undone.`,
    confirmText: "Delete",
    danger: true,
  });
  if (!ok) return;
  try {
    tabs.closeConnection(id);
    await projects.deleteConnection(id);
    invalidate(id);
    expanded.delete(ckey(id));
  } catch (e) {
    ui.toast(apiError(e).message, "error");
  }
}

function openMenu(r: Row, x: number, y: number) {
  selected.value = r.key;
  const c = projects.getConnection(r.connId);
  if (!c) return;
  if (r.kind === "conn") {
    const connected = projects.isConnected(c.id);
    menu.value = {
      x, y,
      items: [
        connected ? { id: "disconnect", label: "Disconnect", icon: "unplug" } : { id: "connect", label: "Connect", icon: "plug" },
        { id: "console", label: "New SQL console", icon: "sql", hint: kbd("⌘T") },
        { id: "refresh", label: "Refresh", icon: "refresh" },
        ...(projects.statuses[c.id]?.error && !connected ? [{ id: "showerr", label: "Show error", icon: "info" }] : []),
        { id: "collapseAll", label: "Collapse all", icon: "collapse" },
        { separator: true },
        { id: "edit", label: "Edit…", icon: "edit" },
        { id: "duplicate", label: "Duplicate", icon: "copy" },
        { id: "delete", label: "Delete…", icon: "trash", danger: true },
      ],
      run: async (id) => {
        if (id === "connect") {
          expanded.delete(ckey(c.id));
          await expandConn(c.id);
        } else if (id === "disconnect") {
          try { await projects.disconnect(c.id); } catch (e) { ui.toast(apiError(e).message, "error"); }
          invalidate(c.id);
          expanded.delete(ckey(c.id));
        } else if (id === "console") tabs.openConsole(c.id);
        else if (id === "refresh") {
          if (!expanded.has(ckey(c.id))) await expandConn(c.id);
          else await refreshConn(c.id);
        } else if (id === "collapseAll") collapseAll();
        else if (id === "showerr") errDlg.value = { title: c.name, text: projects.statuses[c.id]?.error ?? "" };
        else if (id === "edit") ui.openModal("connection", { connId: c.id });
        else if (id === "duplicate") {
          try {
            const d = await projects.duplicateConnection(c.id);
            ui.toast(`Created "${d.name}"`, "success");
          } catch (e) { ui.toast(apiError(e).message, "error"); }
        } else if (id === "delete") await deleteConn(c.id);
      },
    };
  } else if (r.kind === "schema" || (r.kind === "group" && r.schema !== undefined)) {
    const s = r.schema!;
    menu.value = {
      x, y,
      items: [
        { id: "console", label: "New SQL console", icon: "sql" },
        { id: "refresh", label: "Refresh", icon: "refresh" },
        { id: "collapseAll", label: "Collapse all", icon: "collapse" },
      ],
      run: (id) => {
        if (id === "console") tabs.openConsole(c.id, "", s);
        else if (id === "collapseAll") collapseAll();
        else void refreshSchema(c.id, s);
      },
    };
  } else if (r.kind === "table") {
    menu.value = {
      x, y,
      items: [
        { id: "data", label: "Open data", icon: "table", hint: "↵" },
        { id: "structure", label: "Open structure", icon: "structure", hint: kbd("⌘↵") },
        { id: "expand", label: openTables.has(r.key) ? "Hide columns" : "Show columns", icon: "columns", hint: openTables.has(r.key) ? "←" : "→" },
        { separator: true },
        { id: "copyName", label: "Copy name", icon: "copy" },
        { id: "copySelect", label: "Copy SELECT", icon: "copy" },
        { id: "console", label: "New SQL console with SELECT", icon: "sql" },
        { separator: true },
        { id: "refresh", label: "Refresh", icon: "refresh" },
        { id: "collapseAll", label: "Collapse all", icon: "collapse" },
      ],
      run: (id) => {
        const sel = `SELECT * FROM ${qualifiedName(c.driver, r.schema ?? "", r.name!)} LIMIT 100;`;
        if (id === "data") openTable(r, false, false, true);
        else if (id === "structure") openTable(r, true, false, true);
        else if (id === "expand") toggleTable(r.connId, r.schema!, r.name!);
        else if (id === "copyName") copy(r.name!, "Copied");
        else if (id === "copySelect") copy(sel, "Copied");
        else if (id === "console") tabs.openConsole(c.id, sel, r.schema ?? "");
        else if (id === "collapseAll") collapseAll();
        else void refreshSchema(c.id, r.schema ?? "");
      },
    };
  } else if (r.kind === "col") {
    const col = r.name!;
    const tbl = qualifiedName(c.driver, r.schema ?? "", r.table!);
    menu.value = {
      x, y,
      items: [
        { id: "data", label: "Open data at column", icon: "table", hint: "↵" },
        { id: "structure", label: "Open structure", icon: "structure", hint: kbd("⌘↵") },
        { separator: true },
        { id: "copyName", label: "Copy name", icon: "copy" },
        { id: "copySelect", label: "Copy SELECT", icon: "copy" },
        { separator: true },
        { id: "collapseAll", label: "Collapse all", icon: "collapse" },
      ],
      run: (id) => {
        if (id === "data") openColumn(r, false, false, true);
        else if (id === "structure") openColumn(r, true, false, true);
        else if (id === "copyName") copy(col, "Copied");
        else if (id === "copySelect") copy(`SELECT ${quoteIdent(c.driver, col)} FROM ${tbl} LIMIT 100;`, "Copied");
        else if (id === "collapseAll") collapseAll();
      },
    };
  }
}
const errDlg = ref<{ title: string; text: string } | null>(null);
async function copyErr() {
  try {
    await navigator.clipboard.writeText(errDlg.value?.text ?? "");
    ui.toast("Copied", "success", 1200);
  } catch {
    ui.toast("Select the text and copy it manually", "info");
  }
}
function onContext(e: MouseEvent, r: Row) {
  e.preventDefault();
  if (r.kind === "msg") {
    if (!r.error) return;
    selected.value = r.key;
    menu.value = {
      x: e.clientX, y: e.clientY,
      items: [{ id: "show", label: "Show error", icon: "info" }, { id: "retry", label: "Retry", icon: "refresh" }],
      run: (id) => {
        if (id === "show") errDlg.value = { title: connOf(r.connId)?.name ?? "Error", text: r.text ?? "" };
        else void refreshConn(r.connId);
      },
    };
    return;
  }
  openMenu(r, e.clientX, e.clientY);
}

// ---- project switcher ---------------------------------------------------------------
const projMenu = ref<{ x: number; y: number } | null>(null);
const projItems = computed<MenuItem[]>(() => {
  const list: MenuItem[] = projects.projects.map((p) => ({
    id: `p:${p.id}`,
    label: p.name,
    icon: p.id === projects.activeProjectId ? "check" : undefined,
  }));
  const idx = projects.projects.findIndex((p) => p.id === projects.activeProjectId);
  return [
    ...list,
    { separator: true },
    { id: "new", label: "New project…", icon: "plus" },
    { id: "rename", label: "Rename project…", icon: "edit" },
    { id: "up", label: "Move up", disabled: idx <= 0 },
    { id: "down", label: "Move down", disabled: idx < 0 || idx >= projects.projects.length - 1 },
    { id: "delete", label: "Delete project…", icon: "trash", danger: true, disabled: projects.projects.length <= 1 },
  ];
});
function openProjMenu(e: MouseEvent) {
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
  projMenu.value = { x: r.left, y: r.bottom + 2 };
}
const projDlg = ref<{ mode: "new" | "rename"; name: string; busy: boolean; error: string } | null>(null);
async function projAction(id: string) {
  const cur = projects.activeProject;
  if (id.startsWith("p:")) await tabs.requestProjectSwitch(id.slice(2));
  else if (id === "new") projDlg.value = { mode: "new", name: "", busy: false, error: "" };
  else if (id === "rename" && cur) projDlg.value = { mode: "rename", name: cur.name, busy: false, error: "" };
  else if ((id === "up" || id === "down") && cur) {
    const ids = projects.projects.map((p) => p.id);
    const i = ids.indexOf(cur.id);
    const j = id === "up" ? i - 1 : i + 1;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    try { await projects.reorderProjects(ids); } catch (e) { ui.toast(apiError(e).message, "error"); }
  } else if (id === "delete" && cur) {
    const n = projects.connections.filter((c) => c.projectId === cur.id).length;
    const ok = await ui.confirm({
      title: `Delete project "${cur.name}"?`,
      message: `${n} connection${n === 1 ? "" : "s"} (with saved passwords, history and saved queries) will be deleted. This cannot be undone.`,
      confirmText: "Delete project",
      danger: true,
    });
    if (!ok) return;
    try {
      for (const c of projects.connections.filter((c) => c.projectId === cur.id)) tabs.closeConnection(c.id);
      await projects.deleteProject(cur.id);
    } catch (e) {
      ui.toast(apiError(e).message, "error");
    }
  }
}
async function submitProj() {
  const d = projDlg.value;
  if (!d || d.busy) return;
  const name = d.name.trim();
  if (!name) {
    d.error = "Name is required";
    return;
  }
  d.busy = true;
  try {
    if (d.mode === "new") {
      const p = await projects.saveProject({ name });
      await tabs.requestProjectSwitch(p.id);
    } else if (projects.activeProject) await projects.renameProject(projects.activeProject.id, name);
    projDlg.value = null;
  } catch (e) {
    d.error = apiError(e).message;
    d.busy = false;
  }
}

function newConnection() {
  ui.openModal("connection", { projectId: projects.activeProjectId });
}

// ---- resize -------------------------------------------------------------------------
function startResize(e: MouseEvent) {
  e.preventDefault();
  const startX = e.clientX;
  dragging.value = true;
  const startW = ui.sidebarWidth;
  const move = (ev: MouseEvent) => (ui.sidebarWidth = Math.min(560, Math.max(180, startW + ev.clientX - startX)));
  const up = () => {
    window.removeEventListener("mousemove", move);
    window.removeEventListener("mouseup", up);
    document.body.style.cursor = "";
    dragging.value = false;
  };
  document.body.style.cursor = "col-resize";
  window.addEventListener("mousemove", move);
  window.addEventListener("mouseup", up);
}

// ---- row helpers ----------------------------------------------------------------------
/** Shortens the longest unmatched run (middle ellipsis) until the segments fit `max` chars; matched text is never cut. */
function fitSegs(segs: Seg[], max: number): Seg[] {
  const out = segs.map((s) => ({ ...s }));
  let total = out.reduce((n, s) => n + s.t.length, 0);
  while (total > max) {
    let bi = -1;
    for (let i = 0; i < out.length; i++) if (!out[i].m && out[i].t.length > 4 && (bi < 0 || out[i].t.length > out[bi].t.length)) bi = i;
    if (bi < 0) break;
    const t = out[bi].t;
    const keep = Math.max(2, t.length - (total - max) - 1);
    const head = Math.ceil(keep / 2);
    const tail = keep - head;
    const next = tail > 0 ? t.slice(0, head) + "…" + t.slice(-tail) : t.slice(0, head) + "…";
    if (next.length >= t.length) break;
    total -= t.length - next.length;
    out[bi].t = next;
  }
  return out;
}
/** Measured width of one monospace character in the tree's real font (canvas), cached per font. */
let charWCache: { font: string; w: number } | null = null;
/** The font lookup forces a style recalc; fitRow runs per row per render, so it is re-read only on a new tick. */
let charWTick: number | null = null;
function charWidth(): number {
  if (charWTick !== null && charWCache) return charWCache.w;
  charWTick = requestAnimationFrame(() => (charWTick = null));
  const el = scroller.value;
  const cs = el ? getComputedStyle(el) : null;
  const size = cs?.fontSize ?? "12.5px";
  const mono = getComputedStyle(document.documentElement).getPropertyValue("--mono").trim() || "monospace";
  const font = `${size} ${mono}`;
  if (charWCache?.font === font) return charWCache.w;
  const ctx = document.createElement("canvas").getContext("2d");
  let w = parseFloat(size) * 0.6;
  if (ctx) {
    ctx.font = font;
    w = ctx.measureText("MMMMMMMMMMMMMMMMMMMM").width / 20 || w;
  }
  charWCache = { font, w };
  return w;
}
/** Tree viewport width, kept current by a ResizeObserver (sidebar resize, search panel growth). */
const treeW = ref(0);
let treeRO: ResizeObserver | null = null;
onMounted(() => {
  if (scroller.value) {
    treeRO = new ResizeObserver(() => {
      treeW.value = scroller.value?.clientWidth ?? 0;
      viewH.value = scroller.value?.clientHeight ?? 0;
    });
    treeRO.observe(scroller.value);
    treeW.value = scroller.value.clientWidth;
    viewH.value = scroller.value.clientHeight;
  }
});
onBeforeUnmount(() => treeRO?.disconnect());

/**
 * Name + hint segments. Normal tree: the CSS end-ellipsis handles overflow. In speed search the matched
 * text must stay visible, so unmatched runs are middle-truncated to the real available width.
 */
function fitRow(r: Row): { name: Seg[]; hint?: Seg[] } {
  const name = r.segs ?? [{ t: r.name ?? "", m: false }];
  if (!searching.value) return { name, hint: r.hint };
  // row = left/right margin 12 + padding-left (8 + level*14) + padding-right 8 + chevron 11 + icon 13 + 2 gaps of 6 + scrollbar 10
  const px = Math.max(0, treeW.value - (12 + 8 + r.level * 14 + 8 + 11 + 13 + 12 + 10));
  const avail = Math.floor(px / charWidth());
  if (!r.hint) return { name: fitSegs(name, avail) };
  const hintLen = r.hint.reduce((n, s) => n + s.t.length, 0);
  const hint = fitSegs(r.hint, Math.max(14, Math.min(hintLen, avail - 12)));
  const hl = hint.reduce((n, s) => n + s.t.length, 0) + 3;
  return { name: fitSegs(name, Math.max(10, avail - hl)), hint };
}
const dragging = ref(false);
const effWidth = computed(() => ui.sidebarWidth);
function accentOf(id: string): string | undefined {
  const c = connOf(id)?.color;
  return c && c !== "none" ? c : undefined;
}
function badge(d?: string): string {
  return DRIVER_BADGE[d as Driver] ?? (d ?? "").slice(0, 2).toUpperCase();
}
function connOf(id: string): ConnectionConfig | undefined {
  return projects.getConnection(id);
}
function isActiveTable(r: Row) {
  const t = tabs.active;
  if (t && r.kind === "conn") return t.kind === "console" && t.connId === r.connId;
  return !!t && r.kind === "table" && t.connId === r.connId && (t.schema ?? "") === (r.schema ?? "") && t.table === r.name;
}
function rowTitle(r: Row): string | undefined {
  if (r.kind === "table") {
    const col = r.hint ? ` · ${r.hint.map((x) => x.t).join("")}` : "";
    const rowsTxt = r.estRows !== undefined && r.estRows >= 0 ? ` · ~${r.estRows.toLocaleString("en-US")} rows` : "";
    return `${r.name}${col}${rowsTxt}`;
  }
  if (r.kind === "col") {
    return [`${r.name}${r.ctype ? " " + r.ctype : ""}`, r.pk && "primary key", r.fk && `→ ${r.fk}`].filter(Boolean).join(" · ");
  }
  if (r.kind === "conn") return projects.statuses[r.connId]?.error || undefined;
  if (r.kind === "msg") return r.text;
  return undefined;
}
</script>

<template>
  <!-- The wrapper keeps the layout width; while speed searching the aside grows over the content (no reflow). -->
  <div v-show="ui.sidebarVisible" class="no-drag relative flex-none" :style="{ width: ui.sidebarWidth + 'px' }">
  <aside
    class="absolute inset-y-0 left-0 flex flex-col border-r border-bd bg-bg2"
    :style="{
      width: effWidth + 'px',
      zIndex: effWidth > ui.sidebarWidth ? 'var(--z-popover)' : undefined,
      boxShadow: effWidth > ui.sidebarWidth ? '8px 0 28px -10px var(--shadow)' : undefined,
      transition: dragging ? 'none' : 'width .12s ease',
    }"
  >
    <!-- project switcher -->
    <div class="flex flex-none items-center gap-1.5 border-b border-bd p-2">
      <button
        class="btn !h-6 min-w-0 flex-1 !justify-between font-semibold"
        :title="'Switch project'"
        @click="openProjMenu"
      >
        <span class="flex min-w-0 items-center gap-1.5">
          <Icon name="folder" class="dim" />
          <span class="truncate">{{ projects.activeProject?.name ?? "…" }}</span>
        </span>
        <span class="dim text-[9px]">▾</span>
      </button>
      <button
        class="btn !h-6 !px-1.5"
        title="Collapse all"
        :disabled="!expanded.size && !openTables.size"
        @click="collapseAll"
      ><Icon name="collapse" /></button>
      <button class="btn !h-6" title="New connection" @click="newConnection"><Icon name="plus" /> <span>Connection</span></button>
    </div>

    <!-- tree -->
    <div
      ref="scroller"
      tabindex="0"
      class="min-h-0 flex-1 overflow-auto pb-9 pt-1 outline-none"
      role="tree"
      aria-label="Connections"
      @keydown="onKeydown"
      @paste="onPaste"
      @focus="focused = true"
      @blur="focused = false"
      @scroll="onScroll"
    >
      <div v-if="projects.loaded && !projects.projectConnections.length" class="flex flex-col items-center gap-2 px-4 py-10 text-center">
        <div class="dim">No connections in this project yet.</div>
        <button class="btn primary" @click="newConnection"><Icon name="plus" /> New connection</button>
      </div>
      <div :style="{ height: totalSize + 'px', position: 'relative', width: '100%' }">
        <!-- Sticky ancestors come first and stay in flow (CSS sticky, no scroll lag); the virtual rows are absolute.
             Sticky offsets count from the scroller's padding edge, hence - PAD_TOP. -->
        <div
          v-for="it in drawItems"
          :key="it.key"
          class="rowwrap px-1.5"
          :class="it.stuck ? 'stk' : 'absolute left-0 right-0 top-0'"
          :style="it.stuck ? { top: it.y - PAD_TOP + 'px', zIndex: it.z, height: ROW_H + 'px' } : { transform: `translateY(${it.y}px)`, height: ROW_H + 'px' }"
        >
          <div
            class="tree-row flex h-full items-center gap-1.5 whitespace-nowrap pr-2"
            :class="{
              sel: selected === it.row.key,
              foc: focused,
              act: isActiveTable(it.row),
              msg: it.row.kind === 'msg',
            }"
            :style="{
              paddingLeft: 8 + it.row.level * 14 + 'px',
              '--conn': it.row.kind === 'conn' ? projects.connColor(it.row.connId) : undefined,
            }"
            :role="it.stuck ? undefined : 'treeitem'"
            :data-accent="accentOf(it.row.connId)"
            :aria-selected="it.stuck ? undefined : selected === it.row.key"
            :aria-hidden="it.stuck || undefined"
            :title="rowTitle(it.row)"
            @click="it.stuck ? onStickyClick(it.row) : onRowClick(it.row)"
            @dblclick="onRowDblClick(it.row)"
            @contextmenu="onContext($event, it.row)"
          >
            <!-- chevron -->
            <span
              v-if="canExpand(it.row)"
              class="chev"
              :class="{ open: isOpen(it.row) }"
              @click.stop="it.stuck ? onStickyClick(it.row, true) : onChevClick(it.row)"
              @dblclick.stop
            ><Icon name="chev" :size="11" /></span>
            <span v-else class="inline-block w-[11px] flex-none" />

            <template v-if="it.row.kind === 'conn'">
              <span class="conn-dot" />
              <span class="truncate font-medium">{{ it.row.name }}</span>
              <span class="badge">{{ badge(connOf(it.row.connId)?.driver) }}</span>
              <span v-if="projects.isReadOnly(it.row.connId)" class="text-danger" title="Read-only"><Icon name="lock" :size="11" /></span>
              <span class="ml-auto flex items-center">
                <span
                  v-if="projects.statuses[it.row.connId]?.connecting"
                  class="spinner"
                  title="Connecting…"
                />
                <span
                  v-else-if="projects.statuses[it.row.connId]?.error && !projects.statuses[it.row.connId]?.connected"
                  class="text-danger"
                  :title="projects.statuses[it.row.connId]?.error"
                ><Icon name="warn" :size="12" /></span>
                <span
                  v-else-if="projects.isConnected(it.row.connId)"
                  class="inline-block h-[6px] w-[6px] rounded-full bg-ok"
                  title="Connected"
                />
              </span>
            </template>

            <template v-else-if="it.row.kind === 'schema'">
              <Icon name="schema" class="dim" />
              <span class="truncate">{{ it.row.name }}</span>
            </template>

            <template v-else-if="it.row.kind === 'group'">
              <span class="dim text-[11px]">{{ it.row.name }}</span>
              <span class="cnt">{{ it.row.count }}</span>
            </template>

            <template v-else-if="it.row.kind === 'table'">
              <Icon :name="isViewKind(it.row.tkind ?? '') ? 'view' : 'table'" class="dim" />
              <span class="mono name" :class="{ dimname: it.row.byCol && searching }">
                <template v-for="(s, i) in fitRow(it.row).name" :key="i"><span :class="{ m: s.m }">{{ s.t }}</span></template>
              </span>
              <span v-if="it.row.hint" class="hint mono hintcol">
                · <template v-for="(s, i) in fitRow(it.row).hint" :key="i"><span :class="{ m: s.m }">{{ s.t }}</span></template>
              </span>
            </template>

            <template v-else-if="it.row.kind === 'col'">
              <span v-if="it.row.pk" class="cico text-pk"><Icon name="key" :size="11" /></span>
              <span v-else-if="it.row.fk" class="cico text-fk"><Icon name="link" :size="11" /></span>
              <span v-else class="cico"><span class="cdot" /></span>
              <span class="mono name">
                <template v-for="(s, i) in fitRow(it.row).name" :key="i"><span :class="{ m: s.m }">{{ s.t }}</span></template>
              </span>
              <span v-if="it.row.ctype" class="hint mono ctype">{{ it.row.ctype }}</span>
            </template>

            <template v-else-if="it.row.kind === 'more'">
              <span class="cico" />
              <span class="dim text-[11px]">{{ it.row.count }} more…</span>
            </template>

            <template v-else>
              <span :class="it.row.error ? 'text-danger' : 'dim'" class="truncate text-[11px]">{{ it.row.error ? shortError(it.row.text ?? "") : it.row.text }}</span>
              <span v-if="it.row.error" class="dim flex-none text-[10px]">· retry</span>
            </template>
          </div>
        </div>
      </div>
    </div>

    <!-- speed search overlay -->
    <div v-if="searching" class="speed">
      <Icon name="search" class="text-ac" />
      <span class="speed-text mono">{{ speed }}</span>
      <span class="dim whitespace-nowrap">{{ matchText }}</span>
      <kbd class="cursor-pointer" title="Clear filter" @click="clearSpeed(true)">esc</kbd>
    </div>

    <div class="resize" @mousedown="startResize" @dblclick="ui.sidebarWidth = 268" />

    <ContextMenu
      v-if="menu"
      :x="menu.x"
      :y="menu.y"
      :items="menu.items"
      @select="menuRun?.($event)"
      @close="menu = null; scroller?.focus({ preventScroll: true })"
    />
    <ContextMenu v-if="projMenu" :x="projMenu.x" :y="projMenu.y" :items="projItems" @select="projAction" @close="projMenu = null" />

    <Modal v-if="errDlg" :title="`Error: ${errDlg.title}`" :width="560" @close="errDlg = null">
      <pre class="selectable m-0 whitespace-pre-wrap break-all rounded border border-bd bg-bg p-2 font-mono text-[11.5px] text-danger">{{ errDlg.text }}</pre>
      <template #footer>
        <button class="btn" @click="copyErr">Copy</button>
        <button class="btn primary" data-autofocus @click="errDlg = null">Close</button>
      </template>
    </Modal>
    <Modal v-if="projDlg" :title="projDlg.mode === 'new' ? 'New project' : 'Rename project'" :width="360" @close="projDlg = null">
      <form class="flex flex-col gap-1.5" @submit.prevent="submitProj">
        <input v-model="projDlg.name" class="input !h-6" placeholder="Project name" data-autofocus @input="projDlg.error = ''" />
        <div v-if="projDlg.error" class="text-danger">{{ projDlg.error }}</div>
      </form>
      <template #footer>
        <button class="btn" @click="projDlg = null">Cancel</button>
        <button class="btn primary" :disabled="projDlg.busy" @click="submitProj">{{ projDlg.mode === "new" ? "Create" : "Rename" }}</button>
      </template>
    </Modal>
  </aside>
  </div>
</template>

<style scoped>
.tree-row { cursor: default; border-radius: 7px; }
/* sticky ancestor rows: opaque so the rows scrolling underneath don't show through */
.stk { position: sticky; background: var(--bg2); }
.stk:has(+ .rowwrap:not(.stk)) { box-shadow: 0 6px 6px -6px var(--shadow); }
.cico { display: inline-flex; flex: none; width: 13px; justify-content: center; align-items: center; }
.cdot { width: 4px; height: 4px; border-radius: 50%; background: var(--dim); opacity: .55; }
/* the type gives way first: the column name is what the row is about */
.ctype { flex: 0 1000 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dimname { color: var(--dim); }
.tree-row:hover { background: var(--hov); }
.tree-row.sel { background: var(--hov); }
.tree-row.sel.foc { background: var(--selbg); }
.tree-row.act { color: var(--tx); background: var(--selbg); font-weight: 500; }
.tree-row.act :deep(svg) { color: var(--ac); }
.tree-row.act .mono { color: var(--tx); }
.tree-row.msg:hover { background: none; }
.chev { display: inline-flex; flex: none; color: var(--dim); transition: transform .1s; }
.chev.open { transform: rotate(90deg); }
.cnt { margin-left: auto; color: var(--dim); font: 10px var(--mono); }
.hint { color: var(--dim); font-size: 11px; }
.name { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hintcol { flex: 0 0 auto; white-space: nowrap; }
.m { color: var(--ac); font-weight: 700; }
.speed {
  position: absolute; left: 8px; right: 8px; bottom: 8px; z-index: 5;
  display: flex; align-items: center; gap: 8px; padding: 4px 8px;
  background: var(--bg); border: 1px solid var(--ac); border-radius: 5px; box-shadow: 0 4px 16px var(--shadow);
}
.speed-text { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: pre; }
.speed-text::after {
  content: ""; display: inline-block; width: 1px; height: 12px; background: var(--tx);
  vertical-align: -2px; margin-left: 1px; animation: blink 1s steps(1) infinite;
}
@keyframes blink { 50% { opacity: 0; } }
.resize { position: absolute; top: 0; right: -3px; bottom: 0; width: 6px; cursor: col-resize; z-index: 6; }
.resize:hover { background: color-mix(in srgb, var(--ac) 40%, transparent); }
.spinner {
  width: 10px; height: 10px; border-radius: 50%; border: 1.5px solid var(--bd); border-top-color: var(--ac);
  animation: spin .7s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }
</style>
