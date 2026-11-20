<script setup lang="ts">
// Plain-language AI answer in the SQL console's side panel: "Explain with AI" (ExplainSQL) and
// "What's slow?" for an EXPLAIN result (ExplainPlan). Streams the text; ```sql blocks get a Copy
// button (suggested indexes are never run). Re-runs when `request.key` changes.
import { computed, onBeforeUnmount, ref, watch } from "vue";
import type { AITextResult } from "@/api";
import { aiError, fmtDuration, textTask, type AiJob } from "@/lib/ai";
import { kbd } from "@/lib/kbd";
import { useUiStore } from "@/stores/ui";
import Icon from "@/components/common/Icon.vue";

export interface AiAnswerRequest {
  /** changes for every new request */
  key: string;
  kind: "explain" | "plan";
  connId: string;
  schema: string;
  sql: string;
  /** EXPLAIN output as TSV (kind "plan") */
  plan?: string;
}

const props = defineProps<{ request: AiAnswerRequest }>();
const emit = defineEmits<{ close: [] }>();
const ui = useUiStore();

const text = ref("");
const phase = ref<"running" | "done" | "error">("running");
const error = ref<{ message: string; settings: boolean } | null>(null);
const result = ref<AITextResult | null>(null);
const elapsed = ref(0);
let job: AiJob<AITextResult> | null = null;
let timer: number | undefined;

const title = computed(() => (props.request.kind === "plan" ? "What's slow?" : "Explain query"));

async function start(retry = false) {
  job?.cancel();
  const req = props.request;
  text.value = "";
  error.value = null;
  result.value = null;
  phase.value = "running";
  const t0 = performance.now();
  clearInterval(timer);
  timer = window.setInterval(() => (elapsed.value = performance.now() - t0), 100);
  const my = (job = textTask(
    req.kind === "plan" ? "ExplainPlan" : "ExplainSQL",
    { connId: req.connId, schema: req.schema, sql: req.sql, plan: req.plan ?? "", retry },
    (c) => {
      if (c.text) text.value = c.text;
    },
  ));
  try {
    const res = await my.promise;
    if (job !== my) return;
    text.value = res.text;
    result.value = res;
    phase.value = "done";
  } catch (e) {
    if (job !== my) return;
    const err = aiError(e);
    if (err) {
      error.value = err;
      phase.value = "error";
    } else phase.value = "done";
  } finally {
    if (job === my) {
      job = null;
      clearInterval(timer);
    }
  }
}

function stop() {
  job?.cancel();
}

watch(() => props.request.key, () => void start(), { immediate: true });
onBeforeUnmount(() => {
  clearInterval(timer);
  job?.cancel();
  job = null;
});

/** text split into prose and ```code``` blocks (a block still streaming counts as code) */
const parts = computed(() => {
  const out: { code: boolean; text: string }[] = [];
  const re = /```[a-zA-Z]*\n?([\s\S]*?)(?:```|$)/g;
  let pos = 0;
  const s = text.value;
  for (let m = re.exec(s); m; m = re.exec(s)) {
    if (m.index > pos) out.push({ code: false, text: s.slice(pos, m.index).trim() });
    out.push({ code: true, text: m[1].trim() });
    pos = re.lastIndex;
    if (m[0].length === 0) break;
  }
  if (pos < s.length) out.push({ code: false, text: s.slice(pos).trim() });
  return out.filter((p) => p.text);
});

async function copy(t: string) {
  try {
    await navigator.clipboard.writeText(t);
    ui.toast("Copied", "success", 1200);
  } catch {
    ui.toast("Could not copy", "error");
  }
}
function openSettings() {
  ui.openModal("settings");
}
defineExpose({ stop, busy: () => phase.value === "running" });
</script>

<template>
  <div class="aia">
    <div class="side-h">
      <Icon name="sparkle" :size="12" class="spark" /><span>{{ title }}</span><span class="flex-1"></span>
      <button class="btn ghost x" :title="kbd('Close (Esc)')" @click="emit('close')"><Icon name="x" :size="11" /></button>
    </div>
    <div class="body selectable">
      <pre class="mono dim q">{{ request.sql.replace(/\s+/g, " ").trim().slice(0, 160) }}</pre>
      <template v-for="(p, i) in parts" :key="i">
        <div v-if="p.code" class="code">
          <pre class="mono">{{ p.text }}</pre>
          <button v-if="phase !== 'running'" class="btn ghost cp" title="Copy" @click="copy(p.text)"><Icon name="copy" :size="11" /> Copy</button>
        </div>
        <p v-else class="txt">{{ p.text }}</p>
      </template>
      <div v-if="phase === 'running' && !text" class="dim wait">
        <span class="dots" aria-hidden="true"><i></i><i></i><i></i></span>
        {{ request.kind === "plan" ? "Reading the plan…" : "Reading the query…" }} {{ (elapsed / 1000).toFixed(1) }} s
      </div>
      <div v-if="phase === 'error' && error" class="err">
        <Icon name="warn" :size="12" /> <span>{{ error.message }}</span>
        <button v-if="error.settings" class="btn ghost lnk" @click="openSettings">Open Settings</button>
      </div>
    </div>
    <div class="ft">
      <template v-if="phase === 'running'">
        <span class="dim">Writing…</span><span class="flex-1"></span>
        <button class="btn ghost sm" @click="stop">Stop</button>
      </template>
      <template v-else>
        <span v-if="result" class="dim meta">{{ result.model }} · {{ fmtDuration(result.durationMs) }}</span>
        <span class="flex-1"></span>
        <button v-if="text" class="btn ghost sm" title="Copy the answer" @click="copy(text)"><Icon name="copy" :size="11" /></button>
        <button class="btn ghost sm" title="Ask again for a different answer" @click="start(true)"><Icon name="refresh" :size="11" /> Retry</button>
      </template>
    </div>
  </div>
</template>

<style scoped>
.aia { display: flex; flex-direction: column; min-height: 0; flex: 1; }
.side-h { display: flex; align-items: center; gap: 6px; padding: 4px 6px 4px 10px; border-bottom: 1px solid var(--bd); font-size: 11px; color: var(--dim); text-transform: uppercase; letter-spacing: .05em; height: 30px; flex: none; }
.spark { color: var(--ac); }
.x { padding: 0 3px; height: 18px; opacity: .6; }
.body { flex: 1; overflow: auto; padding: 8px 10px; font-size: 12px; line-height: 1.5; user-select: text; }
.q { margin: 0 0 8px; font-size: 10.5px; white-space: pre-wrap; word-break: break-word; }
.txt { margin: 0 0 8px; white-space: pre-wrap; color: var(--tx); }
.code { position: relative; margin: 0 0 8px; border: 1px solid var(--bd); border-radius: var(--r); background: var(--bg); }
.code pre { margin: 0; padding: 6px 8px; font-size: 11px; white-space: pre-wrap; word-break: break-word; }
.code .cp { position: absolute; top: 2px; right: 2px; height: 20px; padding: 0 6px; font-size: 10.5px; background: var(--bg2); }
.wait { display: flex; align-items: center; gap: 6px; font-size: 11px; }
.err { color: var(--danger); display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: 11px; }
.lnk { color: var(--ac); font-size: 11px; }
.ft { display: flex; align-items: center; gap: 6px; padding: 4px 8px; border-top: 1px solid var(--bd); font-size: 11px; flex: none; min-height: 30px; }
.btn.sm { height: 22px; padding: 0 8px; font-size: 11px; }
.meta { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
.dots { display: inline-flex; gap: 3px; }
.dots i { width: 4px; height: 4px; border-radius: 50%; background: var(--ac); animation: blink 1s infinite ease-in-out; }
.dots i:nth-child(2) { animation-delay: .15s; }
.dots i:nth-child(3) { animation-delay: .3s; }
@keyframes blink { 0%, 80%, 100% { opacity: .25; } 40% { opacity: 1; } }
</style>
