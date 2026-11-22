<script setup lang="ts">
// SQL console tab (data agent): CodeMirror 6 editor with schema-aware autocomplete, Cmd+Enter runs the
// statement at the cursor (queryApi.SplitStatements) or the selection, Cmd+Shift+Enter runs everything.
// Result tabs per run (read-only DataGrid), history + saved queries panel, cancel, EXPLAIN.
// Format (⌥⇧F, sql-formatter): the selection or the whole script, one undo step. Export of a result:
// ExportModal with the result's statement as SQL source ("All rows" re-runs it, streamed by the backend).
// Before running on a writable connection: UPDATE/DELETE without WHERE and (on red connections) any
// writing statement ask for one combined confirmation (classification: queryApi.SplitStatements).
// After a run the tab is named after the first referenced table unless the user renamed it.
// AI (when enabled in Settings; the AI button's menu lists everything): ⌘\ opens an inline prompt
// above the statement at the cursor / the selection that writes or changes SQL (AiPrompt.vue,
// aiInline.ts). Fix with AI (⌥⌘F, "Query failed" panel) and Convert to … stream a rewrite over the
// statement the same way. Explain with AI (⌥⌘E) and "What's slow?" (EXPLAIN results) answer in the
// side panel (AiAnswer.vue). Save (⌘S) suggests a name; after a run the tab gets a short AI title
// unless the user renamed it.
import { computed, markRaw, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import type { EditorView } from "@codemirror/view";
import { kbd } from "@/lib/kbd";
import {
  apiError, dataApi, isBinary, isLongText, newId, projectApi, queryApi, runQuery,
  type Driver, type QueryHistory, type QueryResult, type SavedQuery, type StatementRange,
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
import { COPY_FORMATS, formatCopy } from "@/lib/copyFormats";
import { fmtNum } from "@/lib/format";
import type { GridRow, MenuItem } from "@/lib/gridTypes";
import SqlEditor, { type SchemaTable } from "./SqlEditor.vue";
import AiPrompt, { type AiMode, type AiTarget, type AiTask } from "./AiPrompt.vue";
import AiAnswer, { type AiAnswerRequest } from "./AiAnswer.vue";
import { discardInline, endInline, getInline, retargetInline, startInline } from "./aiInline";
import { aiError, aiInlineReady, aiNameReady, aiOffMessage, aiReady, completeSql, inlineModel, sqlTask, textTask, warmInline, type AiJob } from "@/lib/ai";
import type { GhostOptions } from "./aiGhost";
import { cellText } from "@/lib/format";
import type { AITextResult } from "@/api";
import { settings } from "@/lib/settings";

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
const editorH = ref(Math.max(190, Math.min(330, window.innerHeight - 320)));

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
  /** first table the statement references (export file / INSERT target name) */
  table: string;
  /** result of an EXPLAIN: the explained statement ("What's slow?") */
  explainOf?: string;
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
/** between Run and the actual start: classification, row counts and the confirmation dialog */
let preparing = false;
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

/** Splits and classifies a script (backend tokenizer); [] when that fails. */
async function analyze(sql: string): Promise<StatementRange[]> {
  try {
    return await queryApi.SplitStatements(driver.value as string, sql);
  } catch {
    return [];
  }
}

// ---- safety checks (writable connections) ----------------------------------------------------
/** Data / schema changing statement (backend classification; regex fallback for an older backend). */
const isWrite = (s: StatementRange) => (typeof s.writes === "boolean" ? s.writes : WRITE_RE.test(stripComments(s.sql)));

/** Rows of the one table of a simple UPDATE/DELETE without WHERE; null if unknown (or slow). */
async function countTarget(s: StatementRange): Promise<number | null> {
  if (!s.targetTable) return null;
  const p = dataApi.CountRows({
    connId: connId.value, schema: s.targetSchema || schema.value, table: s.targetTable,
    where: "", orderBy: "", limit: 0, offset: 0, columnFilters: [], countTotal: false,
  });
  const timer = window.setTimeout(() => p.cancel(), 3000);
  try {
    return await p;
  } catch {
    return null;
  } finally {
    clearTimeout(timer);
  }
}

/**
 * One confirmation for everything risky in a run: UPDATE/DELETE without WHERE (with the table's row
 * count when it can be determined) and, on a red ("production") connection, every writing statement.
 * Read-only connections are guarded by the backend. Resolves true when the run may go ahead.
 */
async function confirmRisky(stmts: StatementRange[]): Promise<boolean> {
  if (readOnly.value) return true;
  const red = conn.value?.color === "red";
  const noWhere = stmts.filter((s) => s.noWhere);
  const risky = stmts.filter((s) => s.noWhere || (red && isWrite(s)));
  if (!risky.length) return true;
  const counts = await Promise.all(noWhere.map(countTarget));
  const lines = noWhere.map((s, i) => {
    const n = counts[i];
    const verb = s.kind === "delete" ? "deletes" : "updates";
    const on = s.targetTable || s.table ? ` on ${s.targetTable || s.table}` : "";
    const rows = n === null ? "" : ` (${fmtNum(n)} row${n === 1 ? "" : "s"})`;
    return `${s.kind.toUpperCase()}${on} has no WHERE clause: it ${verb} every row in the table${rows}.`;
  });
  if (red) {
    const w = risky.filter(isWrite).length;
    lines.push(`${conn.value?.name ?? "This"} is a production connection. ${w === 1 ? "This statement changes" : `These ${w} statements change`} data or the schema.`);
  }
  if (stmts.length > risky.length) lines.push(`${risky.length} of ${stmts.length} statements need confirmation:`);
  const single = risky.length === 1 && noWhere.length === 1;
  return ui.confirm({
    title: noWhere.length ? `${noWhere.length === 1 ? noWhere[0].kind.toUpperCase() : "UPDATE / DELETE"} without WHERE` : "Run on a production connection?",
    message: lines.join("\n"),
    sql: risky.map((s) => s.sql.trim() + ";").join("\n\n"),
    confirmText: single ? (noWhere[0].kind === "delete" ? "Delete all rows" : "Update all rows") : "Run",
    danger: true,
  });
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
  // A second Run while the first one is still being checked would ask again and, with both
  // confirmed, run the statement twice (e.g. an UPDATE without WHERE).
  if (preparing) return;
  preparing = true;
  try {
    await prepareAndRun(sql, offset);
  } finally {
    preparing = false;
  }
}
async function prepareAndRun(sql: string, offset: number) {
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
  const stmts = await analyze(sql);
  if (!(await confirmRisky(stmts))) return;
  if (running.value) return;
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
    setResults(rs, offset, undefined, stmts);
    // name the console once after the first successful query (kept when the user renamed the tab)
    if (rs.some((r) => !r.error)) {
      const table = stmts.find((s) => s.table)?.table ?? "";
      aiTitle(sql, table);
    }
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

function setResults(rs: QueryResult[], offset: number, note?: string, stmts: StatementRange[] = []) {
  const doc = editor.value?.getState().doc ?? "";
  const tabsOut: ResTab[] = rs.map((r, i) => {
    let title: string;
    if (r.error) title = r.error.startsWith("canceled") ? "Canceled" : "Error";
    else if (r.hasResultSet) title = `Result ${i + 1}`;
    else title = r.rowsAffected >= 0 ? `OK · ${fmtNum(r.rowsAffected)} affected` : "OK";
    const st = r.statement?.trim();
    const at = st ? doc.indexOf(st, i === 0 ? offset : 0) : -1;
    const table = (st && stmts.find((x) => x.sql.trim() === st)?.table) || "";
    const explainOf = st && EXPLAIN_RE.test(st) ? st.replace(EXPLAIN_RE, "").trim() : undefined;
    return { id: newId(), title, result: r, rows: toRows(r), docOffset: at >= 0 ? at : offset, note: i === 0 ? note : undefined, table, explainOf };
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

/** EXPLAIN with its options (the explained statement follows) */
const EXPLAIN_RE = /^\s*explain\s+(?:(?:query\s+plan|analyze|verbose|extended|partitions|format\s*=\s*\w+)\s+|\([^)]*\)\s*)*/i;

async function explain() {
  const s = await statementAtCursor();
  if (!s) return;
  // SQLite's plain EXPLAIN lists VM opcodes; EXPLAIN QUERY PLAN is the readable plan
  const kw = driver.value === "sqlite" ? "EXPLAIN QUERY PLAN " : "EXPLAIN ";
  const sql = /^\s*explain\b/i.test(s.sql) ? s.sql : kw + s.sql.trim().replace(/;\s*$/, "");
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
const panel = ref<"" | "history" | "saved" | "ai">("");
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
  closeAi({ focus: false });
  editor.value?.setDoc(sql);
  editor.value?.focus();
}
async function clearHistory() {
  const ok = await ui.confirm({ title: "Clear history?", message: `Delete the query history of ${conn.value?.name ?? "this connection"}?`, confirmText: "Clear", danger: true });
  if (!ok) return;
  await queryApi.ClearHistory(connId.value).catch(() => undefined);
  loadHistory();
}
/** AI name suggestion for the Save form (prefilled and selected, so typing replaces it) */
const suggesting = ref(false);
let nameJob: AiJob<AITextResult> | null = null;

async function startSave() {
  const st = editor.value?.getState();
  if (!st?.doc.trim()) return ui.toast("Nothing to save", "info", 1500);
  panel.value = "saved";
  loadSaved();
  saving.value = true;
  saveName.value = "";
  await nextTick();
  saveInput.value?.focus();
  if (aiNameReady()) void suggestName(st.selected.trim() ? st.selected : st.doc);
}
async function suggestName(sql: string) {
  nameJob?.cancel();
  const my = (nameJob = textTask("NameQuery", { connId: connId.value, sql: sql.slice(0, 4000) }));
  suggesting.value = true;
  try {
    const res = await my.promise;
    // only when the user has not started typing
    if (nameJob !== my || !saving.value || saveName.value) return;
    saveName.value = res.text;
    await nextTick();
    if (document.activeElement === saveInput.value) saveInput.value?.select();
  } catch {
    /* no suggestion */
  } finally {
    if (nameJob === my) {
      nameJob = null;
      suggesting.value = false;
    }
  }
}
function stopSuggesting() {
  nameJob?.cancel();
  nameJob = null;
  suggesting.value = false;
}
watch(saving, (v) => !v && stopSuggesting());
watch(saveName, (v) => v && nameJob && stopSuggesting());
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
    { label: "Copy", hint: kbd("⌘C"), run: () => g?.copy("tsv") },
    { label: "Copy with headers", hint: kbd("⇧⌘C"), run: () => g?.copy("tsv_header") },
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

// ---- export --------------------------------------------------------------------------------------------------------
/** cells selected in the result grid (Export "Selection" needs more than one) */
const resCells = ref(0);
watch(activeRes, () => (resCells.value = 0));
function openExport() {
  const r = activeResult.value;
  if (!r?.result.hasResultSet) return;
  ui.openModal("export", {
    connId: connId.value, schema: schema.value, table: r.table || "query_result", sql: r.result.statement,
    hasSelection: resCells.value > 1, loadedRows: r.result.rows.length, getText: exportText,
  });
}
/** Client-side export (ExportModal "Loaded rows" / "Selection"). */
async function exportText(scope: "page" | "selection", format: string, header: boolean): Promise<string | null> {
  const fmt = format === "sql" ? "sql_insert" : format === "csv" && !header ? "csv_body" : format === "tsv" ? (header ? "tsv_header" : "tsv") : (format as "csv" | "json" | "markdown");
  if (scope === "selection") return (await gridEl.value?.selectionText(fmt)) ?? null;
  const r = activeResult.value;
  if (!r) return null;
  const rows = r.result.rows;
  if (rows.some((row) => row.some((v) => (isLongText(v) || isBinary(v)) && v.truncated))) {
    throw new Error("The loaded rows contain truncated long values. Export \"All rows\" to get them in full.");
  }
  return formatCopy(fmt, {
    columns: r.result.columns, cols: r.result.columns.map((_, i) => i), rows, driver: driver.value, schema: schema.value, table: r.table || "query_result",
  });
}

// ---- format -------------------------------------------------------------------------------------------------------
/** Formats the selection, or the whole script when nothing is selected (one undo step). */
async function formatEditor() {
  if (!editor.value?.getState().doc.trim()) return;
  let fmt: typeof import("./formatSql");
  try {
    fmt = await import("./formatSql");
  } catch {
    return ui.toast("Could not load the SQL formatter", "error");
  }
  const ed = editor.value;
  if (!ed) return;
  const st = ed.getState();
  try {
    if (st.selected.trim()) {
      // continuation lines keep the indentation of the line the selection starts on
      const lineStart = st.doc.lastIndexOf("\n", st.from - 1) + 1;
      const indent = /^[ \t]*/.exec(st.doc.slice(lineStart))![0];
      const out = fmt.formatFragment(st.selected, driver.value, st.from - lineStart <= indent.length ? indent : "");
      if (out !== st.selected) ed.replaceRange(st.from, st.to, out, st.from, st.from + out.length);
    } else {
      const out = fmt.formatFragment(st.doc, driver.value);
      if (out !== st.doc) ed.replaceRange(0, st.doc.length, out, fmt.mapOffset(st.doc, out, st.cursor));
    }
  } catch (e) {
    ui.toast(`Could not format the SQL: ${e instanceof Error ? e.message.split("\n")[0] : String(e)}`, "error");
  }
  ed.focus();
}

// ---- AI (⌘\) ------------------------------------------------------------------------------------------------
const aiAvailable = computed(() => aiReady());
/** widget element of the open prompt (Teleport target), null = closed */
const aiEl = shallowRef<HTMLElement | null>(null);
const aiView = shallowRef<EditorView | null>(null);
const aiTarget = ref<AiTarget | null>(null);
const aiChangeLabel = ref<string | null>(null);
const aiPrompt = ref<InstanceType<typeof AiPrompt> | null>(null);
const aiTask = shallowRef<AiTask | null>(null);
let aiTargets: Partial<Record<AiMode, AiTarget>> = {};
let aiOpening = false;

/**
 * What the prompt works on: the selection, else the statement at the cursor ("change"), and where a
 * new query goes ("new": on an empty line at the cursor, else on new lines after the statement).
 */
async function aiTargetsAt(view: EditorView): Promise<{ targets: Partial<Record<AiMode, AiTarget>>; changeLabel: string | null }> {
  const st = view.state;
  const sel = st.selection.main;
  const doc = st.doc;
  const after = (pos: number, prefix: string): AiTarget => {
    const line = doc.lineAt(pos);
    // keep a blank line before a statement that follows directly
    const next = line.number < doc.lines ? doc.line(line.number + 1) : null;
    const suffix = next && next.text.trim() ? "\n" : "";
    return { mode: "new", from: line.to, to: line.to, side: 1, prefix, suffix };
  };
  if (!sel.empty) {
    return {
      targets: { change: { mode: "change", from: sel.from, to: sel.to, side: -1, prefix: "", suffix: "" }, new: after(sel.to, "\n\n") },
      changeLabel: "Change selection",
    };
  }
  const line = doc.lineAt(sel.head);
  if (!line.text.trim()) {
    return { targets: { new: { mode: "new", from: line.from, to: line.to, side: -1, prefix: "", suffix: "" } }, changeLabel: null };
  }
  let hit: StatementRange | undefined;
  try {
    const ranges = await queryApi.SplitStatements(driver.value as string, doc.toString());
    hit =
      ranges.find((r) => sel.head >= r.start && sel.head <= r.end) ??
      // cursor after the ";" on the statement's last line
      [...ranges].reverse().find((r) => r.end <= sel.head && doc.lineAt(r.end).number === line.number);
  } catch {
    /* no statement: new query below the line */
  }
  if (!hit) return { targets: { new: after(sel.head, "\n") }, changeLabel: null };
  const end = doc.sliceString(hit.end, hit.end + 1) === ";" ? hit.end + 1 : hit.end;
  return {
    targets: { change: { mode: "change", from: hit.start, to: hit.end, side: -1, prefix: "", suffix: "" }, new: after(end, "\n\n") },
    changeLabel: "Change this query",
  };
}

/** Opens the inline prompt; with opts (Fix, Convert) over a given range, running a task at once. */
async function openAi(opts?: { target: AiTarget; task: AiTask }) {
  if (!aiReady()) return void ui.toast(aiOffMessage(), "info", 4000);
  if (aiEl.value) {
    if (!opts) return aiPrompt.value?.focus();
    if (aiPrompt.value?.busy()) return void ui.toast("The AI is still writing; press Esc to stop it first", "info", 2500);
    closeAi({ focus: false });
  }
  const view = editor.value?.getView();
  if (!view || aiOpening) return;
  aiOpening = true;
  try {
    const { targets, changeLabel } = opts ? { targets: { change: opts.target }, changeLabel: null } : await aiTargetsAt(view);
    if (aiEl.value || !(targets.new || targets.change)) return;
    const t = (targets.change ?? targets.new)!;
    const el = document.createElement("div");
    el.className = "cm-inline-widget";
    startInline(view, { from: t.from, to: t.to, side: t.side, dom: el });
    aiTargets = targets;
    aiTask.value = opts?.task ?? null;
    aiChangeLabel.value = changeLabel;
    aiTarget.value = t;
    aiView.value = markRaw(view);
    aiEl.value = el;
  } finally {
    aiOpening = false;
  }
}

function setAiMode(m: AiMode) {
  const view = aiView.value;
  const t = aiTargets[m];
  if (!view || !t) return;
  retargetInline(view, { from: t.from, to: t.to, side: t.side });
  aiTarget.value = t;
}

/** Closes the prompt; restores the original text unless the result was accepted. */
function closeAi(opts: { restore?: boolean; focus?: boolean } = {}) {
  const view = aiView.value;
  if (!aiEl.value) return;
  aiPrompt.value?.cancel();
  if (view && getInline(view)) {
    if (opts.restore === false) endInline(view);
    else discardInline(view);
  }
  aiEl.value = null;
  aiView.value = null;
  aiTarget.value = null;
  aiTask.value = null;
  aiTargets = {};
  if (opts.focus !== false && props.active) editor.value?.focus();
}

// ---- inline AI suggestions (ghost text, aiGhost.ts) ------------------------------------------
let ghostWarned = "";
const ghostOpts: GhostOptions = {
  enabled: () => aiInlineReady() && props.active && projects.isConnected(connId.value),
  blocked: (v) => !!getInline(v),
  onFocus: () => warmInline(),
  fetch: (req) => {
    const job = completeSql({ connId: connId.value, schema: schema.value, ...req });
    const promise = job.promise.then(
      (r) => r.text,
      (e) => {
        // Ollama not running / model missing: say so once per model instead of failing silently
        const err = aiError(e);
        const key = `${inlineModel()}:${err?.message}`;
        if (err?.settings && ghostWarned !== key) {
          ghostWarned = key;
          ui.toast(`Inline suggestions: ${err.message}`, "error", 6000);
        }
        return "";
      },
    );
    return { promise, cancel: job.cancel };
  },
};

function onAiAccepted(r: { from: number; to: number; text: string; run: boolean }) {
  closeAi({ restore: false });
  if (!r.run) return;
  const sql = r.text.trim().replace(/;\s*$/, "");
  if (sql) void execute(sql, r.from + r.text.length - r.text.trimStart().length);
}

// ---- AI tasks: fix, convert (inline), explain, what's slow (side panel), tab title -------------------------
/** The failed statement of a result in the editor: its range now (the editor may have changed). */
function failedRange(r: ResTab): { from: number; to: number; sql: string } | null {
  const doc = editor.value?.getState().doc ?? "";
  let stmt = (r.result.statement ?? "").trim();
  if (!stmt) return null;
  const find = (s: string) => {
    if (doc.startsWith(s, r.docOffset)) return r.docOffset;
    const near = doc.indexOf(s, Math.max(0, r.docOffset - 200));
    return near >= 0 ? near : doc.indexOf(s);
  };
  let at = find(stmt);
  if (at < 0 && EXPLAIN_RE.test(stmt)) {
    stmt = stmt.replace(EXPLAIN_RE, "").trim();
    at = find(stmt);
  }
  return at < 0 ? null : { from: at, to: at + stmt.length, sql: stmt };
}

/** The error can be fixed by changing the statement (not canceled / blocked on a read-only connection). */
function fixable(r: ResTab | null): boolean {
  if (!r?.result.error || r.note) return false;
  const e = r.result.error;
  return !e.startsWith("canceled") && !(readOnly.value && RO_RE.test(e));
}

async function fixWithAi(r: ResTab | null = activeResult.value) {
  if (!aiReady()) return void ui.toast(aiOffMessage(), "info", 4000);
  if (!r || !fixable(r)) return void ui.toast("There is no failed query to fix", "info", 2000);
  const range = failedRange(r);
  if (!range) return void ui.toast("The failed statement is no longer in the editor", "warning", 3000);
  const error = r.result.error;
  const task: AiTask = {
    label: "Fix",
    doing: "Fixing the query",
    start: (onChunk, retry) => sqlTask("FixSQL", { connId: connId.value, schema: schema.value, sql: range.sql, error, retry }, onChunk),
  };
  await openAi({ target: { mode: "change", from: range.from, to: range.to, side: -1, prefix: "", suffix: "" }, task });
}

const DIALECTS: { id: Driver; label: string }[] = [
  { id: "mysql", label: "MySQL" },
  { id: "postgres", label: "PostgreSQL" },
  { id: "sqlite", label: "SQLite" },
];

/** The selection, else the statement at the cursor, as an inline target. */
async function selectionOrStatement(): Promise<{ from: number; to: number; sql: string } | null> {
  const st = editor.value?.getState();
  if (!st) return null;
  if (st.selected.trim()) return { from: st.from, to: st.to, sql: st.selected };
  const view = editor.value?.getView();
  if (!view) return null;
  const { targets } = await aiTargetsAt(view);
  const t = targets.change;
  return t ? { from: t.from, to: t.to, sql: view.state.sliceDoc(t.from, t.to) } : null;
}

async function convertWithAi(target: Driver) {
  if (!aiReady()) return void ui.toast(aiOffMessage(), "info", 4000);
  const r = await selectionOrStatement();
  if (!r?.sql.trim()) return void ui.toast("Put the cursor in a statement or select the SQL to convert", "info", 2500);
  const label = DIALECTS.find((d) => d.id === target)?.label ?? target;
  const task: AiTask = {
    label: `→ ${label}`,
    doing: `Converting to ${label}`,
    start: (onChunk, retry) => sqlTask("ConvertSQL", { connId: connId.value, sql: r.sql, target, retry }, onChunk),
  };
  await openAi({ target: { mode: "change", from: r.from, to: r.to, side: -1, prefix: "", suffix: "" }, task });
}

/** Side panel answer (Explain with AI / What's slow?) */
const aiAnswer = ref<AiAnswerRequest | null>(null);

async function explainWithAi() {
  if (!aiReady()) return void ui.toast(aiOffMessage(), "info", 4000);
  const s = await statementAtCursor();
  if (!s?.sql.trim()) return void ui.toast("Put the cursor in a statement or select the SQL to explain", "info", 2500);
  const sql = s.sql.trim().replace(EXPLAIN_RE, "");
  aiAnswer.value = { key: newId(), kind: "explain", connId: connId.value, schema: schema.value, sql };
  panel.value = "ai";
}

/** EXPLAIN output as TSV with a header line (at most 200 rows). */
function planText(r: QueryResult): string {
  const clean = (v: string) => v.replace(/[\t\r\n]+/g, " ").trim();
  const lines = [r.columns.map((c) => c.name).join("\t")];
  for (const row of r.rows.slice(0, 200)) lines.push(row.map((v) => (v === null ? "NULL" : clean(cellText(v)))).join("\t"));
  return lines.join("\n");
}

function whatsSlow(r: ResTab | null = activeResult.value) {
  if (!aiReady()) return void ui.toast(aiOffMessage(), "info", 4000);
  if (!r?.explainOf || !r.result.hasResultSet) return void ui.toast("Run Explain first", "info", 2000);
  aiAnswer.value = { key: newId(), kind: "plan", connId: connId.value, schema: schema.value, sql: r.explainOf, plan: planText(r.result) };
  panel.value = "ai";
}

function closeAnswer() {
  if (panel.value === "ai") panel.value = "";
  aiAnswer.value = null;
  if (props.active) editor.value?.focus();
}

/** AI menu (toolbar) */
const aiMenu = ref<{ x: number; y: number } | null>(null);
function aiMenuItems(): MenuItem[] {
  const r = activeResult.value;
  return [
    { label: "Write or change SQL…", hint: kbd("⌘\\"), run: () => void openAi() },
    { label: "Explain query", hint: kbd("⌥⌘E"), run: () => void explainWithAi() },
    { label: "Fix the failed query", hint: kbd("⌥⌘F"), disabled: !fixable(r), run: () => void fixWithAi() },
    { label: "What's slow? (EXPLAIN result)", disabled: !r?.explainOf || !r.result.hasResultSet, run: () => whatsSlow() },
    { separator: true },
    ...DIALECTS.filter((d) => d.id !== driver.value).map((d) => ({ label: `Convert to ${d.label}`, run: () => void convertWithAi(d.id) })),
  ];
}

/**
 * Names the console tab automatically once after the first successful query: uses AI
 * (local Ollama when configured, else Antigravity CLI) or falls back to the first table.
 * Subsequent runs leave the title alone; right-click the tab to "Rename with AI" again.
 */
let titleJob: AiJob<AITextResult> | null = null;
function aiTitle(sql: string, table: string) {
  if (props.tab.customTitle || props.tab.aiNamed) return;
  if (!aiNameReady()) {
    if (table) tabs.autoTitle(props.tab.id, table);
    return;
  }
  const key = sql.trim().replace(EXPLAIN_RE, "");
  if (!key || titleJob) return;
  const my = (titleJob = textTask("NameQuery", { connId: connId.value, sql: key.slice(0, 4000) }));
  my.promise
    .then((res) => {
      if (titleJob !== my) return;
      tabs.setAiTitle(props.tab.id, res.text);
    })
    .catch(() => titleJob === my && table && tabs.autoTitle(props.tab.id, table))
    .finally(() => titleJob === my && (titleJob = null));
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
  saveTimer = window.setTimeout(() => tabs.update(props.tab.id, { sql: v }), 150);
});
watch(() => props.tab.sql, (v) => {
  if (v !== undefined && v !== sqlText.value) {
    closeAi({ focus: false });
    editor.value?.setDoc(v);
  }
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
  closeAi({ focus: false });
  stopSuggesting();
  titleJob?.cancel();
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
  // Cmd+\ opens the AI prompt from anywhere in the console (the editor's keymap handles it there)
  if (isMod(e) && !e.altKey && (e.key === "\\" || e.code === "Backslash")) {
    if (e.defaultPrevented) return;
    e.preventDefault();
    void openAi();
    return;
  }
  // ⌥⌘F fix the failed query, ⌥⌘E explain the statement at the cursor (e.code: ⌥ changes e.key on macOS)
  if (isMod(e) && e.altKey && !e.shiftKey && (e.code === "KeyF" || e.code === "KeyE")) {
    if (e.defaultPrevented) return;
    e.preventDefault();
    if (e.code === "KeyF") void fixWithAi();
    else void explainWithAi();
    return;
  }
  // Esc closes the AI answer panel (unless something else used it: completion, inline prompt, inputs)
  if (e.key === "Escape" && panel.value === "ai" && !e.defaultPrevented && !(e.target as HTMLElement | null)?.closest("input, textarea, select")) {
    e.preventDefault();
    closeAnswer();
    return;
  }
  // Cmd+. cancels a running query
  if (isMod(e) && e.key === ".") {
    e.preventDefault();
    cancel();
  } else if (e.altKey && e.shiftKey && !isMod(e) && e.code === "KeyF" && !(e.target as HTMLElement | null)?.closest(".cm-editor, input, textarea, select")) {
    // ⌥⇧F formats from anywhere in the console (inside the editor its keymap handles it)
    e.preventDefault();
    void formatEditor();
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
      <button v-if="!running" class="btn primary" :title="kbd('Run statement at cursor or selection (⌘↵)')" @click="runCursor">
        <Icon name="play" /> Run <kbd class="kk">{{ kbd("⌘↵") }}</kbd>
      </button>
      <button v-else class="btn danger" :title="kbd('Cancel the running query (⌘.)')" @click="cancel"><Icon name="stop" /> Cancel</button>
      <button class="btn" :disabled="running" :title="kbd('Run everything (⌘⇧↵)')" @click="runAll"><Icon name="play" /> Run all <kbd>{{ kbd("⌘⇧↵") }}</kbd></button>
      <button class="btn ghost" :disabled="running" title="EXPLAIN the statement at the cursor" @click="explain"><Icon name="explain" /> Explain</button>
      <button class="btn ghost" :title="kbd('Format the selection, or the whole script (⌥⇧F)')" @click="formatEditor">Format</button>
      <button
        v-if="aiAvailable" class="btn ghost ai" :class="{ on: !!aiEl }"
        :title="kbd(`Write or change SQL with ${settings.aiModel} (⌘\\)`)" @click="openAi()"
      ><Icon name="sparkle" /> AI <kbd>{{ kbd("⌘\\") }}</kbd></button>
      <button
        v-if="aiAvailable" class="btn ghost aim" title="More AI actions: explain, fix, what's slow, convert"
        @click="aiMenu = { x: ($event.currentTarget as HTMLElement).getBoundingClientRect().left, y: ($event.currentTarget as HTMLElement).getBoundingClientRect().bottom + 2 }"
      ><Icon name="chevD" :size="10" /></button>
      <span class="sep"></span>
      <button class="btn ghost" :title="kbd('Save query (⌘S)')" @click="startSave"><Icon name="bookmark" /> Save</button>
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
            :placeholder-text="kbd('SELECT * FROM … — ⌘↵ runs the statement at the cursor')"
            @run="runCursor" @run-all="runAll" @save-query="startSave" @format="formatEditor"
            :ghost="ghostOpts" @ai="openAi()" @ai-escape="closeAi()"
          />
          <Teleport v-if="aiEl && aiView && aiTarget" :to="aiEl">
            <AiPrompt
              ref="aiPrompt" :view="aiView" :conn-id="connId" :schema="schema" :target="aiTarget" :change-label="aiChangeLabel" :task="aiTask"
              @mode="setAiMode" @close="closeAi()" @accepted="onAiAccepted"
            />
          </Teleport>
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
          <button
            v-if="aiAvailable && activeResult?.explainOf && activeResult.result.hasResultSet" class="btn ghost ai"
            title="Ask the AI what makes this query slow and which indexes would help (nothing is run)" @click="whatsSlow()"
          ><Icon name="sparkle" /> What's slow?</button>
          <button v-if="activeResult?.result.hasResultSet" class="btn ghost" @click="resMenu = { x: $event.clientX, y: $event.clientY }"><Icon name="copy" /> Copy as</button>
          <button v-if="activeResult?.result.hasResultSet" class="btn ghost" title="Export the result to a file (CSV, JSON, SQL INSERT, …)" @click="openExport"><Icon name="export" /> Export</button>
        </div>
        <div class="relative flex min-h-0 flex-1 flex-col">
          <div v-if="!results.length && !running" class="dim p-4">Press <kbd>{{ kbd("⌘↵") }}</kbd> to run the statement under the cursor, <kbd>{{ kbd("⌘⇧↵") }}</kbd> to run everything.</div>
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
                <button
                  v-if="aiAvailable && fixable(activeResult)" class="btn ai fix" :title="kbd('Let the AI fix the statement in the editor; you review the change (⌥⌘F)')"
                  @click="fixWithAi(activeResult)"
                ><Icon name="sparkle" /> Fix with AI <kbd>{{ kbd("⌥⌘F") }}</kbd></button>
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
                empty-text="The statement returned no rows" @edit-blocked="onResReadonly" @selection="resCells = $event.cellCount"
              />
              <div v-if="activeResult.result.truncated || activeResult.result.hasMore" class="note">
                Showing the first {{ fmtNum(activeResult.result.rows.length) }} rows. Add a LIMIT / OFFSET, or open the table to page through all rows.
              </div>
            </template>
          </template>
        </div>
      </div>

      <!-- history / saved panel -->
      <aside v-if="panel" class="side" :class="{ wide: panel === 'ai' }">
        <AiAnswer v-if="panel === 'ai' && aiAnswer" :request="aiAnswer" @close="closeAnswer" />
        <template v-else-if="panel === 'history'">
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
            <input
              ref="saveInput" v-model="saveName" class="input" style="flex: 1" :placeholder="suggesting ? 'Name (suggesting…)' : 'Name'"
              @keydown.esc.stop="saving = false"
            />
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
            <div v-if="!saved.length" class="dim p-3">No saved queries. Use Save ({{ kbd("⌘S") }}) to keep the current one.</div>
          </div>
        </template>
      </aside>
    </div>
    <ContextMenu v-if="resMenu" :x="resMenu.x" :y="resMenu.y" :items="resCopyItems()" @close="resMenu = null" />
    <ContextMenu v-if="aiMenu" :x="aiMenu.x" :y="aiMenu.y" :items="aiMenuItems()" @close="aiMenu = null" />
  </div>
</template>

<style scoped>
.tb { display: flex; align-items: center; gap: 6px; padding: 4px 10px; border-bottom: 1px solid var(--bd); flex: none; flex-wrap: wrap; }
.sep { width: 1px; height: 16px; background: var(--bd); }
.btn.on { background: var(--hov); }
.btn.ai :deep(svg) { color: var(--ac); }
.btn.aim { padding: 0 3px; margin-left: -5px; }
.btn.fix { height: 20px; padding: 0 8px; font-size: 11px; }
.btn.fix kbd { font-size: 9px; }
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
.side.wide { width: 340px; }
.side { width: 270px; flex: none; border-left: 1px solid var(--bd); background: var(--bg2); display: flex; flex-direction: column; min-height: 0; }
.side-h { display: flex; align-items: center; padding: 4px 6px 4px 10px; border-bottom: 1px solid var(--bd); font-size: 11px; color: var(--dim); text-transform: uppercase; letter-spacing: .05em; height: 30px; }
.side-list { overflow: auto; flex: 1; }
.hi { padding: 5px 10px; border-bottom: 1px solid var(--bd); font-size: 11px; cursor: default; }
.hi:hover { background: var(--hov); }
.hi.bad .sub { color: var(--danger); }
.hi .sub { font-size: 10px; margin-top: 1px; }
.x { padding: 0 3px; height: 18px; opacity: .6; }
</style>
