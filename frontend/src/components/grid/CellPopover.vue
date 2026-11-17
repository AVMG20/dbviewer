<script setup lang="ts">
// Multi-line / JSON cell editor (and viewer) popover, anchored near a cell.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { minifyJson, prettyJson } from "@/lib/format";
import { currentAccent } from "@/lib/accent";
import { isMod } from "@/lib/keys";
import { useUiStore } from "@/stores/ui";
import { copyText } from "@/lib/clipboard";
import Icon from "@/components/common/Icon.vue";
import ValueEditor, { type ValueLang } from "./ValueEditor.vue";

const props = defineProps<{
  x: number; y: number;
  title: string; type: string; kind: string;
  initial: string;
  loading?: boolean;
  note?: string;
  canEdit: boolean;
  nullable: boolean;
  error?: string;
  warning?: string;
  /** > 1: Apply writes the value to all selected cells */
  multiCount?: number;
}>();
const emit = defineEmits<{
  commit: [text: string];
  "set-null": [];
  "set-default": [];
  cancel: [];
}>();

const ui = useUiStore();
const accent = currentAccent();
const text = ref(props.initial);
/** text as first shown (after JSON pretty-printing): "dirty" means the user changed it */
const baseline = ref(props.initial);
const editor = ref<InstanceType<typeof ValueEditor> | null>(null);

/** JSON for JSON column types or JSON-looking text, XML for markup, otherwise plain text */
function detectLang(text: string): ValueLang {
  if (props.kind === "json" || /json/i.test(props.type)) return "json";
  const t = text.trim();
  if ((t.startsWith("{") || t.startsWith("[")) && prettyJson(t) !== null) return "json";
  if (t.startsWith("<") && (/^<\?xml/i.test(t) || /^<([\w:.-]+)(\s[^>]*)?>[\s\S]*<\/\1>\s*$/.test(t) || /^<[\w:.-]+(\s[^>]*)?\/>$/.test(t))) return "xml";
  return "text";
}
const lang = ref<ValueLang>(detectLang(props.initial));

function xmlOk(s: string): boolean {
  const d = new DOMParser().parseFromString(s, "application/xml");
  return !d.querySelector("parsererror");
}
function prettyXml(s: string): string | null {
  if (!xmlOk(s)) return null;
  const lines = s.trim().replace(/>\s*</g, ">\n<").split("\n");
  let depth = 0;
  const out: string[] = [];
  for (const raw of lines) {
    const l = raw.trim();
    if (!l) continue;
    if (/^<\//.test(l)) depth = Math.max(0, depth - 1);
    out.push("  ".repeat(depth) + l);
    if (/^<[^!?/][^>]*[^/]>$/.test(l) && !/<\/[^>]+>$/.test(l)) depth++;
  }
  return out.join("\n");
}
function minifyXml(s: string): string | null {
  return xmlOk(s) ? s.trim().replace(/>\s+</g, "><") : null;
}
const root = ref<HTMLElement | null>(null);
const pos = ref({ x: props.x, y: props.y });
const dirty = computed(() => text.value !== baseline.value);

watch(() => props.initial, (v) => {
  text.value = v;
  baseline.value = v;
  formatJson();
  lang.value = detectLang(v);
});
watch(() => ui.overlayOpen, (o) => o && emit("cancel"));
watch(() => props.loading, async (l) => {
  if (!l) {
    await nextTick();
    editor.value?.focus();
  }
});

function formatJson() {
  if (props.kind === "json" && props.canEdit) {
    const p = prettyJson(text.value);
    if (p !== null && p !== text.value) {
      text.value = p;
      baseline.value = p;
    }
  }
}
/** Apply: stages the text. Identical to the original = nothing to stage. */
function commit() {
  if (text.value === props.initial && !props.multiCount) emit("cancel");
  else emit("commit", text.value);
}

onMounted(async () => {
  await nextTick();
  const r = root.value?.getBoundingClientRect();
  if (r) {
    pos.value = {
      x: Math.max(6, Math.min(props.x, window.innerWidth - r.width - 8)),
      y: Math.max(6, Math.min(props.y, window.innerHeight - r.height - 8)),
    };
  }
  formatJson();
  editor.value?.focus();
  window.addEventListener("mousedown", onOutside, true);
});
onBeforeUnmount(() => window.removeEventListener("mousedown", onOutside, true));

function onOutside(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) {
    // Clicking outside discards: only Apply (or Cmd+Enter) stages the edited value.
    emit("cancel");
  }
}
function onKey(e: KeyboardEvent) {
  if (e.key === "Escape") {
    e.preventDefault();
    e.stopPropagation();
    emit("cancel");
  } else if (e.key === "Enter" && isMod(e)) {
    e.preventDefault();
    e.stopPropagation();
    if (props.canEdit) commit();
    else emit("cancel");
  } else if (isMod(e) && e.key.toLowerCase() === "s") {
    // not an Apply: let the event reach the view (the save preview opens and closes this popover)
    return;
  }
  e.stopPropagation();
}
/** Format / Minify only change the editor text; they never stage anything */
function pretty() {
  const p = lang.value === "xml" ? prettyXml(text.value) : prettyJson(text.value);
  if (p !== null) text.value = p;
}
function minify() {
  const p = lang.value === "xml" ? minifyXml(text.value) : minifyJson(text.value);
  if (p !== null) text.value = p;
}
</script>

<template>
  <Teleport to="body">
    <div ref="root" class="pop" :data-accent="accent" :style="{ left: pos.x + 'px', top: pos.y + 'px' }" @keydown="onKey">
      <div class="pop-head">
        <span class="mono truncate">{{ title }}</span>
        <span class="dim mono">{{ type }}</span>
        <span class="flex-1"></span>
        <span v-if="multiCount" class="pop-multi">→ {{ multiCount }} cells</span>
        <span class="dim mono">{{ text.length.toLocaleString("en-US") }} chars</span>
        <select v-model="lang" class="pop-lang" title="Language (highlighting)">
          <option value="text">Text</option><option value="json">JSON</option><option value="xml">XML</option>
        </select>
        <button class="btn ghost" title="Copy value" @click="copyText(text)"><Icon name="copy" /></button>
      </div>
      <div v-if="note" class="pop-note">{{ note }}</div>
      <div v-if="error" class="pop-err">{{ error }}</div>
      <div v-else-if="warning" class="pop-note">{{ warning }}</div>
      <div v-if="loading" class="pop-loading dim">Loading full value…</div>
      <ValueEditor v-else ref="editor" v-model="text" :lang="lang" :read-only="!canEdit" />
      <div class="pop-foot">
        <template v-if="canEdit">
          <button v-if="nullable" class="btn" @click="emit('set-null')">Set NULL</button>
          <button class="btn" @click="emit('set-default')">Set default</button>
        </template>
        <template v-if="lang !== 'text'">
          <button class="btn ghost" @click="pretty">Format</button>
          <button class="btn ghost" @click="minify">Minify</button>
        </template>
        <span class="flex-1"></span>
        <button class="btn ghost" @click="emit('cancel')">{{ canEdit ? "Cancel" : "Close" }} <kbd>esc</kbd></button>
        <button v-if="canEdit" class="btn primary" :disabled="loading" @click="commit">Apply <kbd class="kk">⌘↵</kbd></button>
      </div>
      <div v-if="canEdit" class="pop-hint">Click outside to cancel</div>
    </div>
  </Teleport>
</template>

<style scoped>
.pop {
  position: fixed; z-index: var(--z-popover, 40); width: 540px; background: var(--bg2); border: 1px solid var(--bd); border-radius: 8px;
  box-shadow: 0 12px 40px var(--shadow); display: flex; flex-direction: column; resize: both; overflow: hidden;
  min-width: 320px; min-height: 180px; height: 280px; max-width: 90vw; max-height: 85vh;
}
.pop-head { display: flex; align-items: center; gap: 8px; padding: 5px 8px 5px 12px; border-bottom: 1px solid var(--bd); }
.pop-note { padding: 4px 12px; color: var(--warn); border-bottom: 1px solid var(--bd); font-size: 11px; }
.pop-multi { font: 600 11px var(--sans); padding: 0 7px; border-radius: 4px; background: var(--ac); color: var(--ac-fg, #fff); line-height: 18px; }
.pop-lang { height: 20px; font-size: 11px; padding: 0 2px; border: 1px solid var(--bd); border-radius: 4px; background: var(--bg); color: var(--tx); }
.pop-err { padding: 4px 12px; color: #fff; background: var(--danger); font-size: 11px; }
.pop-loading { flex: 1; padding: 12px; }
.pop-hint { padding: 0 10px 4px; font-size: 10px; color: var(--dim); text-align: right; }
.pop-foot { flex-wrap: wrap; display: flex; gap: 6px; align-items: center; padding: 6px 8px; border-top: 1px solid var(--bd); }
</style>
