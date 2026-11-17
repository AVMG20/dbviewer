<script setup lang="ts">
// SQL console tab (data agent): CodeMirror 6 editor with schema-aware autocomplete, Cmd+Enter runs the
// statement at the cursor (queryApi.SplitStatements) or the selection, Cmd+Shift+Enter runs everything.
// Result tabs per run (read-only DataGrid), history + saved queries panel, cancel, EXPLAIN.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import {
  apiError, newId, projectApi, queryApi, runQuery,
  type Driver, type QueryHistory, type QueryResult, type SavedQuery,
} from "@/api";
import { useProjectsStore } from "@/stores/projects";
import { useTabsStore } from "@/stores/tabs";
import { useUiStore } from "@/stores/ui";
import { isMod } from "@/lib/keys";
import { makeResult } from "@/lib/emptyResult";
import { loadIndex } from "@/components/sidebar/schemaCache";
import type { Tab } from "@/types";
import DataGrid from "@/components/grid/DataGrid.vue";
import ContextMenu from "@/components/common/ContextMenu.vue";
import Icon from "@/components/common/Icon.vue";
import { COPY_FORMATS } from "@/lib/copyFormats";
import { fmtNum } from "@/lib/format";
import type { GridRow, MenuItem } from "@/lib/gridTypes";
import SqlEditor, { type SchemaTable } from "./SqlEditor.vue";

const props = defineProps<{ tab: Tab; active: boolean }>();
const projects = useProjectsStore();
const tabs = useTabsStore();
const ui = useUiStore();

const connId = computed(() => props.tab.connId);
const conn = computed(() => projects.getConnection(connId.value));
const driver = computed(() => (conn.value?.driver ?? "") as Driver | "");
const readOnly = computed(() => projects.isReadOnly(connId.value));
const schema = computed(() => props.tab.schema ?? "");
const tint = computed(() => projects.connColor(connId.value));
const hasTint = computed(() => !!conn.value && conn.value.color !== "none");

const editor = ref<InstanceType<typeof SqlEditor> | null>(null);
const sqlText = ref(props.tab.sql ?? "");
const schemaTables = shallowRef<SchemaTable[]>([]);
const editorH = ref(190);

// ---- results --------------------------------------------------------------------------
interface ResTab {
  id: string;
  title: string;
  result: QueryResult;
  rows: GridRow[];
  /** doc offset of the statement that produced it (for "go to error") */
  docOffset: number;
  /** synthetic message (client-side errors) */
  note?: string;
}
const results = ref<ResTab[]>([]);
const activeRes = ref<string | null>(null);
const running = ref(false);
const lastMs = ref(0);
/** the one rendered result grid (only the active result is mounted) */
const gridEl = ref<InstanceType<typeof DataGrid> | null>(null);
const resMenu = ref<{ x: number; y: number } | null>(null);
let runningId = "";
let userCanceled = false;
let elapsedTimer: number | undefined;
const elapsedMs = ref(0);
let inflight: { cancel: () => void } | null = null;

const activeResult = computed(() => results.value.find((r) => r.id === activeRes.value) ?? null);

function toRows(r: QueryResult): GridRow[] {
  return r.rows.map((values, i) => ({ key: String(i), values, state: "normal" as const }));
}

// ---- running ------------------------------------------------------------------------------
const WRITE_RE = /^\s*(insert|update|delete|drop|alter|create|truncate|replace|grant|revoke|rename|merge|vacuum|reindex)\b/i;
function stripComments(s: string): string {
  return s.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/(^|\n)\s*--[^\n]*/g, "$1");
}
const RO_RE = /read[- ]only|readonly|query_only|cannot execute .* in a read-only transaction/i;

function friendlyError(msg: string): string {
  if (readOnly.value && RO_RE.test(msg)) return `This connection is read-only, so writes are blocked.\n\n${msg}`;
  return msg;
}

async function statementAtCursor(): Promise<{ sql: string; offset: number } | null> {
  const st = editor.value?.getState();
  if (!st) return null;
  if (st.selected.trim()) return { sql: st.selected, offset: st.from };
  if (!st.doc.trim()) return null;
  try {
    const ranges = await queryApi.SplitStatements(driver.value as string, st.doc);
    if (ranges.length) {
      const hit =
        ranges.find((r) => st.cursor >= r.start && st.cursor <= r.end) ??
        [...ranges].reverse().find((r) => r.end <= st.cursor) ??
        ranges[0];
      return { sql: hit.sql, offset: hit.start };
    }
  } catch {
    /* fall back to the whole document */
  }
  return { sql: st.doc, offset: 0 };
}

async function runCursor() {
  const s = await statementAtCursor();
  if (s) await execute(s.sql, s.offset);
}
async function runAll() {
  const st = editor.value?.getState();
  if (st?.doc.trim()) await execute(st.doc, 0);
}

async function execute(sql: string, offset: number) {
  if (!sql.trim()) return;
  if (running.value) return ui.toast("A query is already running", "info", 1500);
  if (readOnly.value) {
    const bad = stripComments(sql).split(";").find((s) => WRITE_RE.test(s));
    if (bad) {
      ui.toast("Connection is read-only", "warning");
      const r = makeResult({ statement: bad.trim(), error: "This connection is read-only, so writes are blocked." });
      setResults([r], offset, "Blocked on the client: " + bad.trim().slice(0, 80));
      return;
    }
  }
  if (!(await projects.ensureConnected(connId.value))) return;
  // a new run clears the previous result / error panel at once and shows a running state
  results.value = [];
  activeRes.value = null;
  userCanceled = false;
  running.value = true;
  runningId = newId();
  const t0 = performance.now();
  elapsedMs.value = 0;
  elapsedTimer = window.setInterval(() => (elapsedMs.value = performance.now() - t0), 100);
  const p = runQuery({ connId: connId.value, queryId: runningId, sql, schema: schema.value, maxRows: 1000, sessionId: props.tab.id });
  inflight = p;
  try {
    const rs = await p;
    lastMs.value = Math.round(performance.now() - t0);
    setResults(rs, offset);
    const first = rs.find((r) => r.hasResultSet) ?? rs[rs.length - 1];
    const dur = rs.reduce((n, r) => n + (r.durationMs || 0), 0);
    const wasCanceled = userCanceled || rs.some((r) => r.error.startsWith("canceled"));
    const failed = rs.filter((r) => r.error && !r.error.startsWith("canceled")).length;
    if (wasCanceled) {
      ui.setStatus({ tabId: props.tab.id, durationMs: undefined, rows: undefined, total: undefined, message: "Query canceled" });
    } else if (rs.length > 1) {
      const sets = rs.filter((r) => r.hasResultSet && !r.error).length;
      ui.setStatus({
        tabId: props.tab.id, durationMs: undefined, rows: undefined, total: undefined,
        message: `${rs.length} statements · ${sets} result set${sets === 1 ? "" : "s"}${failed ? ` · ${failed} failed` : ""} · ${Math.round(dur)} ms`,
      });
    } else if (first) {
      ui.setStatus({
        tabId: props.tab.id, durationMs: dur,
        rows: first.hasResultSet ? first.rows.length : first.rowsAffected >= 0 ? first.rowsAffected : undefined,
        total: undefined,
        message: failed ? "Query failed" : undefined,
      });
    }
  } catch (e) {
    const err = apiError(e);
    if (err.code === "canceled") {
      setResults([makeResult({ statement: sql, error: "canceled: the query was canceled" })], offset);
      ui.setStatus({ tabId: props.tab.id, durationMs: undefined, rows: undefined, total: undefined, message: "Query canceled" });
    } else {
      const r = makeResult({ statement: sql, error: err.message || err.code });
      setResults([r], offset);
    }
  } finally {
    clearInterval(elapsedTimer);
    running.value = false;
    inflight = null;
    loadHistory();
  }
}

function setResults(rs: QueryResult[], offset: number, note?: string) {
  const doc = editor.value?.getState().doc ?? "";
  const tabsOut: ResTab[] = rs.map((r, i) => {
    let title: string;
    if (r.error) title = r.error.startsWith("canceled") ? "Canceled" : "Error";
    else if (r.hasResultSet) title = `Result ${i + 1}`;
    else title = r.rowsAffected >= 0 ? `OK · ${fmtNum(r.rowsAffected)} affected` : "OK";
    const st = r.statement?.trim();
    const at = st ? doc.indexOf(st, i === 0 ? offset : 0) : -1;
    return { id: newId(), title, result: r, rows: toRows(r), docOffset: at >= 0 ? at : offset, note: i === 0 ? note : undefined };
  });
  results.value = tabsOut;
  activeRes.value = (tabsOut.find((t) => t.result.error) ?? tabsOut[0])?.id ?? null;
}

async function cancel() {
  if (!running.value) return;
  userCanceled = true;
  inflight?.cancel();
  try {
    await queryApi.CancelQuery(runningId);
  } catch {
    /* ignore */
  }
}

async function explain() {
  const s = await statementAtCursor();
  if (!s) return;
  const sql = /^\s*explain\b/i.test(s.sql) ? s.sql : "EXPLAIN " + s.sql.trim().replace(/;\s*$/, "");
  await execute(sql, s.offset);
}

// ---- error position --------------------------------------------------------------------------------------
function errorLine(r: ResTab): { label: string; offset: number } | null {
  const msg = r.result.error;
  const stmt = r.result.statement ?? "";
  let m = /\bLINE (\d+)\b/.exec(msg) ?? /\bat line (\d+)\b/i.exec(msg) ?? /\bline (\d+)\b/i.exec(msg);
  if (m) {
    const line = Number(m[1]);
    const lines = stmt.split("\n");
    let off = 0;
    for (let i = 0; i < Math.min(line - 1, lines.length - 1); i++) off += lines[i].length + 1;
    return { label: `line ${line} of the statement`, offset: r.docOffset + off };
  }
  m = /\bposition (\d+)\b/i.exec(msg);
  if (m) return { label: `position ${m[1]}`, offset: r.docOffset + Math.max(0, Number(m[1]) - 1) };
  return null;
}
function goToError(r: ResTab) {
  const e = errorLine(r);
  editor.value?.goToOffset(e ? e.offset : r.docOffset);
}

// ---- history + saved ----------------------------------------------------------------------------------------
const panel = ref<"" | "history" | "saved">("");
const history = ref<QueryHistory[]>([]);
const histSearch = ref("");
const saved = ref<SavedQuery[]>([]);
const saveName = ref("");
const saving = ref(false);
const saveInput = ref<HTMLInputElement | null>(null);

async function loadHistory() {
  try {
    history.value = await queryApi.History(connId.value, histSearch.value, 150);
  } catch {
    history.value = [];
  }
}
async function loadSaved() {
  try {
    const all = await projectApi.SavedQueries(conn.value?.projectId ?? "");
    saved.value = all.filter((q) => !q.connId || q.connId === connId.value);
  } catch {
    saved.value = [];
  }
}
function openPanel(p: "history" | "saved") {
  panel.value = panel.value === p ? "" : p;
  if (panel.value === "history") loadHistory();
  if (panel.value === "saved") loadSaved();
}
let histTimer: number | undefined;
watch(histSearch, () => {
  clearTimeout(histTimer);
  histTimer = window.setTimeout(loadHistory, 250);
});
function loadSql(sql: string) {
  editor.value?.setDoc(sql);
  editor.value?.focus();
}
async function clearHistory() {
  const ok = await ui.confirm({ title: "Clear history?", message: `Delete the query history of ${conn.value?.name ?? "this connection"}?`, confirmText: "Clear", danger: true });
  if (!ok) return;
  await queryApi.ClearHistory(connId.value).catch(() => undefined);
  loadHistory();
}
async function startSave() {
  const st = editor.value?.getState();
  if (!st?.doc.trim()) return ui.toast("Nothing to save", "info", 1500);
  panel.value = "saved";
  loadSaved();
  saving.value = true;
  saveName.value = "";
  await nextTick();
  saveInput.value?.focus();
}
async function confirmSave() {
  const st = editor.value?.getState();
  const name = saveName.value.trim();
  if (!st || !name) return;
  try {
    await projectApi.SaveQuery({
      id: "", projectId: conn.value?.projectId ?? "", connId: connId.value, name,
      sql: st.selected.trim() ? st.selected : st.doc, createdAt: "", updatedAt: "",
    } as SavedQuery);
    saving.value = false;
    ui.toast("Query saved", "success", 1500);
    loadSaved();
  } catch (e) {
    ui.toast(apiError(e).message || "Could not save the query", "error");
  }
}
async function deleteSaved(q: SavedQuery) {
  await projectApi.DeleteSavedQuery(q.id).catch(() => undefined);
  loadSaved();
}

function ago(iso: string): string {
  const d = Date.now() - new Date(iso).getTime();
  if (!Number.isFinite(d)) return "";
  const m = Math.round(d / 60000);
  if (m < 1) return "just now";
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.round(h / 24)}d ago`;
}
const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();

// ---- result grid helpers ----------------------------------------------------------------------------------------

function cellMenu({ row, col }: { row: number; col: number }): MenuItem[] {
  const g = gridEl.value;
  return [
    { label: "Copy", hint: "⌘C", run: () => g?.copy("tsv") },
    { label: "Copy with headers", hint: "⇧⌘C", run: () => g?.copy("tsv_header") },
    { label: "Copy as", children: COPY_FORMATS.filter((f) => f.id !== "sql_update").map((f) => ({ label: f.label, run: () => g?.copy(f.id) })) },
    { separator: true },
    { label: "View value…", run: () => g?.viewValue(row, col) },
  ];
}
function onResReadonly() {
  ui.toast("Query results are read-only", "info", 1500);
}
function resCopyItems(): MenuItem[] {
  const g = gridEl.value;
  return COPY_FORMATS.filter((f) => f.id !== "sql_update").map((f) => ({ label: f.label, run: () => g?.copy(f.id) }));
}

// ---- layout (editor / results splitter) ----------------------------------------------------------------------------------
function startSplit(e: MouseEvent) {
  const y0 = e.clientY;
  const h0 = editorH.value;
  const move = (ev: MouseEvent) => (editorH.value = Math.max(80, Math.min(window.innerHeight - 260, h0 + ev.clientY - y0)));
  const up = () => {
    window.removeEventListener("mousemove", move);
    window.removeEventListener("mouseup", up);
  };
  window.addEventListener("mousemove", move);
  window.addEventListener("mouseup", up);
}

// ---- lifecycle -------------------------------------------------------------------------------------------------------------------
let saveTimer: number | undefined;
watch(sqlText, (v) => {
  clearTimeout(saveTimer);
  saveTimer = window.setTimeout(() => tabs.update(props.tab.id, { sql: v }), 400);
});
watch(() => props.tab.sql, (v) => {
  if (v !== undefined && v !== sqlText.value) editor.value?.setDoc(v);
});

/** The editor only takes focus when the opener asked for it (tabs.consumeFocus). */
const consumeFocus = () => (tabs as unknown as { consumeFocus?: (id: string) => boolean }).consumeFocus?.(props.tab.id) ?? false;
let wantFocus = props.active && consumeFocus();
onMounted(async () => {
  window.addEventListener("keydown", onWinKey);
  if (await projects.ensureConnected(connId.value)) {
    try {
      const idx = await loadIndex(connId.value, schema.value || projects.statuses[connId.value]?.defaultSchema || "");
      schemaTables.value = idx.map((t) => ({ name: t.name, columns: t.columns, kind: t.kind }));
    } catch {
      /* autocomplete is best effort */
    }
  }
  if (wantFocus && props.active) editor.value?.focus();
  wantFocus = false;
});
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onWinKey);
  // flush the pending (debounced) save so the last keystrokes are kept in the tab state
  clearTimeout(saveTimer);
  clearInterval(elapsedTimer);
  if (sqlText.value !== props.tab.sql) tabs.update(props.tab.id, { sql: sqlText.value });
  inflight?.cancel();
  queryApi.CloseSession(props.tab.id).catch(() => undefined);
  ui.clearStatus?.(props.tab.id);
});
watch(() => props.active, (a) => {
  if (a && consumeFocus()) nextTick(() => editor.value?.focus());
});

function onWinKey(e: KeyboardEvent) {
  if (!props.active || ui.overlayOpen) return;
  // Cmd+. cancels a running query
  if (isMod(e) && e.key === ".") {
    e.preventDefault();
    cancel();
  } else if (isMod(e) && e.key === "Enter" && !(e.target as HTMLElement | null)?.closest(".cm-editor")) {
    // run from anywhere in the console (e.g. with focus in the result grid)
    e.preventDefault();
    e.stopPropagation();
    if (e.shiftKey) void runAll();
    else void runCursor();
  }
}
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col" :style="{ '--conn': tint }">
    <div class="tb">
      <button v-if="!running" class="btn primary" title="Run statement at cursor or selection (⌘↵)" @click="runCursor">
        <Icon name="play" /> Run <kbd class="kk">⌘↵</kbd>
      </button>
      <button v-else class="btn danger" title="Cancel the running query (⌘.)" @click="cancel"><Icon name="stop" /> Cancel</button>
      <button class="btn" :disabled="running" title="Run everything (⌘⇧↵)" @click="runAll"><Icon name="play" /> Run all <kbd>⌘⇧↵</kbd></button>
      <button class="btn ghost" :disabled="running" title="EXPLAIN the statement at the cursor" @click="explain"><Icon name="explain" /> Explain</button>
      <span class="sep"></span>
      <button class="btn ghost" title="Save query (⌘S)" @click="startSave"><Icon name="bookmark" /> Save</button>
      <button class="btn ghost" :class="{ on: panel === 'saved' }" @click="openPanel('saved')">Saved</button>
      <button class="btn ghost" :class="{ on: panel === 'history' }" @click="openPanel('history')"><Icon name="history" /> History</button>
      <span class="flex-1"></span>
      <span v-if="running" class="dim">Running…</span>
      <span v-else-if="activeResult && !activeResult.result.error" class="dim mono">
        {{ activeResult.result.hasResultSet ? `${fmtNum(activeResult.result.rows.length)} row${activeResult.result.rows.length === 1 ? "" : "s"}` : "" }}
        · {{ Math.round(activeResult.result.durationMs) }} ms
      </span>
      <span class="dim mono">{{ conn?.name }}</span>
      <span v-if="readOnly" class="pill ro" title="This connection is read-only: writes are blocked."><Icon name="lock" :size="10" /> READ-ONLY</span>
    </div>

    <div class="flex min-h-0 flex-1">
      <div class="flex min-w-0 flex-1 flex-col">
        <div :style="{ height: editorH + 'px' }" class="flex-none">
          <SqlEditor
            ref="editor" v-model="sqlText" :driver="driver" :tables="schemaTables"
            placeholder-text="SELECT * FROM … — ⌘↵ runs the statement at the cursor"
            @run="runCursor" @run-all="runAll" @save-query="startSave"
          />
        </div>
        <div class="split" @mousedown.prevent="startSplit"></div>

        <!-- results -->
        <div v-if="results.length" class="rtabs">
          <div
            v-for="r in results" :key="r.id" class="rtab" :class="{ act: r.id === activeRes, err: !!r.result.error }"
            :title="r.result.statement" @click="activeRes = r.id"
          >
            <Icon v-if="r.result.error" name="warn" :size="11" />
            {{ r.title }}<span v-if="r.result.hasResultSet && !r.result.error" class="dim mono"> {{ fmtNum(r.result.rows.length) }}</span>
          </div>
          <span class="flex-1"></span>
          <button v-if="activeResult?.result.hasResultSet" class="btn ghost" @click="resMenu = { x: $event.clientX, y: $event.clientY }"><Icon name="copy" /> Copy as</button>
        </div>
        <div class="relative flex min-h-0 flex-1 flex-col">
          <div v-if="!results.length && !running" class="dim p-4">Press <kbd>⌘↵</kbd> to run the statement under the cursor, <kbd>⌘⇧↵</kbd> to run everything.</div>
          <div v-if="running" class="runbox">
            <span class="dim">Running… {{ (elapsedMs / 1000).toFixed(1) }} s</span>
            <button class="btn danger" @click="cancel"><Icon name="stop" /> Cancel</button>
          </div>
          <template v-if="activeResult">
            <div v-if="activeResult.result.error" class="rerr selectable">
              <div class="flex items-center gap-2">
                <b>{{ activeResult.result.error.startsWith("canceled") ? "Query canceled" : "Query failed" }}</b>
                <button v-if="errorLine(activeResult)" class="btn ghost lnk" @click="goToError(activeResult)">Go to {{ errorLine(activeResult)!.label }}</button>
                <button v-else class="btn ghost lnk" @click="goToError(activeResult)">Go to statement</button>
              </div>
              <pre class="mono">{{ friendlyError(activeResult.result.error) }}</pre>
              <pre v-if="activeResult.note" class="mono dim">{{ activeResult.note }}</pre>
              <pre v-else class="mono dim stmt">{{ oneLine(activeResult.result.statement).slice(0, 300) }}</pre>
            </div>
            <div v-else-if="!activeResult.result.hasResultSet" class="rok">
              <Icon name="check" class="ok" />
              <span>
                {{ activeResult.result.rowsAffected >= 0 ? `${fmtNum(activeResult.result.rowsAffected)} row${activeResult.result.rowsAffected === 1 ? "" : "s"} affected` : "Statement executed" }}
                <span class="dim"> · {{ Math.round(activeResult.result.durationMs) }} ms</span>
              </span>
              <pre class="mono dim stmt">{{ oneLine(activeResult.result.statement).slice(0, 300) }}</pre>
            </div>
            <template v-else>
              <DataGrid
                :key="activeResult.id" ref="gridEl"
                :columns="activeResult.result.columns" :rows="activeResult.rows" :editable="false"
                :copy-context="{ driver, schema, table: '' }" :context-menu="cellMenu"
                empty-text="The statement returned no rows" @edit-blocked="onResReadonly"
              />
              <div v-if="activeResult.result.truncated || activeResult.result.hasMore" class="note">
                Showing the first {{ fmtNum(activeResult.result.rows.length) }} rows. Add a LIMIT / OFFSET, or open the table to page through all rows.
              </div>
            </template>
          </template>
        </div>
      </div>

      <!-- history / saved panel -->
      <aside v-if="panel" class="side">
        <template v-if="panel === 'history'">
          <div class="side-h">
            <span>History</span><span class="flex-1"></span>
            <button class="btn ghost" title="Clear history" @click="clearHistory"><Icon name="trash" /></button>
          </div>
          <div class="p-2"><input v-model="histSearch" class="input" style="width: 100%" placeholder="Search history" /></div>
          <div class="side-list">
            <div v-for="h in history" :key="h.id" class="hi" :class="{ bad: !!h.error }" :title="h.sql" @click="loadSql(h.sql)">
              <div class="mono truncate">{{ oneLine(h.sql) }}</div>
              <div class="dim sub">
                {{ ago(h.ranAt) }} · {{ Math.round(h.durationMs) }} ms<template v-if="h.error"> · failed</template><template v-else-if="h.rowCount >= 0"> · {{ fmtNum(h.rowCount) }} rows</template>
              </div>
            </div>
            <div v-if="!history.length" class="dim p-3">No history yet.</div>
          </div>
        </template>
        <template v-else>
          <div class="side-h"><span>Saved queries</span></div>
          <form v-if="saving" class="p-2 flex gap-1" @submit.prevent="confirmSave">
            <input ref="saveInput" v-model="saveName" class="input" style="flex: 1" placeholder="Name" @keydown.esc.stop="saving = false" />
            <button class="btn primary" type="submit" :disabled="!saveName.trim()">Save</button>
          </form>
          <div class="side-list">
            <div v-for="q in saved" :key="q.id" class="hi" :title="q.sql" @click="loadSql(q.sql)">
              <div class="flex items-center gap-1">
                <b class="truncate flex-1">{{ q.name }}</b>
                <button class="btn ghost x" title="Delete" @click.stop="deleteSaved(q)"><Icon name="x" :size="11" /></button>
              </div>
              <div class="mono dim truncate sub">{{ oneLine(q.sql) }}</div>
            </div>
            <div v-if="!saved.length" class="dim p-3">No saved queries. Use Save (⌘S) to keep the current one.</div>
          </div>
        </template>
      </aside>
    </div>
    <ContextMenu v-if="resMenu" :x="resMenu.x" :y="resMenu.y" :items="resCopyItems()" @close="resMenu = null" />
  </div>
</template>

<style scoped>
.tb { display: flex; align-items: center; gap: 6px; padding: 4px 10px; border-bottom: 1px solid var(--bd); flex: none; flex-wrap: wrap; }
.sep { width: 1px; height: 16px; background: var(--bd); }
.btn.on { background: var(--hov); }
.pill.ro { display: inline-flex; align-items: center; gap: 4px; }
.split { height: 5px; flex: none; cursor: row-resize; border-top: 1px solid var(--bd); background: var(--bg2); }
.split:hover { background: var(--ac); opacity: .5; }
.runbox { display: flex; align-items: center; gap: 12px; padding: 14px; }
.rtabs { display: flex; align-items: center; background: var(--bg2); border-bottom: 1px solid var(--bd); height: 28px; flex: none; padding-right: 6px; overflow: hidden; }
.rtab { display: flex; align-items: center; gap: 5px; padding: 0 12px; height: 100%; border-right: 1px solid var(--bd); color: var(--dim); cursor: default; white-space: nowrap; border-top: 2px solid transparent; }
.rtab.act { background: var(--bg); color: var(--tx); border-top-color: var(--ac); }
.rtab.err { color: var(--danger); }
.rerr { padding: 12px 14px; display: flex; flex-direction: column; gap: 6px; color: var(--danger); overflow: auto; }
.rerr pre { margin: 0; white-space: pre-wrap; word-break: break-word; color: var(--tx); font-size: 12px; user-select: text; }
.rerr pre.dim, .rok pre.dim { color: var(--dim); }
.rerr pre.stmt, .rok pre.stmt { font-size: 11px; }
.lnk { color: var(--ac); font-size: 11px; }
.rok { padding: 14px; display: flex; flex-direction: column; gap: 4px; }
.rok :deep(.ok) { color: var(--ok); }
.rok pre { margin: 0; white-space: pre-wrap; }
.note { padding: 3px 10px; font-size: 11px; color: var(--warn); background: var(--bg2); border-top: 1px solid var(--bd); }
.side { width: 270px; flex: none; border-left: 1px solid var(--bd); background: var(--bg2); display: flex; flex-direction: column; min-height: 0; }
.side-h { display: flex; align-items: center; padding: 4px 6px 4px 10px; border-bottom: 1px solid var(--bd); font-size: 11px; color: var(--dim); text-transform: uppercase; letter-spacing: .05em; height: 30px; }
.side-list { overflow: auto; flex: 1; }
.hi { padding: 5px 10px; border-bottom: 1px solid var(--bd); font-size: 11px; cursor: default; }
.hi:hover { background: var(--hov); }
.hi.bad .sub { color: var(--danger); }
.hi .sub { font-size: 10px; margin-top: 1px; }
.x { padding: 0 3px; height: 18px; opacity: .6; }
</style>
