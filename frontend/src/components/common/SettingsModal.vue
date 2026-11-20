<script setup lang="ts">
// Settings (shell), opened with Cmd+, or from the palette. Changes apply immediately and
// are persisted with the other ui prefs (stores/ui.ts).
import { computed, onMounted, ref, watch } from "vue";
import { useUiStore } from "@/stores/ui";
import { DEFAULT_AI_URL, DEFAULT_SETTINGS, PAGE_SIZES, settings, type AIKeepAlive, type DateStyle, type Density, type NullStyle } from "@/lib/settings";
import { aiError, listModels, pickDefaultModel } from "@/lib/ai";
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

// ---- AI (Ollama) ----------------------------------------------------------------------
const KEEP: { v: AIKeepAlive; l: string }[] = [
  { v: "0", l: "Unload right away" },
  { v: "1m", l: "1 min" },
  { v: "5m", l: "5 min" },
  { v: "30m", l: "30 min" },
];
const models = ref<AIModel[]>([]);
const modelsState = ref<"idle" | "loading" | "ok" | "error">("idle");
const modelsError = ref("");
const aiUrlDraft = ref(settings.aiUrl);
let modelsSeq = 0;

async function loadModels() {
  const seq = ++modelsSeq;
  modelsState.value = "loading";
  try {
    const list = await listModels(settings.aiUrl);
    if (seq !== modelsSeq) return;
    models.value = list;
    modelsState.value = "ok";
    if (settings.aiEnabled && !settings.aiModel) settings.aiModel = pickDefaultModel(list);
  } catch (e) {
    if (seq !== modelsSeq) return;
    models.value = [];
    modelsState.value = "error";
    modelsError.value = aiError(e)?.message ?? "Could not reach Ollama";
  }
}
function setAiEnabled(on: boolean) {
  settings.aiEnabled = on;
  if (on) void loadModels();
}
function commitUrl() {
  const v = aiUrlDraft.value.trim() || DEFAULT_AI_URL;
  aiUrlDraft.value = v;
  if (v === settings.aiUrl && modelsState.value !== "idle") return;
  settings.aiUrl = v;
  void loadModels();
}
watch(() => settings.aiUrl, (v) => (aiUrlDraft.value = v));
const modelMissing = computed(() => modelsState.value === "ok" && !!settings.aiModel && !models.value.some((m) => m.name === settings.aiModel));
const inlineModelMissing = computed(() => modelsState.value === "ok" && !!settings.aiInlineModel && !models.value.some((m) => m.name === settings.aiInlineModel));
const fmtSize = (b: number) => (b >= 1e9 ? `${(b / 1e9).toFixed(1)} GB` : `${Math.round(b / 1e6)} MB`);
onMounted(() => {
  if (settings.aiEnabled) void loadModels();
});

function reset() {
  Object.assign(settings, DEFAULT_SETTINGS);
  models.value = [];
  modelsState.value = "idle";
  ui.setTheme("system");
}
</script>

<template>
  <Modal title="Settings" :width="580" @close="ui.closeModal()">
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

      <div class="sect">AI <span class="dim">· local model via Ollama</span></div>

      <label>Write SQL with AI</label>
      <div>
        <div class="seg" role="radiogroup" aria-label="AI">
          <button :class="{ on: !settings.aiEnabled }" role="radio" :aria-checked="!settings.aiEnabled" @click="setAiEnabled(false)">Off</button>
          <button :class="{ on: settings.aiEnabled }" role="radio" :aria-checked="settings.aiEnabled" @click="setAiEnabled(true)">On</button>
        </div>
        <div class="help">{{ kbd("⌘\\ in the SQL console writes or changes the query at the cursor.") }}</div>
      </div>

      <template v-if="settings.aiEnabled">
        <label for="set-ai-url">Ollama URL</label>
        <div>
          <input id="set-ai-url" v-model="aiUrlDraft" class="input w-full mono" spellcheck="false" :placeholder="DEFAULT_AI_URL" @change="commitUrl" @keydown.enter="commitUrl" />
          <div class="help">Your prompts and the relevant part of the schema (table and column names) are sent to Ollama at this URL. No data rows.</div>
        </div>

        <label for="set-ai-model">Model</label>
        <div>
          <div class="flex items-center gap-1.5">
            <input id="set-ai-model" v-model.trim="settings.aiModel" class="input mono flex-1" list="set-ai-models" spellcheck="false" placeholder="e.g. qwen3:4b-instruct" />
            <datalist id="set-ai-models">
              <option v-for="m in models" :key="m.name" :value="m.name">{{ [m.parameterSize, fmtSize(m.sizeBytes)].filter(Boolean).join(" · ") }}</option>
            </datalist>
            <button class="btn icon" title="Reload the installed models" aria-label="Reload models" :disabled="modelsState === 'loading'" @click="loadModels"><Icon name="refresh" /></button>
          </div>
          <div v-if="modelsState === 'loading'" class="help">Checking Ollama…</div>
          <div v-else-if="modelsState === 'error'" class="help bad">{{ modelsError }}</div>
          <div v-else-if="modelsState === 'ok' && !models.length" class="help bad">Ollama is running but has no models. Install one, e.g. <span class="mono">ollama pull qwen3:4b-instruct</span></div>
          <div v-else-if="modelMissing" class="help bad">{{ settings.aiModel }} is not installed. Pick one of the {{ models.length }} installed models, or run <span class="mono">ollama pull {{ settings.aiModel }}</span></div>
          <div v-else-if="modelsState === 'ok'" class="help">{{ models.length }} model{{ models.length === 1 ? "" : "s" }} installed. Small instruct / coder models (3–8B) answer in about a second.</div>
        </div>

        <label>Keep loaded</label>
        <div>
          <div class="seg" role="radiogroup" aria-label="Keep the model loaded">
            <button v-for="k in KEEP" :key="k.v" :class="{ on: settings.aiKeepAlive === k.v }" role="radio" :aria-checked="settings.aiKeepAlive === k.v" @click="settings.aiKeepAlive = k.v">{{ k.l }}</button>
          </div>
          <div class="help">How long the model stays in memory after a query. Loading it again takes about a second.</div>
        </div>

        <label>Inline suggestions</label>
        <div>
          <div class="seg" role="radiogroup" aria-label="Inline suggestions">
            <button :class="{ on: !settings.aiInline }" role="radio" :aria-checked="!settings.aiInline" @click="settings.aiInline = false">Off</button>
            <button :class="{ on: settings.aiInline }" role="radio" :aria-checked="settings.aiInline" @click="settings.aiInline = true">On</button>
          </div>
          <div class="help">Suggests the rest of the line as gray text while you type in the SQL console; Tab accepts it. Uses the GPU while you type.</div>
        </div>

        <template v-if="settings.aiInline">
          <label for="set-ai-inline-model">Suggestion model</label>
          <div>
            <input id="set-ai-inline-model" v-model.trim="settings.aiInlineModel" class="input mono w-full" list="set-ai-models" spellcheck="false" :placeholder="settings.aiModel ? `Same as above (${settings.aiModel})` : 'Same as above'" />
            <div v-if="inlineModelMissing" class="help bad">{{ settings.aiInlineModel }} is not installed. Run <span class="mono">ollama pull {{ settings.aiInlineModel }}</span> or leave this empty.</div>
            <div v-else class="help">Empty = the model above. A small coder model (e.g. <span class="mono">qwen2.5-coder:1.5b</span>) is faster and fills in the middle of a query.</div>
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
