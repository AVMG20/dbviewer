<script setup lang="ts">
// Command palette (shell agent), Cmd+K: fuzzy jump to tables / views / columns,
// connections and saved queries of the active project, plus commands.
// Uses SchemaService.SearchObjects when implemented, otherwise searches the cached
// SchemaIndex of the connected connections locally.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { kbd } from "@/lib/kbd";
import { apiError, projectApi, schemaApi, type SavedQuery, type SearchHit } from "@/api";
import { useProjectsStore } from "@/stores/projects";
import { useTabsStore } from "@/stores/tabs";
import { useUiStore } from "@/stores/ui";
import Icon from "@/components/common/Icon.vue";
import { fuzzy, highlightSegments, matchTable, normQuery } from "@/components/common/fuzzy";
import { cache, isViewKind, loadIndex } from "@/components/sidebar/schemaCache";
import { recent } from "@/lib/recent";

const projects = useProjectsStore();
const tabs = useTabsStore();
const ui = useUiStore();

// SearchObjects support is probed once per app run (null = unknown).
let remoteOk: boolean | null = null;

type Seg = { t: string; m: boolean };
interface Item {
  key: string;
  kind: "command" | "conn" | "table" | "view" | "column" | "saved";
  segs: Seg[];
  hint?: Seg[];
  connId?: string;
  right?: string; // dim text on the right when no connection
  score: number;
  run: (alt: boolean) => void;
  /** keep the palette open after running (e.g. connect, then search again) */
  keepOpen?: boolean;
  /** do not move focus to the active view afterwards */
  noFocus?: boolean;
  /** section header shown above the first item of a group (empty query only) */
  group?: string;
}

const accent = computed(() => {
  const c = tabs.active ? projects.getConnection(tabs.active.connId)?.color : undefined;
  return c && c !== "none" ? c : undefined;
});
const q = ref("");
const idx = ref(0);
const input = ref<HTMLInputElement | null>(null);
const listEl = ref<HTMLElement | null>(null);
const saved = ref<SavedQuery[]>([]);
const remote = ref<SearchHit[] | null>(null); // SearchObjects results (null = use local)


function close() {
  ui.closePalette();
}

// ---- commands ----------------------------------------------------------------
const activeConnId = () => tabs.active?.connId ?? projects.projectConnections[0]?.id;
interface Cmd { id: string; label: string; hint?: string; noFocus?: boolean; run: () => void }
const commands = computed<Cmd[]>(() => [
  {
    id: "console", label: "New SQL console", hint: kbd("⌘T"),
    run: () => {
      const id = activeConnId();
      if (id) tabs.openConsole(id);
      else ui.toast("Create a connection first", "info");
    },
  },
  { id: "newconn", label: "New connection…", noFocus: true, run: () => ui.openModal("connection", { projectId: projects.activeProjectId }) },
  { id: "theme", label: `Cycle theme (system / dark / light)`, run: () => ui.cycleTheme() },
  {
    id: "refresh", label: "Refresh schema",
    run: () => {
      const ids = tabs.active ? [tabs.active.connId] : projects.projectConnections.map((c) => c.id);
      ids.forEach((id) => ui.refreshConnection(id));
      ui.toast("Refreshing schema…", "info", 1500);
    },
  },
  { id: "sidebar", label: "Toggle sidebar", run: () => ui.toggleSidebar() },
  { id: "focus", label: "Focus sidebar (speed search)", noFocus: true, run: () => ui.focusSidebar() },
  {
    id: "find", label: "Find in database…", hint: kbd("⇧⌘F"), noFocus: true,
    run: () => ui.openModal("findInDatabase", { connId: tabs.active?.connId ?? "" }),
  },
  {
    id: "closetab", label: "Close tab", hint: kbd("⌘W"),
    run: () => {
      if (tabs.activeId) void tabs.close(tabs.activeId);
    },
  },
  { id: "settings", label: "Settings…", hint: kbd("⌘,"), noFocus: true, run: () => ui.openModal("settings") },
  { id: "shortcuts", label: "Keyboard shortcuts", hint: kbd("⌘/"), noFocus: true, run: () => ui.openModal("shortcuts") },
  ...projects.projects
    .filter((p) => p.id !== projects.activeProjectId)
    .map((p) => ({ id: `project:${p.id}`, label: `Switch to project: ${p.name}`, noFocus: true, run: () => void projects.setActiveProject(p.id) })),
]);

// ---- items ---------------------------------------------------------------------
function connRight(connId: string) {
  return projects.getConnection(connId)?.name ?? "";
}

/** Opens a table (or its structure) and shows it in the sidebar; a column hit also selects that column in the tree and the grid. */
function openTableItem(connId: string, schema: string, table: string, alt: boolean, column?: string) {
  if (alt) tabs.openStructure(connId, schema, table, { focus: true });
  else tabs.openTable(connId, schema, table, { preview: true, focus: true });
  ui.revealInSidebar(connId, schema, table, column || undefined);
  if (column && !alt) ui.focusColumn(connId, schema, table, column);
}

function localTableItems(nq: string): Item[] {
  const out: Item[] = [];
  for (const c of projects.projectConnections) {
    for (const key of Object.keys(cache.index)) {
      if (!key.startsWith(c.id + "\u0000")) continue;
      const schema = key.slice(c.id.length + 1);
      for (const t of cache.index[key]) {
        const m = matchTable(nq, t);
        if (!m) continue;
        out.push({
          key: `${c.id}:${schema}:${t.name}`, kind: m.hint ? "column" : isViewKind(t.kind) ? "view" : "table", connId: c.id,
          score: m.score, segs: m.segs, hint: m.hint,
          run: (alt) => openTableItem(c.id, schema, t.name, alt, m.column),
        });
      }
    }
  }
  return out;
}

function remoteItems(hits: SearchHit[]): Item[] {
  return hits.map((h, i) => {
    const label = h.label || h.table;
    const pos = h.positions ?? [];
    const segs = highlightSegments(label, pos);
    const kind = h.kind === "saved_query" ? "saved" : h.kind === "column" ? "column" : h.kind === "table" ? "table" : "view";
    return {
      key: `r${i}:${h.connId}:${h.schema}:${h.table}:${h.column}:${h.savedId}`,
      kind, connId: h.connId, score: h.score, segs,
      run: (alt: boolean) => {
        if (h.kind === "saved_query") {
          const sq = saved.value.find((s) => s.id === h.savedId);
          tabs.openConsole(h.connId || activeConnId() || "", sq?.sql ?? "");
        } else openTableItem(h.connId, h.schema, h.table, alt, h.kind === "column" ? h.column : undefined);
      },
    };
  });
}

/** Project connections that are not open (their tables are not searchable yet). */
const unconnected = computed(() => projects.projectConnections.filter((c) => !projects.statuses[c.id]?.connected));

const items = computed<Item[]>(() => {
  const nq = normQuery(q.value.replace(/^>/, ""));
  const commandsOnly = q.value.startsWith(">");
  const out: Item[] = [];

  if (!nq && !commandsOnly) {
    recent.value
      .filter((r) => projects.getConnection(r.connId) && !(tabs.active?.connId === r.connId && tabs.active.table === r.table && (tabs.active.schema ?? "") === r.schema))
      .slice(0, 6)
      .forEach((r, i) => {
        out.push({
          key: `recent:${r.kind}:${r.connId}:${r.schema}:${r.table}`, kind: "table", connId: r.connId, score: 200000 - i, group: "Recent",
          segs: [{ t: r.table, m: false }], hint: r.kind === "structure" ? [{ t: "structure", m: false }] : undefined,
          run: (alt) => openTableItem(r.connId, r.schema, r.table, alt || r.kind === "structure"),
        });
      });
  }

  for (const c of commands.value) {
    const h = nq ? fuzzy(nq, c.label) : { score: 0, positions: [] as number[] };
    if (!h) continue;
    out.push({
      key: `cmd:${c.id}`, kind: "command", score: nq ? h.score + 5000 : 100000, segs: highlightSegments(c.label, h.positions),
      right: c.hint, run: c.run, noFocus: c.noFocus, group: "Commands",
    });
  }
  if (commandsOnly) return out.sort((a, b) => b.score - a.score).slice(0, 60);

  for (const c of projects.projectConnections) {
    const h = nq ? fuzzy(nq, c.name) : { score: 0, positions: [] as number[] };
    if (!h) continue;
    out.push({
      key: `conn:${c.id}`, kind: "conn", connId: c.id, score: nq ? h.score + 9000 : 50000, segs: highlightSegments(c.name, h.positions),
      noFocus: true, group: "Connections",
      run: () => {
        ui.revealInSidebar(c.id);
        ui.focusSidebar();
      },
    });
  }
  // Connections that are not open cannot be searched: offer to connect without leaving the palette.
  if (nq) {
    for (const c of unconnected.value) {
      out.push({
        key: `connect:${c.id}`, kind: "conn", connId: c.id, score: -1000 - out.length, keepOpen: true,
        segs: [{ t: `Connect to ${c.name} to search its tables`, m: false }],
        right: "↵",
        run: async () => {
          if (await projects.ensureConnected(c.id)) {
            const st = projects.statuses[c.id];
            await loadIndex(c.id, st?.defaultSchema || "").catch(() => {});
          }
        },
      });
    }
  }
  for (const s of saved.value) {
    const h = nq ? fuzzy(nq, s.name) : null;
    if (nq && !h) continue;
    if (!nq) continue; // not listed on empty query
    out.push({
      key: `sq:${s.id}`, kind: "saved", connId: s.connId || undefined, score: 9500 + (h?.score ?? 0), segs: highlightSegments(s.name, h?.positions ?? []),
      right: s.connId ? undefined : "saved query",
      run: () => {
        const id = s.connId || activeConnId();
        if (id) tabs.openConsole(id, s.sql);
        else ui.toast("Create a connection first", "info");
      },
    });
  }
  if (nq) {
    const objs = remote.value ? remoteItems(remote.value.filter((h) => h.kind !== "saved_query")) : localTableItems(nq);
    out.push(...objs);
  }
  out.sort((a, b) => b.score - a.score);
  return out.slice(0, 80);
});

watch(items, () => {
  if (idx.value >= items.value.length) idx.value = Math.max(0, items.value.length - 1);
});
watch(q, () => {
  idx.value = 0;
  scheduleRemote();
});

// ---- backend search (when available) -------------------------------------------
let timer: ReturnType<typeof setTimeout> | undefined;
let seq = 0;
function scheduleRemote() {
  clearTimeout(timer);
  if (remoteOk === false || !q.value.trim() || q.value.startsWith(">")) {
    remote.value = null;
    return;
  }
  const my = ++seq;
  timer = setTimeout(async () => {
    try {
      const hits = await schemaApi.SearchObjects(projects.activeProjectId, q.value.trim(), 50);
      if (my !== seq) return;
      remoteOk = true;
      remote.value = hits;
    } catch (e) {
      if (my !== seq) return;
      if (apiError(e).code === "not_implemented") remoteOk = false;
      remote.value = null;
    }
  }, 60);
}

onMounted(async () => {
  input.value?.focus();
  // Prime the local index of connected connections (default schema).
  for (const c of projects.projectConnections) {
    const st = projects.statuses[c.id];
    if (st?.connected) loadIndex(c.id, st.defaultSchema || "").catch(() => {});
  }
  // also key by "" default schema alias: normalise to the real name when known
  try {
    saved.value = await projectApi.SavedQueries(projects.activeProjectId);
  } catch {
    /* ignore */
  }
});

// ---- keyboard -------------------------------------------------------------------
const prevFocus = document.activeElement;
let picked = false;
function run(i = idx.value, alt = false) {
  const it = items.value[i];
  if (!it) return;
  if (it.keepOpen) {
    void it.run(alt);
    input.value?.focus();
    return;
  }
  picked = true;
  close();
  nextTick(() => {
    it.run(alt);
    if (!it.noFocus) setTimeout(() => ui.focusActiveView(), 80);
  });
}
onBeforeUnmount(() => {
  if (!picked) ui.restoreFocus(prevFocus);
});
function scrollActive() {
  nextTick(() => listEl.value?.querySelector<HTMLElement>(".it.act")?.scrollIntoView({ block: "nearest" }));
}
function onKey(e: KeyboardEvent) {
  const n = items.value.length;
  if (e.key === "ArrowDown" || (e.ctrlKey && e.key === "n")) {
    e.preventDefault();
    idx.value = n ? (idx.value + 1) % n : 0;
    scrollActive();
  } else if (e.key === "ArrowUp" || (e.ctrlKey && e.key === "p")) {
    e.preventDefault();
    idx.value = n ? (idx.value - 1 + n) % n : 0;
    scrollActive();
  } else if (e.key === "Enter") {
    e.preventDefault();
    run(idx.value, e.metaKey || e.ctrlKey || e.shiftKey);
  } else if (e.key === "Escape") {
    e.preventDefault();
    e.stopPropagation();
    close();
  }
}

const kindIcon = { command: "chev", conn: "db", table: "table", view: "view", column: "columns", saved: "query" } as const;
const kindLabel = { command: "", conn: "connection", table: "", view: "view", column: "", saved: "saved query" } as const;
</script>

<template>
  <Teleport to="body">
    <div class="fixed inset-0 flex items-start justify-center pt-[9vh]" :style="{ zIndex: 'var(--z-palette)', background: 'var(--scrim)', backdropFilter: 'blur(2px)' }" :data-accent="accent" @mousedown.self="close">
      <div class="no-drag w-[600px] max-w-[94vw] overflow-hidden rounded-[14px] border border-bd2 bg-pop" :style="{ boxShadow: '0 20px 60px var(--shadow)' }" role="dialog">
        <input
          ref="input"
          v-model="q"
          class="h-9 w-full border-0 border-b border-bd bg-transparent px-3.5 text-[14px] outline-none"
          style="user-select: text"
          placeholder="Jump to table, column, connection, saved query…  (> for commands)"
          spellcheck="false"
          autocomplete="off"
          @keydown="onKey"
        />
        <div ref="listEl" class="max-h-[340px] overflow-auto py-1">
          <template v-for="(it, i) in items" :key="it.key">
          <div v-if="!q.trim() && it.group && it.group !== items[i - 1]?.group" class="grp">{{ it.group }}</div>
          <div
            class="it flex h-[26px] items-center gap-2 px-3.5"
            :class="{ act: i === idx }"
            :style="{ '--conn': it.connId ? projects.connColor(it.connId) : undefined }"
            @mousemove="idx = i"
            @click="run(i, false)"
          >
            <Icon :name="kindIcon[it.kind]" class="dim" />
            <span class="mono truncate" :class="{ '!font-sans': it.kind === 'command' || it.kind === 'conn' || it.kind === 'saved' }">
              <template v-for="(s, j) in it.segs" :key="j"><span :class="{ m: s.m }">{{ s.t }}</span></template>
            </span>
            <span v-if="it.hint" class="mono truncate text-[11px] text-dim">
              · <template v-for="(s, j) in it.hint" :key="j"><span :class="{ m: s.m }">{{ s.t }}</span></template>
            </span>
            <span v-if="kindLabel[it.kind]" class="badge">{{ kindLabel[it.kind] }}</span>
            <span class="ml-auto flex flex-none items-center gap-1.5 text-dim">
              <template v-if="it.connId && it.kind !== 'conn'">
                <span class="conn-dot" />{{ connRight(it.connId) }}
              </template>
              <kbd v-else-if="it.right">{{ it.right }}</kbd>
            </span>
          </div>
          </template>
          <div v-if="!items.length" class="dim px-3.5 py-3">No results.</div>
          <div v-else-if="q.trim() && unconnected.length && !q.startsWith('>')" class="dim border-t border-bd px-3.5 py-1.5 text-[11px]">
            {{ unconnected.length }} connection{{ unconnected.length === 1 ? "" : "s" }} not connected: their tables are not searched until you connect.
          </div>
        </div>
        <div class="flex items-center gap-3 border-t border-bd px-3.5 py-1 text-[11px] text-dim">
          <span><kbd>↑↓</kbd> navigate</span>
          <span><kbd>↵</kbd> open</span>
          <span><kbd>{{ kbd("⌘↵") }}</kbd> structure</span>
          <span><kbd>esc</kbd> close</span>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.it { margin: 2px 6px; padding-left: 10px !important; padding-right: 10px !important; border-radius: var(--r); }
.it.act { background: var(--selbg); }
.grp { padding: 6px 16px 2px; font-size: 10px; text-transform: uppercase; letter-spacing: .05em; color: var(--dim); }
.m { color: var(--ac); font-weight: 700; }
</style>
