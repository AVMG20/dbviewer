<script setup lang="ts">
// Sidebar (shell agent): clean ghost project switcher, flattened virtualized tree
// (Connection → schema → tables/views → columns), capped sticky ancestors, highlight-and-jump
// find bar (with ↑/↓ navigation, match counter, scrollbar ticks, and optional filter toggle),
// hover quick actions, context menus, and resize handle.
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from "vue";
import { kbd } from "@/lib/kbd";
import { settings } from "@/lib/settings";
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
import { matchSidebarName, type Seg } from "./treeSearch";

const projects = useProjectsStore();
const tabs = useTabsStore();
const ui = useUiStore();

interface Row {
  key: string;
  kind: "conn" | "schema" | "group" | "section" | "table" | "col" | "msg";
  level: number;
  connId: string;
  schema?: string;
  name?: string;
  tkind?: string;
  group?: "t" | "v" | "o";
  count?: number;
  segs?: Seg[];
  /** true when this row matched the active find query */
  matched?: boolean;
  text?: string;
  error?: boolean;
  estRows?: number;
  /** col rows: the owning table (name holds the column name) */
  table?: string;
  /** col rows: display type, pk flag, FK target ("table.column") */
  ctype?: string;
  pk?: boolean;
  fk?: string;
}

// ---- expansion state ---------------------------------------------------------
const expanded = reactive(new Set<string>());
const collapsedGroups = reactive(new Set<string>());
/** expanded tables (tkey → [connId, schema, table]) */
const openTables = reactive(new Map<string, readonly [string, string, string]>());
/** conn ids whose "Other databases / schemas" group is open */
const openOthers = reactive(new Set<string>());

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
    const err = projects.statuses[id]?.error;
    if (err) cache.errors[id] = err;
    else expanded.delete(ckey(id));
    return;
  }
  try {
    await loadSchemas(id);
  } catch {
    return;
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

// Expanded tables whose columns are not cached yet load them once their schema is loaded.
watch(
  () => [...openTables.values()].filter(([id, s, n]) => cache.tables[k(id, s)] && !cache.columns[kt(id, s, n)] && !cache.errors[`c:${kt(id, s, n)}`]),
  (need) => need.forEach(([id, s, n]) => loadColumns(id, s, n).catch(() => {})),
);
function dropColumns(id: string, s: string) {
  const pre = k(id, s) + "\u0000";
  for (const key of Object.keys(cache.columns)) if (key.startsWith(pre)) delete cache.columns[key];
  for (const key of Object.keys(cache.errors)) if (key.startsWith("c:" + pre)) delete cache.errors[key];
}

function collapseAll() {
  closeFind(false);
  const sel = selRow();
  expanded.clear();
  openTables.clear();
  openOthers.clear();
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
async function refreshOrExpand(id: string) {
  if (expanded.has(ckey(id))) {
    await refreshConn(id);
  } else {
    invalidate(id);
    await expandConn(id);
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

// ---- highlight-and-jump find bar ----------------------------------------------
// Keeps all tables in place in alphabetical order, highlights matches, and steps
// through them with ↑/↓ (or ⌘G/⇧⌘G). Optional filter toggle hides non-matches.
const findOpen = ref(false);
const findQuery = ref("");
const filterMode = ref(false);
const findInput = ref<HTMLInputElement | null>(null);
const matchIdx = ref(0);
const normFind = computed(() => normQuery(findQuery.value));
const hasQuery = computed(() => normFind.value.length > 0);

function openFind(selectExisting = true, initialChar?: string) {
  findOpen.value = true;
  if (initialChar !== undefined) {
    findQuery.value = findQuery.value + initialChar;
  }
  nextTick(() => {
    const el = findInput.value;
    if (!el) return;
    el.focus({ preventScroll: true });
    if (initialChar !== undefined) {
      const end = el.value.length;
      el.setSelectionRange(end, end);
    } else if (selectExisting) {
      el.select();
    }
  });
}

function closeFind(focusTree = true) {
  if (!findOpen.value && !findQuery.value) return;
  const wasFiltering = filterMode.value && hasQuery.value;
  const keepKey = selected.value ?? currentMatch.value?.row.key;
  findOpen.value = false;
  findQuery.value = "";
  matchIdx.value = 0;
  if (keepKey) {
    selected.value = keepKey;
    nextTick(() => (wasFiltering ? scrollToKeyMargin(keepKey) : scrollToKey(keepKey)));
  }
  if (focusTree) nextTick(() => scroller.value?.focus({ preventScroll: true }));
}

function toggleFindBar() {
  if (findOpen.value) closeFind(true);
  else openFind(true);
}

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
function pushColumns(out: Row[], id: string, s: string, n: string, level: number, nq: string, filter: boolean) {
  const tk = tkey(id, s, n);
  const base = { kind: "col" as const, level, connId: id, schema: s, table: n };
  const det = cache.columns[kt(id, s, n)];
  if (det) {
    for (const c of det) {
      const m = nq ? matchSidebarName(nq, c.name) : null;
      if (filter && !m) continue;
      out.push({
        ...base, key: colkey(id, s, n, c.name), name: c.name, ctype: c.type,
        pk: c.pk, fk: fkText(c.fk), segs: m?.segs, matched: !!m,
      });
    }
    if (!det.length) out.push({ key: `n:${tk}`, kind: "msg", level, connId: id, text: "No columns" });
    return;
  }
  const it = indexedTable(id, s, n);
  if (it) {
    it.columns.forEach((c, i) => {
      const m = nq ? matchSidebarName(nq, c, it.columnsL[i]) : null;
      if (filter && !m) return;
      out.push({
        ...base, key: colkey(id, s, n, c), name: c, ctype: it.types[i] || undefined,
        segs: m?.segs, matched: !!m,
      });
    });
    return;
  }
  const err = cache.errors[`c:${kt(id, s, n)}`];
  out.push(
    err
      ? { key: `e:${tk}`, kind: "msg", level, connId: id, schema: s, table: n, text: err, error: true }
      : { key: `l:${tk}`, kind: "msg", level, connId: id, text: "Loading…" },
  );
}

const rows = computed<Row[]>(() => {
  const out: Row[] = [];
  const nq = normFind.value;
  const filtering = !!nq && filterMode.value;

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
      if (showSchemas && (!filtering || expanded.has(sk))) {
        out.push({ key: sk, kind: "schema", level: lvl, connId: c.id, schema: s.name, name: s.name });
      }
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

      const hasViews = list.some((t) => isViewKind(t.kind));
      type Entry = { table: (typeof list)[number]; segs?: Seg[]; matched: boolean; hasColHit: boolean };
      const tbl: Entry[] = [];
      const vws: Entry[] = [];

      for (const t of list) {
        const m = nq ? matchSidebarName(nq, t.name) : null;
        const tk = tkey(c.id, s.name, t.name);
        let hasColHit = false;
        if (filtering && !m && openTables.has(tk)) {
          const det = cache.columns[kt(c.id, s.name, t.name)];
          if (det) hasColHit = det.some((col) => !!matchSidebarName(nq, col.name));
          else {
            const it = indexedTable(c.id, s.name, t.name);
            if (it) hasColHit = it.columns.some((col, i) => !!matchSidebarName(nq, col, it.columnsL[i]));
          }
        }
        if (filtering && !m && !hasColHit) continue;
        const entry: Entry = { table: t, segs: m?.segs, matched: !!m, hasColHit };
        if (isViewKind(t.kind)) vws.push(entry);
        else tbl.push(entry);
      }

      if (!tbl.length && !vws.length) {
        out.push({ key: `n:${sk}`, kind: "msg", level: base, connId: c.id, text: filtering ? "No matches" : "No tables" });
        return;
      }

      const emitGroup = (g: "t" | "v", items: Entry[], label: string, showHeader: boolean) => {
        if (!items.length) return;
        const gk = gkey(c.id, s.name, g);
        if (showHeader) {
          out.push({ key: gk, kind: "section", level: base, connId: c.id, schema: s.name, group: g, name: label, count: items.length });
          if (collapsedGroups.has(gk)) return;
        }
        for (const { table: t, segs, matched, hasColHit } of items) {
          const tk = tkey(c.id, s.name, t.name);
          out.push({
            key: tk, kind: "table", level: base, connId: c.id, schema: s.name,
            name: t.name, tkind: t.kind, estRows: t.estRows, segs, matched,
          });
          if (openTables.has(tk)) {
            pushColumns(out, c.id, s.name, t.name, base + 1, nq, filtering && !matched && hasColHit);
          }
        }
      };

      // Only show non-indenting Tables / Views section headers when the schema actually has views.
      emitGroup("t", tbl, "Tables", hasViews);
      emitGroup("v", vws, "Views", hasViews);
    };

    for (const s of main) pushSchema(s, 1);
    if (others.length) {
      const open = openOthers.has(c.id);
      if (!filtering || (open && others.some((s) => expanded.has(skey(c.id, s.name))))) {
        out.push({
          key: `og:${c.id}`, kind: "group", level: 1, connId: c.id, group: "o", count: others.length,
          name: c.driver === "postgres" ? "Other schemas" : "Other databases",
        });
      }
      if (open) for (const s of others) pushSchema(s, 2);
    }
  }
  return out;
});

// ---- match list, navigation & scrollbar ticks ----------------------------------
interface MatchEntry {
  row: Row;
  index: number;
}
const matches = computed<MatchEntry[]>(() => {
  if (!hasQuery.value) return [];
  const out: MatchEntry[] = [];
  const R = rows.value;
  for (let i = 0; i < R.length; i++) {
    if (R[i].matched) out.push({ row: R[i], index: i });
  }
  return out;
});
const currentMatch = computed<MatchEntry | null>(() => matches.value[matchIdx.value] ?? null);
const currentMatchKey = computed<string | null>(() => currentMatch.value?.row.key ?? null);

/** Overview ticks along the right edge showing where matches sit in the tree. */
const matchTicks = computed<{ pct: number; cur: boolean }[]>(() => {
  const list = matches.value;
  const total = rows.value.length;
  if (!hasQuery.value || filterMode.value || !list.length || total <= 1) return [];
  if (totalSize.value <= viewH.value) return [];
  const curKey = currentMatchKey.value;
  const buckets = new Map<number, { pct: number; cur: boolean }>();
  for (const m of list) {
    const pct = ((m.index + 0.5) / total) * 100;
    const b = Math.round(pct * 1.5); // ~150 vertical buckets max
    const isCur = m.row.key === curKey;
    const prev = buckets.get(b);
    if (!prev || isCur) buckets.set(b, { pct, cur: prev?.cur || isCur });
  }
  return [...buckets.values()];
});

function jumpToMatch(idx: number) {
  const list = matches.value;
  if (!list.length) return;
  const i = ((idx % list.length) + list.length) % list.length;
  matchIdx.value = i;
  const hit = list[i];
  selected.value = hit.row.key;
  nextTick(() => scrollToKeyMargin(hit.row.key));
}
function stepMatch(delta: number) {
  if (!matches.value.length) return;
  jumpToMatch(matchIdx.value + delta);
}

watch(normFind, (now, old) => {
  if (!now) {
    matchIdx.value = 0;
    return;
  }
  const list = matches.value;
  if (!list.length) {
    matchIdx.value = 0;
    return;
  }
  // When refining an active query, stay on the current row if it still matches.
  if (old && selected.value) {
    const stay = list.findIndex((m) => m.row.key === selected.value);
    if (stay >= 0) {
      matchIdx.value = stay;
      nextTick(() => scrollToKeyMargin(list[stay].row.key));
      return;
    }
  }
  jumpToMatch(0);
});

watch(filterMode, () => {
  if (!hasQuery.value || !matches.value.length) return;
  const stay = selected.value ? matches.value.findIndex((m) => m.row.key === selected.value) : -1;
  jumpToMatch(stay >= 0 ? stay : 0);
});

watch(matches, (list) => {
  if (!hasQuery.value) return;
  if (!list.length) {
    matchIdx.value = 0;
    return;
  }
  if (selected.value) {
    const idx = list.findIndex((m) => m.row.key === selected.value);
    if (idx >= 0) {
      matchIdx.value = idx;
      return;
    }
  }
  if (matchIdx.value >= list.length) matchIdx.value = 0;
});

// ---- selection / virtualization --------------------------------------------------
const selected = ref<string | null>(null);
const scroller = ref<HTMLElement | null>(null);
const focused = ref(false);
const PAD_TOP = 4; // virtualizer paddingStart (kept off scroller so sticky top: 0 is flush with no see-through gap)
const PAD_END = 8;
const rowH = computed(() => (settings.density === "comfortable" ? 28 : 26));
const virt = useVirtualizer<HTMLElement, Element>(
  computed(() => ({
    count: rows.value.length,
    getScrollElement: () => scroller.value,
    estimateSize: () => rowH.value,
    paddingStart: PAD_TOP,
    paddingEnd: PAD_END,
    overscan: 14,
    getItemKey: (i: number) => rows.value[i]?.key ?? i,
  })),
);
watch(rowH, () => nextTick(() => virt.value.measure()));

const vItems = computed(() => virt.value.getVirtualItems());
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

// ---- sticky ancestors (capped at Connection + Schema) -----------------------------
const STICKY_MAX = 2;
const scrollTop = ref(0);
const viewH = ref(0);
const sticky = shallowRef<{ row: Row; top: number }[]>([]);
const stickyCap = computed(() => Math.max(1, Math.min(STICKY_MAX, Math.floor(viewH.value / rowH.value / 4))));

function isStickyKind(r: Row): boolean {
  return r.kind === "conn" || r.kind === "schema" || r.kind === "group";
}
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
watch([scrollTop, rows, stickyCap, rowH], () => {
  const R = rows.value;
  const st = scrollTop.value;
  const H = rowH.value;
  const out: { row: Row; top: number }[] = [];
  let parent = -1;
  for (let d = 0; d < stickyCap.value && R.length; d++) {
    const i = Math.min(R.length - 1, Math.floor((st + d * H - PAD_TOP) / H));
    if (i < 0) break;
    const a = R[i].level === d && st + d * H > PAD_TOP + i * H ? i : ancestorAt(i, d);
    if (a < 0 || (d > 0 && ancestorAt(a, d - 1) !== parent)) break;
    if (!isStickyKind(R[a]) || !isOpen(R[a])) break;
    const end = subtreeEnd(a);
    if (end <= a) break;
    const endY = PAD_TOP + (end + 1) * H - st;
    out.push({ row: R[a], top: Math.min(d * H, endY - H) });
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
  const R = rows.value;
  const row = R[i];
  if (!row) return 0;
  let count = 0;
  for (let d = 0; d < stickyCap.value; d++) {
    const a = ancestorAt(i, d);
    if (a < 0 || !isStickyKind(R[a]) || !isOpen(R[a])) break;
    count++;
  }
  return count * rowH.value;
}

/** Scrolls a row into view with ~3 rows of margin; centers it when off screen. */
function scrollToKeyMargin(key: string) {
  const i = rows.value.findIndex((r) => r.key === key);
  const el = scroller.value;
  if (i < 0 || !el) return;
  const H = rowH.value;
  const top = i * H + PAD_TOP;
  const view = el.clientHeight;
  const head = stickyAbove(i);
  const margin = Math.min(3 * H, Math.max(0, (view - head - H) / 2));
  if (top < el.scrollTop + head || top + H > el.scrollTop + view) {
    el.scrollTop = Math.max(0, top - head - Math.max(0, view - head - H) / 2);
  } else if (top < el.scrollTop + head + margin) {
    el.scrollTop = Math.max(0, top - head - margin);
  } else if (top + H > el.scrollTop + view - margin) {
    el.scrollTop = top + H - view + margin;
  }
}

/** Minimal scroll that shows a row, keeping it clear of the sticky rows above it. */
function scrollToKey(key: string) {
  const i = rows.value.findIndex((r) => r.key === key);
  const el = scroller.value;
  if (i < 0 || !el) return;
  const H = rowH.value;
  const top = i * H + PAD_TOP;
  const head = stickyAbove(i);
  if (top - head < el.scrollTop) el.scrollTop = Math.max(0, top - head);
  else if (top + H > el.scrollTop + el.clientHeight) el.scrollTop = top + H - el.clientHeight;
}

function moveSel(delta: number) {
  const list = rows.value;
  if (!list.length) return;
  let i = list.findIndex((r) => r.key === selected.value);
  i = i < 0 ? (delta > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, i + delta));
  const key = list[i].key;
  selected.value = key;
  if (hasQuery.value) {
    const mi = matches.value.findIndex((m) => m.row.key === key);
    if (mi >= 0) matchIdx.value = mi;
  }
  scrollToKey(key);
}
function selRow(): Row | undefined {
  return rows.value.find((r) => r.key === selected.value);
}

// ---- actions ---------------------------------------------------------------------
function openTable(r: Row, structure = false, preview = true, focus = false) {
  if (r.kind !== "table" || !r.name) return;
  if (structure) tabs.openStructure(r.connId, r.schema ?? "", r.name, { focus });
  else tabs.openTable(r.connId, r.schema ?? "", r.name, { preview, focus });
}
function openColumn(r: Row, structure = false, preview = true, focus = false) {
  if (r.kind !== "col" || !r.table || !r.name) return;
  if (structure) tabs.openStructure(r.connId, r.schema ?? "", r.table, { focus });
  else tabs.openTable(r.connId, r.schema ?? "", r.table, { preview, focus });
  if (!structure) ui.focusColumn(r.connId, r.schema ?? "", r.table, r.name);
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
      }
      break;
    case "section":
      if (r.group === "t" || r.group === "v") toggleGroup(r.connId, r.schema!, r.group);
      break;
    case "table":
      openTable(r, alt, true, viaKey);
      break;
    case "col":
      if (viaKey) openColumn(r, alt, true, true);
      break;
    case "msg":
      if (r.error && r.table) delete cache.errors[`c:${kt(r.connId, r.schema!, r.table)}`];
      else if (r.error) void refreshConn(r.connId);
      break;
  }
}

function openCurrentMatch(structure = false) {
  const hit = hasQuery.value ? currentMatch.value?.row : selRow();
  if (!hit) return;
  const wasFiltering = filterMode.value && hasQuery.value;
  activate(hit, structure, true);
  if (hit.kind === "table" || hit.kind === "col") {
    const keepKey = hit.key;
    findOpen.value = false;
    findQuery.value = "";
    matchIdx.value = 0;
    selected.value = keepKey;
    nextTick(() => (wasFiltering ? scrollToKeyMargin(keepKey) : scrollToKey(keepKey)));
  }
}

function onRowClick(r: Row) {
  selected.value = r.key;
  if (hasQuery.value) {
    const mi = matches.value.findIndex((m) => m.row.key === r.key);
    if (mi >= 0) matchIdx.value = mi;
  }
  scroller.value?.focus({ preventScroll: true });
  activate(r);
  nextTick(() => scroller.value?.focus({ preventScroll: true }));
}
function onRowDblClick(r: Row) {
  if (r.kind === "table") openTable(r, false, false, true);
  else if (r.kind === "col") openColumn(r, false, false, true);
}
function onChevClick(r: Row) {
  selected.value = r.key;
  toggle(r);
  nextTick(() => scroller.value?.focus({ preventScroll: true }));
}
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
  if (r.kind === "group") return openOthers.has(r.connId);
  if (r.kind === "section") return !collapsedGroups.has(gkey(r.connId, r.schema!, r.group as "t" | "v"));
  if (r.kind === "table") return openTables.has(r.key);
  return false;
}
function canExpand(r: Row) {
  return r.kind === "conn" || r.kind === "schema" || r.kind === "group" || r.kind === "section" || r.kind === "table";
}

// ---- keyboard -------------------------------------------------------------------
function onAsideKeydown(e: KeyboardEvent) {
  const mod = isMod(e);
  const kLow = e.key.toLowerCase();
  if (mod && !e.altKey && !e.shiftKey && kLow === "f") {
    e.preventDefault();
    e.stopPropagation();
    openFind(true);
    return;
  }
  if (((mod && !e.altKey && kLow === "g") || e.key === "F3") && hasQuery.value) {
    e.preventDefault();
    e.stopPropagation();
    stepMatch(e.shiftKey ? -1 : 1);
    return;
  }
  if (e.altKey && !mod && !e.shiftKey && e.code === "KeyF" && findOpen.value) {
    e.preventDefault();
    e.stopPropagation();
    filterMode.value = !filterMode.value;
    return;
  }
  if (e.key === "Escape" && findOpen.value && !menu.value && !projMenu.value && !errDlg.value && !projDlg.value) {
    e.preventDefault();
    e.stopPropagation();
    closeFind(true);
    return;
  }
}

function onFindKeydown(e: KeyboardEvent) {
  const mod = isMod(e);
  if (e.key === "ArrowDown") {
    e.preventDefault();
    if (matches.value.length) stepMatch(1);
    else moveSel(1);
    return;
  }
  if (e.key === "ArrowUp") {
    e.preventDefault();
    if (matches.value.length) stepMatch(-1);
    else moveSel(-1);
    return;
  }
  if (e.key === "Enter") {
    e.preventDefault();
    openCurrentMatch(mod || e.shiftKey);
    return;
  }
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
      if (findOpen.value) {
        e.preventDefault();
        e.stopPropagation();
        closeFind(true);
      } else if (tabs.activeId) {
        e.preventDefault();
        ui.focusActiveView();
      }
      return;
    case "ArrowRight":
      if (r && canExpand(r)) {
        e.preventDefault();
        if (!isOpen(r)) toggle(r);
        else moveSel(1);
      }
      return;
    case "ArrowLeft":
      if (r) {
        e.preventDefault();
        if (canExpand(r) && isOpen(r)) toggle(r);
        else {
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
    if (e.key === " " && !findOpen.value) {
      e.preventDefault();
      if (r && canExpand(r)) toggle(r);
      return;
    }
    e.preventDefault();
    openFind(false, e.key);
  }
}

// ---- reveal / focus ---------------------------------------------------------------
async function reveal(connId: string, schema?: string, table?: string, focus = true, column?: string) {
  if (focus) closeFind(false);
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
        await loadColumns(connId, s, table).catch(() => loadIndex(connId, s).catch(() => {}));
      }
    }
  }
  await nextTick();
  const key =
    table && s !== undefined ? (column ? colkey(connId, s, table, column) : tkey(connId, s, table)) : ckey(connId);
  if (!column && table && s !== undefined && selected.value?.startsWith(colkey(connId, s, table, ""))) return;
  if (filterMode.value && hasQuery.value && !rows.value.some((r) => r.key === key)) {
    filterMode.value = false;
    await nextTick();
  }
  if (!rows.value.some((r) => r.key === key) && column) {
    return reveal(connId, schema, table, focus);
  }
  selected.value = key;
  scrollToKeyMargin(key);
  if (focus) scroller.value?.focus({ preventScroll: true });
}
watch(
  () => ui.revealRequest,
  (r) => r && void reveal(r.connId, r.schema, r.table, r.focus !== false, r.column),
);
watch(
  () => [tabs.activeId, projects.loaded] as const,
  () => {
    const t = tabs.active;
    if (!t || hasQuery.value || !projects.getConnection(t.connId)) return;
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
  for (const c of projects.projectConnections) if (projects.isConnected(c.id)) void expandConn(c.id);
});
watch(
  () => projects.activeProjectId,
  () => {
    findOpen.value = false;
    findQuery.value = "";
    matchIdx.value = 0;
    selected.value = null;
    for (const c of projects.projectConnections) if (projects.isConnected(c.id) && !expanded.has(ckey(c.id))) void expandConn(c.id);
  },
);

// ---- context menus ------------------------------------------------------------------
const menu = ref<{ x: number; y: number; items: MenuItem[]; run: (id: string) => void } | null>(null);
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
  } else if (r.kind === "schema" || (r.kind === "section" && r.schema !== undefined)) {
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
const dragging = ref(false);
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

// ---- drag reorder (connections) -------------------------------------------------------
const dragConn = ref("");
const dropAt = ref<{ connId: string; after: boolean } | null>(null);
function onDragStart(e: DragEvent, r: Row) {
  if (r.kind !== "conn") {
    e.preventDefault();
    return;
  }
  dragConn.value = r.connId;
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", r.name ?? "");
  }
}
function onDragOver(e: DragEvent, r: Row) {
  if (!dragConn.value) return;
  e.preventDefault();
  if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
  let after = true;
  if (r.kind === "conn") {
    const b = (e.currentTarget as HTMLElement).getBoundingClientRect();
    after = e.clientY > b.top + b.height / 2;
  }
  const cur = dropAt.value;
  if (!cur || cur.connId !== r.connId || cur.after !== after) dropAt.value = { connId: r.connId, after };
}
async function onDrop(e: DragEvent) {
  e.preventDefault();
  const src = dragConn.value;
  const at = dropAt.value;
  onDragEnd();
  if (!src || !at || at.connId === src) return;
  const ids = projects.projectConnections.map((c) => c.id).filter((id) => id !== src);
  const i = ids.indexOf(at.connId);
  if (i < 0) return;
  ids.splice(at.after ? i + 1 : i, 0, src);
  if (ids.every((id, j) => projects.projectConnections[j]?.id === id)) return;
  try {
    await projects.reorderConnections(projects.activeProjectId, ids);
  } catch (err) {
    ui.toast(apiError(err).message, "error");
  }
}
function onDragEnd() {
  dragConn.value = "";
  dropAt.value = null;
}
const lastRowOf = computed(() => {
  const m: Record<string, string> = {};
  for (const r of rows.value) m[r.connId] = r.key;
  return m;
});
function dropClass(r: Row): string | undefined {
  const at = dropAt.value;
  if (!at || at.connId !== r.connId || at.connId === dragConn.value) return undefined;
  if (!at.after) return r.kind === "conn" ? "drop-before" : undefined;
  return lastRowOf.value[r.connId] === r.key ? "drop-after" : undefined;
}

// ---- row helpers ----------------------------------------------------------------------
/** Shortens the longest unmatched run (middle ellipsis) so matched text stays visible in narrow sidebars. */
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
let charWCache: { font: string; w: number } | null = null;
let charWTick: number | null = null;
function charWidth(): number {
  if (charWTick !== null && charWCache) return charWCache.w;
  charWTick = requestAnimationFrame(() => (charWTick = null));
  const el = scroller.value;
  const cs = el ? getComputedStyle(el) : null;
  const size = cs?.fontSize ?? "12.5px";
  const sans = getComputedStyle(document.documentElement).getPropertyValue("--sans").trim() || "sans-serif";
  const font = `${size} ${sans}`;
  if (charWCache?.font === font) return charWCache.w;
  const ctx = document.createElement("canvas").getContext("2d");
  let w = parseFloat(size) * 0.54;
  if (ctx) {
    ctx.font = font;
    w = ctx.measureText("sales_order_entity_id").width / 21 || w;
  }
  charWCache = { font, w };
  return w;
}
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

function rowSegs(r: Row): Seg[] {
  const segs = r.segs ?? [{ t: r.name ?? "", m: false }];
  if (!r.matched || !r.segs) return segs;
  // Keep highlighted runs visible by middle-truncating long unmatched spans.
  const rightReserve = r.kind === "table" && r.estRows !== undefined && r.estRows >= 0 ? 36 : 12;
  const px = Math.max(40, treeW.value - (12 + 6 + r.level * 12 + 13 + 18 + rightReserve));
  const avail = Math.max(10, Math.floor(px / charWidth()));
  return fitSegs(segs, avail);
}

function formatCompactRows(n?: number): string {
  if (n === undefined || n < 0) return "";
  if (n < 1000) return String(n);
  if (n < 100_000) return (n / 1000).toFixed(1).replace(/\.0$/, "") + "k";
  if (n < 1_000_000) return Math.round(n / 1000) + "k";
  if (n < 100_000_000) return (n / 1_000_000).toFixed(1).replace(/\.0$/, "") + "M";
  return Math.round(n / 1_000_000) + "M";
}

function connOf(id: string): ConnectionConfig | undefined {
  return projects.getConnection(id);
}
function accentOf(id: string): string | undefined {
  const c = connOf(id)?.color;
  return c && c !== "none" ? c : undefined;
}
function statusDotColor(id: string): string {
  const c = connOf(id)?.color;
  if (c && c !== "none") return `var(--conn-${c})`;
  return projects.isConnected(id) ? "var(--ok)" : "var(--dim)";
}
function badge(d?: string): string {
  return DRIVER_BADGE[d as Driver] ?? (d ?? "").slice(0, 2).toUpperCase();
}
function isActiveTable(r: Row) {
  const t = tabs.active;
  if (t && r.kind === "conn") return t.kind === "console" && t.connId === r.connId;
  return !!t && r.kind === "table" && t.connId === r.connId && (t.schema ?? "") === (r.schema ?? "") && t.table === r.name;
}
function isDimmed(r: Row): boolean {
  return hasQuery.value && !filterMode.value && (r.kind === "table" || r.kind === "col") && !r.matched;
}
function rowTitle(r: Row): string | undefined {
  if (r.kind === "table") {
    const rowsTxt = r.estRows !== undefined && r.estRows >= 0 ? ` · ~${r.estRows.toLocaleString("en-US")} rows` : "";
    return `${r.name}${rowsTxt}`;
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
  <div v-show="ui.sidebarVisible" class="no-drag relative flex-none" :style="{ width: ui.sidebarWidth + 'px' }">
  <aside
    class="absolute inset-y-0 left-0 flex flex-col border-r border-bd bg-bg2"
    :style="{ width: ui.sidebarWidth + 'px', transition: dragging ? 'none' : 'width .12s ease' }"
    @keydown="onAsideKeydown"
  >
    <!-- header: ghost project switcher + compact icon actions -->
    <div class="flex flex-none items-center gap-0.5 border-b border-bd px-2 py-1.5">
      <button
        class="hdr-proj min-w-0 flex-1"
        title="Switch project"
        @click="openProjMenu"
      >
        <Icon name="folder" :size="13" class="dim flex-none" />
        <span class="truncate font-semibold">{{ projects.activeProject?.name ?? "…" }}</span>
        <Icon name="chevD" :size="11" class="dim flex-none opacity-70" />
      </button>
      <button
        class="hdr-icon"
        :class="{ on: findOpen }"
        :title="kbd('Find in sidebar (⌘F)')"
        aria-label="Find in sidebar"
        @click="toggleFindBar"
      ><Icon name="search" :size="13" /></button>
      <button
        class="hdr-icon"
        title="Collapse all"
        aria-label="Collapse all"
        :disabled="!expanded.size && !openTables.size"
        @click="collapseAll"
      ><Icon name="collapse" :size="13" /></button>
      <button
        class="hdr-icon"
        title="New connection"
        aria-label="New connection"
        @click="newConnection"
      ><Icon name="plus" :size="13" /></button>
    </div>

    <!-- top find bar (highlight-and-jump by default, optional filter toggle) -->
    <div v-if="findOpen" class="findbar flex flex-none items-center gap-1 border-b border-bd px-2 py-1.5">
      <div class="findbox flex min-w-0 flex-1 items-center gap-1.5">
        <Icon name="search" :size="12" :class="hasQuery ? 'text-ac' : 'dim'" />
        <input
          ref="findInput"
          v-model="findQuery"
          class="find-input min-w-0 flex-1"
          placeholder="Find tables…"
          spellcheck="false"
          autocomplete="off"
          aria-label="Find tables in sidebar"
          @keydown="onFindKeydown"
        />
        <span
          v-if="hasQuery"
          class="find-cnt mono"
          :class="{ none: !matches.length }"
        >{{ matches.length ? `${matchIdx + 1}/${matches.length}` : "0/0" }}</span>
      </div>
      <button
        class="find-btn"
        :disabled="!matches.length"
        :title="kbd('Previous match (↑ / ⇧⌘G)')"
        aria-label="Previous match"
        @mousedown.prevent
        @click="stepMatch(-1)"
      ><Icon name="chevU" :size="12" /></button>
      <button
        class="find-btn"
        :disabled="!matches.length"
        :title="kbd('Next match (↓ / ⌘G)')"
        aria-label="Next match"
        @mousedown.prevent
        @click="stepMatch(1)"
      ><Icon name="chevD" :size="12" /></button>
      <button
        class="find-btn"
        :class="{ on: filterMode }"
        :title="kbd('Filter matching only (⌥F)')"
        aria-label="Toggle filter mode"
        :aria-pressed="filterMode"
        @mousedown.prevent
        @click="filterMode = !filterMode; findInput?.focus()"
      ><Icon name="filter" :size="12" /></button>
      <button
        class="find-btn"
        title="Close (Esc)"
        aria-label="Close find"
        @click="closeFind(true)"
      ><Icon name="x" :size="11" /></button>
    </div>

    <!-- tree + match overview ticks -->
    <div class="relative flex min-h-0 flex-1 flex-col">
      <div
        ref="scroller"
        tabindex="0"
        class="min-h-0 flex-1 overflow-auto outline-none"
        role="tree"
        aria-label="Connections"
        @keydown="onKeydown"
        @focus="focused = true"
        @blur="focused = false"
        @scroll="onScroll"
      >
        <div v-if="projects.loaded && !projects.projectConnections.length" class="flex flex-col items-center gap-2 px-4 py-10 text-center">
          <div class="dim">No connections in this project yet.</div>
          <button class="btn primary" @click="newConnection"><Icon name="plus" /> New connection</button>
        </div>
        <div :style="{ height: totalSize + 'px', position: 'relative', width: '100%' }">
          <div
            v-for="it in drawItems"
            :key="it.key"
            class="rowwrap px-1.5"
            :class="it.stuck ? 'stk' : 'absolute left-0 right-0 top-0'"
            :style="it.stuck ? { top: it.y + 'px', zIndex: it.z, height: rowH + 'px' } : { transform: `translateY(${it.y}px)`, height: rowH + 'px' }"
          >
            <div
              class="tree-row group relative flex h-full items-center gap-1.5 whitespace-nowrap pr-2"
              :class="[{
                sel: selected === it.row.key,
                foc: focused,
                act: isActiveTable(it.row),
                hit: it.row.matched,
                'cur-hit': currentMatchKey === it.row.key,
                dimmed: isDimmed(it.row),
                section: it.row.kind === 'section',
                msg: it.row.kind === 'msg',
                dragging: it.row.kind === 'conn' && dragConn === it.row.connId,
              }, it.stuck ? undefined : dropClass(it.row)]"
              :style="{
                paddingLeft: 6 + it.row.level * 12 + 'px',
              }"
              :role="it.stuck ? undefined : 'treeitem'"
              :data-accent="accentOf(it.row.connId)"
              :aria-selected="it.stuck ? undefined : selected === it.row.key"
              :aria-hidden="it.stuck || undefined"
              :title="rowTitle(it.row)"
              @click="it.stuck ? onStickyClick(it.row) : onRowClick(it.row)"
              @dblclick="onRowDblClick(it.row)"
              @contextmenu="onContext($event, it.row)"
              :draggable="it.row.kind === 'conn'"
              @dragstart="onDragStart($event, it.row)"
              @dragover="onDragOver($event, it.row)"
              @drop="onDrop"
              @dragend="onDragEnd"
            >
              <!-- subtle vertical indent guides -->
              <template v-if="!it.stuck && it.row.level > 0">
                <span
                  v-for="g in it.row.level"
                  :key="g"
                  class="iguide"
                  :class="{ last: g === it.row.level }"
                  :style="{ left: 6 + (g - 1) * 12 + 5 + 'px' }"
                />
              </template>

              <!-- connection row -->
              <template v-if="it.row.kind === 'conn'">
                <span
                  class="chev"
                  :class="{ open: isOpen(it.row) }"
                  @click.stop="it.stuck ? onStickyClick(it.row, true) : onChevClick(it.row)"
                  @dblclick.stop
                ><Icon name="chev" :size="11" /></span>
                <span
                  v-if="projects.statuses[it.row.connId]?.connecting"
                  class="spinner"
                  title="Connecting…"
                />
                <span
                  v-else-if="projects.statuses[it.row.connId]?.error && !projects.statuses[it.row.connId]?.connected"
                  class="flex-none text-danger"
                  :title="projects.statuses[it.row.connId]?.error"
                ><Icon name="warn" :size="12" /></span>
                <span
                  v-else
                  class="sdot"
                  :class="{ on: projects.isConnected(it.row.connId) }"
                  :style="{ '--dot': statusDotColor(it.row.connId) }"
                  :title="projects.isConnected(it.row.connId) ? 'Connected' : 'Disconnected'"
                />
                <span class="name font-medium">{{ it.row.name }}</span>
                <span v-if="projects.isReadOnly(it.row.connId)" class="flex-none" :class="accentOf(it.row.connId) === 'red' ? 'text-danger' : 'dim'" title="Read-only"><Icon name="lock" :size="10" /></span>
                <span class="drv mono">{{ badge(connOf(it.row.connId)?.driver) }}</span>
                <span class="row-acts" @click.stop @dblclick.stop>
                  <button
                    class="act-btn"
                    tabindex="-1"
                    title="New SQL console"
                    aria-label="New SQL console"
                    @click="tabs.openConsole(it.row.connId)"
                  ><Icon name="sql" :size="12" /></button>
                  <button
                    class="act-btn"
                    tabindex="-1"
                    title="Refresh"
                    aria-label="Refresh connection"
                    @click="refreshOrExpand(it.row.connId)"
                  ><Icon name="refresh" :size="11" /></button>
                </span>
              </template>

              <!-- schema row -->
              <template v-else-if="it.row.kind === 'schema'">
                <span
                  class="chev"
                  :class="{ open: isOpen(it.row) }"
                  @click.stop="it.stuck ? onStickyClick(it.row, true) : onChevClick(it.row)"
                  @dblclick.stop
                ><Icon name="chev" :size="11" /></span>
                <Icon name="schema" :size="12" class="tico" />
                <span class="name">{{ it.row.name }}</span>
              </template>

              <!-- "Other schemas / databases" group -->
              <template v-else-if="it.row.kind === 'group'">
                <span
                  class="chev"
                  :class="{ open: isOpen(it.row) }"
                  @click.stop="it.stuck ? onStickyClick(it.row, true) : onChevClick(it.row)"
                  @dblclick.stop
                ><Icon name="chev" :size="11" /></span>
                <span class="dim text-[11px]">{{ it.row.name }}</span>
                <span class="cnt">{{ it.row.count }}</span>
              </template>

              <!-- non-indenting section header (Tables / Views when views exist) -->
              <template v-else-if="it.row.kind === 'section'">
                <span
                  class="chev"
                  :class="{ open: isOpen(it.row) }"
                  @click.stop="onChevClick(it.row)"
                  @dblclick.stop
                ><Icon name="chev" :size="10" /></span>
                <span class="sec-lbl">{{ it.row.name }}</span>
                <span class="cnt">{{ it.row.count }}</span>
              </template>

              <!-- table / view row -->
              <template v-else-if="it.row.kind === 'table'">
                <span
                  class="tchev"
                  :class="{ open: isOpen(it.row) }"
                  title="Show columns"
                  @click.stop="onChevClick(it.row)"
                  @dblclick.stop
                ><Icon name="chev" :size="10" /></span>
                <Icon :name="isViewKind(it.row.tkind ?? '') ? 'view' : 'table'" :size="13" class="tico" />
                <span class="name">
                  <template v-for="(s, i) in rowSegs(it.row)" :key="i"><mark v-if="s.m" class="hl">{{ s.t }}</mark><template v-else>{{ s.t }}</template></template>
                </span>
                <span v-if="formatCompactRows(it.row.estRows)" class="rcnt mono">{{ formatCompactRows(it.row.estRows) }}</span>
                <span class="row-acts" @click.stop @dblclick.stop>
                  <button
                    class="act-btn"
                    tabindex="-1"
                    :title="kbd('Open structure (⌘↵)')"
                    aria-label="Open structure"
                    @click="openTable(it.row, true, false, true)"
                  ><Icon name="structure" :size="12" /></button>
                </span>
              </template>

              <!-- column row -->
              <template v-else-if="it.row.kind === 'col'">
                <span v-if="it.row.pk" class="cico text-pk"><Icon name="key" :size="11" /></span>
                <span v-else-if="it.row.fk" class="cico text-fk"><Icon name="link" :size="11" /></span>
                <span v-else class="cico"><span class="cdot" /></span>
                <span class="name text-[12px]">
                  <template v-for="(s, i) in rowSegs(it.row)" :key="i"><mark v-if="s.m" class="hl">{{ s.t }}</mark><template v-else>{{ s.t }}</template></template>
                </span>
                <span v-if="it.row.ctype" class="ctype mono">{{ it.row.ctype }}</span>
              </template>

              <!-- message / error row -->
              <template v-else>
                <span :class="it.row.error ? 'text-danger' : 'dim'" class="truncate text-[11px]">{{ it.row.error ? shortError(it.row.text ?? "") : it.row.text }}</span>
                <span v-if="it.row.error" class="dim flex-none text-[10px]">· retry</span>
              </template>
            </div>
          </div>
        </div>
      </div>

      <!-- scrollbar match overview ticks -->
      <div v-if="matchTicks.length" class="tick-rail" aria-hidden="true">
        <span
          v-for="(t, i) in matchTicks"
          :key="i"
          class="tick"
          :class="{ cur: t.cur }"
          :style="{ top: t.pct + '%' }"
        />
      </div>
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
/* header */
.hdr-proj {
  display: inline-flex; align-items: center; gap: 6px; height: 26px; padding: 0 6px;
  border: 0; border-radius: 6px; background: transparent; color: var(--tx); cursor: default;
  transition: background .1s;
}
.hdr-proj:hover { background: var(--hov); }
.hdr-icon {
  display: inline-flex; flex: none; align-items: center; justify-content: center;
  width: 24px; height: 24px; padding: 0; border: 0; border-radius: 6px;
  background: transparent; color: var(--dim); cursor: default; transition: background .1s, color .1s;
}
.hdr-icon:hover:not(:disabled) { background: var(--hov); color: var(--tx); }
.hdr-icon.on { background: var(--selbg); color: var(--ac); }
.hdr-icon:disabled { opacity: .35; }

/* top find bar */
.findbox {
  height: 26px; padding: 0 7px; border-radius: 6px;
  background: var(--bg); border: 1px solid var(--bd);
  transition: border-color .1s, box-shadow .1s;
}
.findbox:focus-within { border-color: var(--ac); box-shadow: 0 0 0 2px var(--ring); }
.find-input {
  border: 0; background: transparent; color: var(--tx); outline: none; font-size: 12px;
  width: 100%; padding: 0;
}
.find-input::placeholder { color: var(--dim); opacity: .7; }
.find-cnt {
  flex: none; font-size: 10.5px; color: var(--dim); font-variant-numeric: tabular-nums;
}
.find-cnt.none { color: var(--danger); }
.find-btn {
  display: inline-flex; flex: none; align-items: center; justify-content: center;
  width: 22px; height: 22px; padding: 0; border: 0; border-radius: 5px;
  background: transparent; color: var(--dim); cursor: default; transition: background .1s, color .1s;
}
.find-btn:hover:not(:disabled) { background: var(--hov); color: var(--tx); }
.find-btn.on { background: var(--selbg); color: var(--ac); }
.find-btn:disabled { opacity: .3; }

/* tree rows */
.tree-row { cursor: default; border-radius: 6px; transition: background .08s, opacity .1s; }
.stk { position: sticky; background: var(--bg2); box-shadow: 0 -1px 0 var(--bg2); }
.stk:has(+ .rowwrap:not(.stk)) { box-shadow: 0 -1px 0 var(--bg2), 0 6px 6px -6px var(--shadow); }
.iguide {
  position: absolute; top: 0; bottom: 0; width: 1px; background: var(--bd); opacity: .65; pointer-events: none;
  transition: opacity .1s;
}
.tree-row:has(.tchev):hover .iguide.last,
.tree-row:has(.tchev.open) .iguide.last { opacity: 0; }

/* icons & chevrons */
.tico { flex: none; color: var(--dim); opacity: .75; }
.chev { display: inline-flex; flex: none; color: var(--dim); opacity: .75; transition: transform .1s, color .1s; }
.chev.open { transform: rotate(90deg); }
.tchev {
  display: inline-flex; flex: none; align-items: center; justify-content: center;
  width: 14px; height: 14px; margin-left: -14px; margin-right: -4px; border-radius: 3px;
  color: var(--dim); opacity: 0; transition: opacity .1s, transform .1s, color .1s;
}
.tchev.open { opacity: .85; transform: rotate(90deg); }
.tree-row:hover .tchev { opacity: .75; }
.tchev:hover { opacity: 1 !important; color: var(--tx); }

/* unified connection status dot */
.sdot {
  width: 7px; height: 7px; border-radius: 50%; flex: none; display: inline-block;
  border: 1.5px solid var(--dot, var(--dim)); background: transparent;
  transition: background .15s, box-shadow .15s;
}
.sdot.on {
  border-color: var(--dot, var(--ok));
  background: var(--dot, var(--ok));
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--dot, var(--ok)) 22%, transparent);
}

/* columns */
.cico { display: inline-flex; flex: none; width: 13px; justify-content: center; align-items: center; }
.cdot { width: 4px; height: 4px; border-radius: 50%; background: var(--dim); opacity: .5; }
.ctype {
  margin-left: auto; flex: 0 1000 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis;
  white-space: nowrap; font-size: 10.5px; color: var(--dim); opacity: .75;
}

/* text, sections & right-aligned metadata */
.name { flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sec-lbl {
  font-size: 10px; font-weight: 600; letter-spacing: .06em; text-transform: uppercase;
  color: var(--dim); opacity: .8;
}
.cnt { margin-left: auto; color: var(--dim); font: 10px var(--mono); opacity: .7; font-variant-numeric: tabular-nums; }
.drv {
  margin-left: auto; flex: none; font-size: 10px; color: var(--dim); opacity: .65;
  letter-spacing: .04em;
}
.rcnt {
  margin-left: auto; flex: none; font-size: 10.5px; color: var(--dim); opacity: .65;
  font-variant-numeric: tabular-nums;
}

/* hover quick actions (swap out driver / row count cleanly on hover) */
.row-acts { display: none; margin-left: auto; flex: none; align-items: center; gap: 2px; }
.tree-row:hover .row-acts { display: inline-flex; }
.tree-row:hover .drv,
.tree-row:hover .rcnt { display: none; }
.act-btn {
  display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; padding: 0; border: 0; border-radius: 4px;
  background: transparent; color: var(--dim); cursor: default;
}
.act-btn:hover { background: var(--bg3); color: var(--tx); }

/* row states: clearly distinct active tab, keyboard cursor, and find matches */
.tree-row:hover { background: var(--hov); }
.tree-row.msg:hover { background: none; }
.tree-row.dimmed { opacity: .45; }
.tree-row.dimmed:hover { opacity: 1; }

.tree-row.act {
  color: var(--tx);
  background: color-mix(in srgb, var(--ac) 10%, transparent);
  font-weight: 500;
}
.tree-row.act::before {
  content: ""; position: absolute; left: 0; top: 4px; bottom: 4px; width: 2px;
  border-radius: 2px; background: var(--ac);
}
.tree-row.act .tico { color: var(--ac); opacity: 1; }

.tree-row.sel:not(.act):not(.cur-hit) { background: var(--hov); }
.tree-row.sel.foc:not(.cur-hit):not(.drop-before):not(.drop-after) {
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--ac) 50%, transparent);
}

.tree-row.cur-hit {
  opacity: 1;
  background: var(--selbg);
}
.tree-row.cur-hit:not(.drop-before):not(.drop-after) {
  box-shadow: inset 0 0 0 1px var(--ac);
}

/* find match character highlights */
.hl {
  background: var(--mark); color: var(--tx); border-radius: 2px; padding: 0 1px; font-weight: 600;
}
.tree-row.cur-hit .hl {
  background: var(--ac); color: var(--ac-fg, #fff);
}

/* drag & drop */
.tree-row.dragging { opacity: .5; }
.tree-row.drop-before { box-shadow: inset 0 2px 0 var(--ac); border-radius: 0; }
.tree-row.drop-after { box-shadow: inset 0 -2px 0 var(--ac); border-radius: 0; }

/* scrollbar match overview ticks */
.tick-rail {
  position: absolute; top: 4px; right: 1px; bottom: 4px; width: 4px;
  pointer-events: none; z-index: 4;
}
.tick {
  position: absolute; left: 0; right: 0; height: 2px; border-radius: 1px;
  background: var(--warn); opacity: .75; transform: translateY(-50%);
}
.tick.cur { height: 3px; background: var(--ac); opacity: 1; z-index: 1; }

/* resize & spinner */
.resize { position: absolute; top: 0; right: -3px; bottom: 0; width: 6px; cursor: col-resize; z-index: 6; }
.resize:hover { background: color-mix(in srgb, var(--ac) 40%, transparent); }
.spinner {
  width: 10px; height: 10px; flex: none; border-radius: 50%;
  border: 1.5px solid var(--bd); border-top-color: var(--ac);
  animation: spin .7s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }
</style>
