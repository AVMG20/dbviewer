<script setup lang="ts">
// Settings (shell), opened with Cmd+, or from the palette. Changes apply immediately and
// are persisted with the other ui prefs (stores/ui.ts).
import { computed, onMounted, ref, watch } from "vue";
import { useUiStore } from "@/stores/ui";
import {
  DEFAULT_AI_MODEL, DEFAULT_OLLAMA_URL, DEFAULT_SETTINGS, PAGE_SIZES, settings,
  type AIKeepAlive, type DateStyle, type Density, type NullStyle,
} from "@/lib/settings";
import { aiError, listAgyModels, listOllamaModels, pickDefaultAgyModel, pickDefaultOllamaModel } from "@/lib/ai";
import type { AIModel } from "@/api";
import type { Theme } from "@/types";
import { kbd } from "@/lib/kbd";
import Modal from "./Modal.vue";
import Icon from "./Icon.vue";

const ui = useUiStore();

const THEMES: { v: Theme; l: string }[] = [
  { v: "system", l: "System" },
  { v: "light", l: "Light" },
  { v: "dark", l: "Dark" },
];
const DENSITIES: { v: Density; l: string }[] = [
  { v: "compact", l: "Compact" },
  { v: "comfortable", l: "Comfortable" },
];
const NULLS: { v: NullStyle; l: string }[] = [
  { v: "NULL", l: "NULL" },
  { v: "null", l: "null" },
  { v: "∅", l: "∅" },
  { v: "empty", l: "Empty" },
];
const sampleDate = new Date(2026, 2, 14, 9, 30, 0);
const DATES = computed<{ v: DateStyle; l: string }[]>(() => [
  { v: "iso", l: "2026-03-14 09:30:00" },
  { v: "local", l: sampleDate.toLocaleString() },
]);
const zoomPct = computed(() => Math.round(settings.zoom * 100) + "%");

// ---- AI (Antigravity CLI) -------------------------------------------------------------
const agyModels = ref<AIModel[]>([]);
const agyState = ref<"idle" | "loading" | "ok" | "error">("idle");
const agyError = ref("");
let agySeq = 0;

async function loadAgyModels() {
  const seq = ++agySeq;
  agyState.value = "loading";
  agyError.value = "";
  try {
    const list = await listAgyModels();
    if (seq !== agySeq) return;
    agyModels.value = list;
    agyState.value = "ok";
    if (!settings.aiModel) settings.aiModel = pickDefaultAgyModel(list);
  } catch (e) {
    if (seq !== agySeq) return;
    agyModels.value = [];
    agyState.value = "error";
    agyError.value = aiError(e)?.message ?? "Could not run Antigravity CLI (agy)";
  }
}
function setAiEnabled(on: boolean) {
  settings.aiEnabled = on;
  if (on && !settings.aiModel) settings.aiModel = DEFAULT_AI_MODEL;
  if (on && agyState.value !== "ok") void loadAgyModels();
}
const agyModelOptions = computed<AIModel[]>(() => {
  const cur = settings.aiModel || DEFAULT_AI_MODEL;
  if (agyModels.value.some((m) => m.name === cur)) return agyModels.value;
  const fallbackLabel = cur === DEFAULT_AI_MODEL ? "Gemini 4 Hydrogen (Medium)" : cur;
  return [{ name: cur, label: fallbackLabel, sizeBytes: 0, parameterSize: "", family: "" }, ...agyModels.value];
});
const agyModelMissing = computed(() => agyState.value === "ok" && !!settings.aiModel && !agyModels.value.some((m) => m.name === settings.aiModel));

// ---- Local AI (Ollama: tab naming & inline autocomplete) ------------------------------
const KEEP: { v: AIKeepAlive; l: string }[] = [
  { v: "0", l: "Unload right away" },
  { v: "1m", l: "1 min" },
  { v: "5m", l: "5 min" },
  { v: "30m", l: "30 min" },
];
const ollamaModels = ref<AIModel[]>([]);
const ollamaState = ref<"idle" | "loading" | "ok" | "error">("idle");
const ollamaError = ref("");
const ollamaUrlDraft = ref(settings.ollamaUrl);
let ollamaSeq = 0;

async function loadOllamaModels() {
  const seq = ++ollamaSeq;
  ollamaState.value = "loading";
  ollamaError.value = "";
  try {
    const list = await listOllamaModels(settings.ollamaUrl);
    if (seq !== ollamaSeq) return;
    ollamaModels.value = list;
    ollamaState.value = "ok";
    if (settings.ollamaEnabled && !settings.ollamaModel) settings.ollamaModel = pickDefaultOllamaModel(list);
  } catch (e) {
    if (seq !== ollamaSeq) return;
    ollamaModels.value = [];
    ollamaState.value = "error";
    ollamaError.value = aiError(e)?.message ?? "Could not reach Ollama";
  }
}
function setOllamaEnabled(on: boolean) {
  settings.ollamaEnabled = on;
  if (on) void loadOllamaModels();
}
function commitOllamaUrl() {
  const v = ollamaUrlDraft.value.trim() || DEFAULT_OLLAMA_URL;
  ollamaUrlDraft.value = v;
  if (v === settings.ollamaUrl && ollamaState.value !== "idle") return;
  settings.ollamaUrl = v;
  void loadOllamaModels();
}
watch(() => settings.ollamaUrl, (v) => (ollamaUrlDraft.value = v));
const ollamaModelMissing = computed(() => ollamaState.value === "ok" && !!settings.ollamaModel && !ollamaModels.value.some((m) => m.name === settings.ollamaModel));
const inlineModelMissing = computed(() => ollamaState.value === "ok" && !!settings.aiInlineModel && !ollamaModels.value.some((m) => m.name === settings.aiInlineModel));
const fmtSize = (b: number) => (b >= 1e9 ? `${(b / 1e9).toFixed(1)} GB` : `${Math.round(b / 1e6)} MB`);

onMounted(() => {
  if (settings.aiEnabled) void loadAgyModels();
  if (settings.ollamaEnabled) void loadOllamaModels();
});

function reset() {
  Object.assign(settings, DEFAULT_SETTINGS);
  agyModels.value = [];
  agyState.value = "idle";
  ollamaModels.value = [];
  ollamaState.value = "idle";
  ui.setTheme("system");
}
</script>

<template>
  <Modal title="Settings" :width="600" @close="ui.closeModal()">
    <div class="grid">
      <label>Theme</label>
      <div class="seg" role="radiogroup" aria-label="Theme">
        <button v-for="t in THEMES" :key="t.v" :class="{ on: ui.theme === t.v }" role="radio" :aria-checked="ui.theme === t.v" @click="ui.setTheme(t.v)">{{ t.l }}</button>
      </div>

      <label>Zoom</label>
      <div class="flex items-center gap-1.5">
        <button class="btn" :title="kbd('Zoom out (⌘-)')" aria-label="Zoom out" @click="ui.zoomOut()">−</button>
        <span class="mono w-12 text-center">{{ zoomPct }}</span>
        <button class="btn" :title="kbd('Zoom in (⌘=)')" aria-label="Zoom in" @click="ui.zoomIn()">+</button>
        <button class="btn ghost" :disabled="settings.zoom === 1" :title="kbd('Reset (⌘0)')" @click="ui.zoomReset()">Reset</button>
      </div>

      <label>Row height</label>
      <div class="seg" role="radiogroup" aria-label="Row height">
        <button v-for="d in DENSITIES" :key="d.v" :class="{ on: settings.density === d.v }" role="radio" :aria-checked="settings.density === d.v" @click="settings.density = d.v">{{ d.l }}</button>
      </div>

      <label for="set-page">Rows per page</label>
      <div>
        <select id="set-page" v-model.number="settings.pageSize" class="input sel">
          <option v-for="n in PAGE_SIZES" :key="n" :value="n">{{ n.toLocaleString() }} rows</option>
        </select>
        <div class="help">For tables you open from now on.</div>
      </div>

      <label>Show NULL as</label>
      <div class="seg mono" role="radiogroup" aria-label="Show NULL as">
        <button v-for="n in NULLS" :key="n.v" :class="{ on: settings.nullStyle === n.v }" role="radio" :aria-checked="settings.nullStyle === n.v" @click="settings.nullStyle = n.v">{{ n.l }}</button>
      </div>

      <label>Dates</label>
      <div>
        <div class="seg mono" role="radiogroup" aria-label="Dates">
          <button v-for="d in DATES" :key="d.v" :class="{ on: settings.dateStyle === d.v }" role="radio" :aria-checked="settings.dateStyle === d.v" @click="settings.dateStyle = d.v">{{ d.l }}</button>
        </div>
        <div class="help">Display only. Editing, copying and filters always use the stored value.</div>
      </div>

      <div class="sect">AI <span class="dim">· Antigravity CLI (agy)</span></div>

      <label>Antigravity CLI</label>
      <div>
        <div class="seg" role="radiogroup" aria-label="Antigravity CLI">
          <button :class="{ on: !settings.aiEnabled }" role="radio" :aria-checked="!settings.aiEnabled" @click="setAiEnabled(false)">Off</button>
          <button :class="{ on: settings.aiEnabled }" role="radio" :aria-checked="settings.aiEnabled" @click="setAiEnabled(true)">On</button>
        </div>
        <div class="help">{{ kbd("⌘\\ writes or changes SQL, fixes failed queries, explains queries & plans, converts dialects, and filters tables.") }}</div>
      </div>

      <template v-if="settings.aiEnabled">
        <label for="set-ai-model">Model</label>
        <div>
          <div class="flex items-center gap-1.5">
            <select id="set-ai-model" v-model="settings.aiModel" class="input sel flex-1">
              <option v-for="m in agyModelOptions" :key="m.name" :value="m.name">{{ m.label && m.label !== m.name ? `${m.label} (${m.name})` : m.name }}</option>
            </select>
            <button class="btn icon" title="Reload available models from agy" aria-label="Reload Antigravity models" :disabled="agyState === 'loading'" @click="loadAgyModels"><Icon name="refresh" /></button>
          </div>
          <div v-if="agyState === 'loading'" class="help">Loading models from Antigravity CLI…</div>
          <div v-else-if="agyState === 'error'" class="help bad">{{ agyError }}</div>
          <div v-else-if="agyModelMissing" class="help bad">{{ settings.aiModel }} was not listed by <span class="mono">agy models</span>. Pick an available model above.</div>
          <div v-else-if="agyState === 'ok'" class="help">{{ agyModels.length }} model{{ agyModels.length === 1 ? "" : "s" }} available via <span class="mono">agy</span>. Prompts and relevant table/column names are sent; no data rows.</div>
          <div v-else class="help">Prompts and relevant table/column names are sent via <span class="mono">agy</span>. No data rows.</div>
        </div>
      </template>

      <div class="sect">Local AI <span class="dim">· Ollama (tab naming &amp; autocomplete)</span></div>

      <label>Local Ollama</label>
      <div>
        <div class="seg" role="radiogroup" aria-label="Local Ollama">
          <button :class="{ on: !settings.ollamaEnabled }" role="radio" :aria-checked="!settings.ollamaEnabled" @click="setOllamaEnabled(false)">Off</button>
          <button :class="{ on: settings.ollamaEnabled }" role="radio" :aria-checked="settings.ollamaEnabled" @click="setOllamaEnabled(true)">On</button>
        </div>
        <div class="help">Runs locally for fast console tab naming and inline ghost-text autocomplete while typing.</div>
      </div>

      <template v-if="settings.ollamaEnabled">
        <label for="set-ollama-url">Ollama URL</label>
        <div>
          <input id="set-ollama-url" v-model="ollamaUrlDraft" class="input w-full mono" spellcheck="false" :placeholder="DEFAULT_OLLAMA_URL" @change="commitOllamaUrl" @keydown.enter="commitOllamaUrl" />
          <div class="help">Your queries and relevant table/column names stay local at this URL.</div>
        </div>

        <label for="set-ollama-model">Model</label>
        <div>
          <div class="flex items-center gap-1.5">
            <input id="set-ollama-model" v-model.trim="settings.ollamaModel" class="input mono flex-1" list="set-ollama-models" spellcheck="false" placeholder="e.g. qwen3:4b-instruct" />
            <datalist id="set-ollama-models">
              <option v-for="m in ollamaModels" :key="m.name" :value="m.name">{{ [m.parameterSize, fmtSize(m.sizeBytes)].filter(Boolean).join(" · ") }}</option>
            </datalist>
            <button class="btn icon" title="Reload installed Ollama models" aria-label="Reload Ollama models" :disabled="ollamaState === 'loading'" @click="loadOllamaModels"><Icon name="refresh" /></button>
          </div>
          <div v-if="ollamaState === 'loading'" class="help">Checking Ollama…</div>
          <div v-else-if="ollamaState === 'error'" class="help bad">{{ ollamaError }}</div>
          <div v-else-if="ollamaState === 'ok' && !ollamaModels.length" class="help bad">Ollama is running but has no models. Install one, e.g. <span class="mono">ollama pull qwen3:4b-instruct</span></div>
          <div v-else-if="ollamaModelMissing" class="help bad">{{ settings.ollamaModel }} is not installed. Pick one of the {{ ollamaModels.length }} installed models, or run <span class="mono">ollama pull {{ settings.ollamaModel }}</span></div>
          <div v-else-if="ollamaState === 'ok'" class="help">{{ ollamaModels.length }} model{{ ollamaModels.length === 1 ? "" : "s" }} installed. Used for console tab naming and inline suggestions.</div>
        </div>

        <label>Keep loaded</label>
        <div>
          <div class="seg" role="radiogroup" aria-label="Keep the Ollama model loaded">
            <button v-for="k in KEEP" :key="k.v" :class="{ on: settings.ollamaKeepAlive === k.v }" role="radio" :aria-checked="settings.ollamaKeepAlive === k.v" @click="settings.ollamaKeepAlive = k.v">{{ k.l }}</button>
          </div>
          <div class="help">How long Ollama keeps the model in memory after a request.</div>
        </div>

        <label>Inline suggestions</label>
        <div>
          <div class="seg" role="radiogroup" aria-label="Inline suggestions">
            <button :class="{ on: !settings.aiInline }" role="radio" :aria-checked="!settings.aiInline" @click="settings.aiInline = false">Off</button>
            <button :class="{ on: settings.aiInline }" role="radio" :aria-checked="settings.aiInline" @click="settings.aiInline = true">On</button>
          </div>
          <div class="help">Suggests the rest of the line as gray text while you type in the SQL console; Tab accepts it.</div>
        </div>

        <template v-if="settings.aiInline">
          <label for="set-ai-inline-model">Suggestion model</label>
          <div>
            <input id="set-ai-inline-model" v-model.trim="settings.aiInlineModel" class="input mono w-full" list="set-ollama-models" spellcheck="false" :placeholder="settings.ollamaModel ? `Same as above (${settings.ollamaModel})` : 'Same as above'" />
            <div v-if="inlineModelMissing" class="help bad">{{ settings.aiInlineModel }} is not installed. Run <span class="mono">ollama pull {{ settings.aiInlineModel }}</span> or leave this empty.</div>
            <div v-else class="help">Empty = the Ollama model above. A small coder model (e.g. <span class="mono">qwen2.5-coder:1.5b</span>) is faster and fills in the middle of a query.</div>
          </div>
        </template>
      </template>
    </div>
    <template #footer>
      <button class="btn ghost mr-auto" @click="reset">Reset to defaults</button>
      <button class="btn primary" @click="ui.closeModal()">Done</button>
    </template>
  </Modal>
</template>

<style scoped>
.grid { display: grid; grid-template-columns: 130px 1fr; gap: 14px 12px; align-items: center; }
label { color: var(--dim); }
.help { margin-top: 3px; font-size: 11px; color: var(--dim); }
.help.bad { color: var(--danger); }
.sect { grid-column: 1 / -1; margin-top: 4px; padding-top: 12px; border-top: 1px solid var(--bd); font-weight: 600; }
.seg { display: inline-flex; padding: 2px; gap: 2px; border: 1px solid var(--bd); border-radius: var(--r); background: var(--bg2); flex-wrap: wrap; }
.seg button { border: 0; background: none; padding: 3px 10px; border-radius: calc(var(--r) - 2px); color: var(--dim); }
.seg button:hover { color: var(--tx); }
.seg button.on { background: var(--bg); color: var(--tx); box-shadow: 0 1px 2px var(--shadow); }
</style>
