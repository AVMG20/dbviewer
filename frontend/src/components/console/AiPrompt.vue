<script setup lang="ts">
// Inline AI prompt (⌘\ in the SQL console), rendered into the block widget of an inline edit
// session (aiInline.ts) via <Teleport> from SqlConsole. Generates / changes SQL with the local
// model (lib/ai.ts), streams it into the editor as a preview, then Accept (Tab / ↵ on an empty
// input), Accept & run (⌘↵), Discard (Esc) or Retry. A new instruction refines the preview.
// With a `task` (Fix with AI, Convert to …) the task runs at once over the target; the prompt
// then refines its result the same way.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { EditorView } from "@codemirror/view";
import type { AIChunk, AISQLResult } from "@/api";
import { aiError, fmtDuration, generateSql, promptHistory, rememberPrompt, warm, type AiJob } from "@/lib/ai";
import { settings } from "@/lib/settings";
import { kbd } from "@/lib/kbd";
import { isMod } from "@/lib/keys";
import { useUiStore } from "@/stores/ui";
import Icon from "@/components/common/Icon.vue";
import { acceptInline, getInline, previewInline } from "./aiInline";

export type AiMode = "change" | "new";
/** A task that runs as soon as the prompt opens (e.g. fix the failed statement). */
export interface AiTask {
  /** short label shown before the input, e.g. "Fix" or "→ PostgreSQL" */
  label: string;
  /** what it does, for the status line ("Fixing the query") */
  doing: string;
  start: (onChunk: (c: AIChunk) => void, retry: boolean) => AiJob;
}
export interface AiTarget {
  mode: AiMode;
  from: number;
  to: number;
  side: -1 | 1;
  /** text put before / after the generated SQL (new query after a statement: a blank line) */
  prefix: string;
  suffix: string;
}

const props = defineProps<{
  view: EditorView;
  connId: string;
  schema: string;
  target: AiTarget;
  /** label of the "change" mode ("Change selection" / "Change this query"); null = not available */
  changeLabel: string | null;
  task?: AiTask | null;
}>();
const emit = defineEmits<{
  mode: [m: AiMode];
  close: [];
  accepted: [r: { from: number; to: number; text: string; run: boolean }];
}>();
const ui = useUiStore();

const input = ref<HTMLInputElement | null>(null);
const text = ref("");
const phase = ref<"idle" | "running" | "done" | "error">("idle");
const thinking = ref(false);
const gotChunk = ref(false);
const elapsed = ref(0);
const warming = ref(true);
const result = ref<AISQLResult | null>(null);
const error = ref<{ message: string; settings: boolean } | null>(null);
let job: AiJob | null = null;
let timer: number | undefined;
/** SQL of the current preview (without prefix / semicolon handling) */
let lastSql: string | null = null;
/** the last request (Retry runs it again with retry = true) */
const lastRun = ref<((onChunk: (c: AIChunk) => void, retry: boolean) => AiJob) | null>(null);
let histIdx = -1;

/** Fits the generated statement into the target: whitespace and `;` like the original. */
function shape(sql: string): string {
  const core = sql.trim().replace(/;\s*$/, "");
  if (props.target.mode === "new") return props.target.prefix + core + ";" + props.target.suffix;
  const orig = getInline(props.view)?.original ?? "";
  const lead = /^\s*/.exec(orig)![0];
  const trail = /\s*$/.exec(orig)![0];
  return lead + core + (orig.trimEnd().endsWith(";") ? ";" : "") + trail;
}

async function generate(retry = false) {
  const s = getInline(props.view);
  if (!s || phase.value === "running") return;
  if (!retry) {
    const prompt = text.value.trim();
    if (!prompt) return;
    rememberPrompt(prompt);
    const req = { prompt, sql: lastSql ?? (props.target.mode === "change" ? s.original.trim() : "") };
    lastRun.value = (onChunk, r) =>
      generateSql({ connId: props.connId, schema: props.schema, prompt: req.prompt, sql: req.sql, context: props.view.state.doc.toString(), retry: r }, onChunk, { session: true });
  }
  if (lastRun.value) await run(lastRun.value, retry);
}

async function runTask(retry = false) {
  if (!props.task || phase.value === "running" || !getInline(props.view)) return;
  lastRun.value = props.task.start;
  await run(props.task.start, retry);
}

async function run(start: (onChunk: (c: AIChunk) => void, retry: boolean) => AiJob, retry: boolean) {
  const s = getInline(props.view);
  if (!s) return;
  histIdx = -1;
  const before = props.view.state.sliceDoc(s.from, s.to);
  error.value = null;
  thinking.value = false;
  gotChunk.value = false;
  phase.value = "running";
  const t0 = performance.now();
  elapsed.value = 0;
  clearInterval(timer);
  timer = window.setInterval(() => (elapsed.value = performance.now() - t0), 100);
  const my = (job = start((c) => {
    thinking.value = c.thinking;
    if (c.sql) {
      gotChunk.value = true;
      previewInline(props.view, shape(c.sql), { busy: true });
    }
  }, retry));
  try {
    const res = await my.promise;
    if (job !== my) return;
    previewInline(props.view, shape(res.sql));
    lastSql = res.sql.trim();
    result.value = res;
    phase.value = "done";
    if (!retry) text.value = "";
  } catch (e) {
    if (job !== my) return;
    if (getInline(props.view)) previewInline(props.view, before);
    const err = aiError(e);
    if (err) {
      error.value = err;
      phase.value = "error";
    } else {
      phase.value = lastSql !== null ? "done" : "idle";
    }
  } finally {
    if (job === my) {
      job = null;
      clearInterval(timer);
    }
  }
  focus();
}

function cancel() {
  job?.cancel(); // the pending generate() restores the text from before the request
}

function accept(run = false) {
  if (phase.value !== "done" || !getInline(props.view)) return;
  const r = acceptInline(props.view);
  if (r) emit("accepted", { ...r, run });
}

function onKey(e: KeyboardEvent) {
  // the prompt owns its keys: nothing reaches the console / grid handlers
  e.stopPropagation();
  if (e.isComposing) return;
  const mod = isMod(e);
  if (e.key === "Enter") {
    e.preventDefault();
    if (phase.value === "running") return;
    if (mod) {
      if (phase.value === "done" && !text.value.trim()) accept(true);
      else void generate();
    } else if (text.value.trim()) void generate();
    else accept();
  } else if (e.key === "Tab" && !e.shiftKey && !mod) {
    e.preventDefault();
    accept();
  } else if (e.key === "Escape") {
    e.preventDefault();
    if (phase.value === "running") cancel();
    else emit("close");
  } else if (mod && (e.key === "\\" || e.code === "Backslash")) {
    e.preventDefault();
    if (phase.value === "running") cancel();
    emit("close");
  } else if ((e.key === "ArrowUp" || e.key === "ArrowDown") && !mod && !e.shiftKey && !e.altKey) {
    const hist = promptHistory();
    const browsing = histIdx >= 0 && text.value === hist[hist.length - 1 - histIdx];
    if (!hist.length || (text.value && !browsing)) return;
    e.preventDefault();
    if (!browsing) histIdx = -1;
    histIdx = e.key === "ArrowUp" ? Math.min(hist.length - 1, histIdx + 1) : histIdx - 1;
    text.value = histIdx >= 0 ? hist[hist.length - 1 - histIdx] : "";
  }
}

function setMode(m: AiMode) {
  if (m === props.target.mode || phase.value === "running") return;
  emit("mode", m);
}
// a new target (mode switch) starts from its original text again
watch(() => [props.target.mode, props.target.from], () => {
  lastSql = null;
  lastRun.value = null;
  result.value = null;
  if (phase.value !== "running") phase.value = error.value ? "error" : "idle";
  focus();
});

function openSettings() {
  ui.openModal("settings");
}

function focus() {
  nextTick(() => input.value?.focus({ preventScroll: true }));
}

const tablesShown = computed(() => {
  const t = result.value?.tables ?? [];
  return t.length <= 4 ? t.join(", ") : `${t.slice(0, 3).join(", ")} +${t.length - 3}`;
});
const tablesTitle = computed(() => {
  const r = result.value;
  if (!r) return "";
  const of = r.totalTables > r.tables.length ? ` (out of ${r.totalTables})` : "";
  return `Schema sent to the model${of}:\n${r.tables.join("\n")}`;
});
const placeholder = computed(() =>
  props.task && phase.value === "running"
    ? `${props.task.doing}…`
    : phase.value === "done"
    ? "Describe a change, or press ↵ to accept"
    : props.target.mode === "change"
      ? "How should the query change? e.g. only paid orders, add the customer email"
      : "Describe the query, e.g. orders from last week with the customer email",
);
const runningText = computed(() => (thinking.value ? "Thinking…" : gotChunk.value ? "Writing…" : warming.value ? "Loading model…" : "Waiting for the model…"));
const taskTitle = computed(() => (props.task ? props.task.doing : ""));

onMounted(() => {
  focus();
  if (props.task) void runTask();
  // load the model while the user types
  void warm().then((err) => {
    warming.value = false;
    if (err && (err.code === "not_connected" || err.code === "not_found") && phase.value === "idle") {
      error.value = { message: err.message, settings: true };
      phase.value = "error";
    }
  });
});
onBeforeUnmount(() => {
  clearInterval(timer);
  job?.cancel();
  job = null;
});
defineExpose({ focus, cancel, busy: () => phase.value === "running" });
</script>

<template>
  <div class="aip" :class="{ busy: phase === 'running' }" @mousedown.stop>
    <div class="row">
      <span class="spark" :class="{ spin: phase === 'running' }"><Icon name="sparkle" :size="13" /></span>
      <span v-if="task" class="tag" :title="taskTitle">{{ task.label }}</span>
      <input
        ref="input" v-model="text" class="pin" :placeholder="placeholder" spellcheck="false" autocomplete="off"
        :aria-label="placeholder" @keydown="onKey"
      />
      <div v-if="changeLabel" class="seg" role="radiogroup" aria-label="What to write">
        <button :class="{ on: target.mode === 'change' }" role="radio" :aria-checked="target.mode === 'change'" :disabled="phase === 'running'" @click="setMode('change')">{{ changeLabel }}</button>
        <button :class="{ on: target.mode === 'new' }" role="radio" :aria-checked="target.mode === 'new'" :disabled="phase === 'running'" @click="setMode('new')">New query</button>
      </div>
      <button class="btn ghost x" :title="kbd('Close (Esc)')" aria-label="Close" @click="emit('close')"><Icon name="x" :size="11" /></button>
    </div>
    <div class="st">
      <template v-if="phase === 'running'">
        <span class="dots" aria-hidden="true"><i></i><i></i><i></i></span>
        <span>{{ runningText }}</span>
        <span class="dim mono">{{ (elapsed / 1000).toFixed(1) }} s</span>
        <span class="flex-1"></span>
        <button class="btn ghost sm" @click="cancel">Stop <kbd>Esc</kbd></button>
      </template>
      <template v-else-if="phase === 'error' && error">
        <Icon name="warn" :size="12" class="err" />
        <span class="err msg selectable">{{ error.message }}</span>
        <span class="flex-1"></span>
        <button v-if="error.settings" class="btn ghost sm lnk" @click="openSettings">Open Settings</button>
        <button v-if="lastRun" class="btn ghost sm" @click="generate(true)">Retry</button>
      </template>
      <template v-else-if="phase === 'done' && result">
        <Icon name="check" :size="12" class="ok" />
        <span class="dim meta">
          {{ result.model }} · {{ fmtDuration(result.durationMs) }}<template v-if="result.loadMs > 200"> (load {{ fmtDuration(result.loadMs) }})</template>
          <template v-if="result.tables.length"> · <span :title="tablesTitle">tables: {{ tablesShown }}</span></template>
        </span>
        <span class="flex-1"></span>
        <button class="btn primary sm" :title="kbd('Keep the query (Tab, or ↵ with an empty prompt)')" @click="accept()">Accept <kbd>Tab</kbd></button>
        <button class="btn sm" :title="kbd('Keep the query and run it (⌘↵)')" @click="accept(true)">Accept &amp; run <kbd>{{ kbd("⌘↵") }}</kbd></button>
        <button class="btn ghost sm" title="Restore the original text (Esc)" @click="emit('close')">Discard <kbd>Esc</kbd></button>
        <button class="btn ghost sm" title="Ask again for a different answer" @click="generate(true)"><Icon name="refresh" :size="11" /> Retry</button>
      </template>
      <template v-else>
        <span class="dim">
          <template v-if="warming">Loading {{ settings.aiModel }}… · </template>
          <kbd>↵</kbd> {{ target.mode === "change" ? "change" : "write" }} · <kbd>↑</kbd> previous prompts · <kbd>Esc</kbd> close
        </span>
      </template>
    </div>
  </div>
</template>

<style scoped>
.aip {
  max-width: 820px; margin: 2px 0; padding: 6px 8px 6px 8px; border: 1px solid var(--bd2); border-left: 2px solid var(--ac);
  border-radius: var(--r); background: var(--bg2); box-shadow: 0 4px 16px var(--shadow); font-family: var(--sans); font-size: 12px;
  color: var(--tx); white-space: normal; line-height: 1.4; cursor: default;
}
.row { display: flex; align-items: center; gap: 6px; }
.spark { color: var(--ac); display: inline-flex; }
.tag { flex: none; padding: 0 6px; height: 18px; line-height: 18px; border-radius: 9px; font-size: 10.5px; color: var(--ac); background: color-mix(in srgb, var(--ac) 14%, transparent); white-space: nowrap; }
.spark.spin { animation: pulse 1.2s ease-in-out infinite; }
.pin {
  flex: 1; min-width: 0; height: 26px; border: 0; outline: none; background: transparent; color: var(--tx);
  font: inherit; font-size: 12.5px; caret-color: var(--tx); user-select: text; -webkit-user-select: text;
}
.pin::placeholder { color: var(--dim); opacity: .8; }
.seg { display: inline-flex; padding: 2px; gap: 2px; border: 1px solid var(--bd); border-radius: var(--r); background: var(--bg); flex: none; }
.seg button { border: 0; background: none; padding: 1px 8px; border-radius: calc(var(--r) - 3px); color: var(--dim); font-size: 11px; white-space: nowrap; }
.seg button:hover:not(:disabled) { color: var(--tx); }
.seg button.on { background: var(--bg3); color: var(--tx); box-shadow: 0 1px 2px var(--shadow); }
.x { padding: 0 4px; height: 20px; }
.st { display: flex; align-items: center; gap: 6px; min-height: 24px; margin-top: 3px; padding-top: 4px; border-top: 1px solid var(--bd); font-size: 11px; flex-wrap: wrap; }
.btn.sm { height: 22px; padding: 0 8px; font-size: 11px; }
.btn.sm kbd { font-size: 9px; }
.meta { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
.ok { color: var(--ok); }
.err { color: var(--danger); }
.msg { white-space: pre-wrap; word-break: break-word; min-width: 0; flex: 1 1 260px; user-select: text; }
.lnk { color: var(--ac); }
.dots { display: inline-flex; gap: 3px; }
.dots i { width: 4px; height: 4px; border-radius: 50%; background: var(--ac); animation: blink 1s infinite ease-in-out; }
.dots i:nth-child(2) { animation-delay: .15s; }
.dots i:nth-child(3) { animation-delay: .3s; }
@keyframes blink { 0%, 80%, 100% { opacity: .25; } 40% { opacity: 1; } }
@keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: .35; } }
</style>
