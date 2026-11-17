<script setup lang="ts">
// DataGrip-style raw SQL clause input (WHERE / ORDER BY) with cheap column-name autocomplete.
import { computed, ref } from "vue";

const props = defineProps<{
  modelValue: string; label: string; placeholder?: string; columns: string[]; error?: string;
}>();
const emit = defineEmits<{ "update:modelValue": [v: string]; apply: [] }>();

const input = ref<HTMLInputElement | null>(null);
const caret = ref(0);
const open = ref(false);
const hi = ref(-1); // -1 = nothing chosen yet: Enter applies; after arrow keys Enter accepts

const token = computed(() => {
  const before = props.modelValue.slice(0, caret.value);
  const m = /[A-Za-z_][A-Za-z_0-9]*$/.exec(before);
  return m ? m[0] : "";
});
const suggestions = computed(() => {
  const t = token.value.toLowerCase();
  if (!open.value || t.length < 1) return [];
  const starts: string[] = [];
  const contains: string[] = [];
  for (const c of props.columns) {
    const l = c.toLowerCase();
    if (l === t) continue;
    if (l.startsWith(t)) starts.push(c);
    else if (l.includes(t)) contains.push(c);
  }
  return [...starts, ...contains].slice(0, 8);
});

function sync() {
  caret.value = input.value?.selectionStart ?? props.modelValue.length;
}
function onInput(e: Event) {
  emit("update:modelValue", (e.target as HTMLInputElement).value);
  sync();
  open.value = true;
  hi.value = -1;
}
function accept(name: string) {
  const el = input.value;
  const pos = caret.value;
  const start = pos - token.value.length;
  const text = props.modelValue.slice(0, start) + name + props.modelValue.slice(pos);
  emit("update:modelValue", text);
  open.value = false;
  const np = start + name.length;
  requestAnimationFrame(() => {
    el?.focus();
    el?.setSelectionRange(np, np);
    sync();
  });
}
function onKey(e: KeyboardEvent) {
  const s = suggestions.value;
  if (s.length) {
    if (e.key === "ArrowDown") return e.preventDefault(), void (hi.value = (hi.value + 1) % s.length);
    if (e.key === "ArrowUp") return e.preventDefault(), void (hi.value = hi.value <= 0 ? s.length - 1 : hi.value - 1);
    if (e.key === "Tab") return e.preventDefault(), accept(s[Math.max(0, hi.value)]);
    if (e.key === "Enter" && hi.value >= 0) return e.preventDefault(), accept(s[hi.value]);
    if (e.key === "Escape") return e.preventDefault(), e.stopPropagation(), void (open.value = false);
  }
  if (e.key === "Enter") {
    e.preventDefault();
    open.value = false;
    emit("apply");
  }
}
</script>

<template>
  <div class="ci" :class="{ err: !!error }" :title="error">
    <span class="dim mono ci-l">{{ label }}</span>
    <div class="ci-w">
      <input
        ref="input" :value="modelValue" class="input mono" :placeholder="placeholder" spellcheck="false" autocomplete="off"
        @input="onInput" @keydown="onKey" @keyup="sync" @click="sync" @blur="open = false"
      />
      <div v-if="suggestions.length" class="ci-dd">
        <div
          v-for="(s, i) in suggestions" :key="s" class="ci-it mono" :class="{ hi: i === hi }"
          @mousedown.prevent="accept(s)"
        >{{ s }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ci { flex: 1 1 220px; display: flex; align-items: center; gap: 6px; min-width: 160px; }
.ci-l { font-size: 11px; }
.ci-w { position: relative; flex: 1; display: flex; }
.ci-w input { flex: 1; width: 100%; }
.ci.err input { border-color: var(--danger); }
.ci-dd {
  position: absolute; top: 24px; left: 0; z-index: 120; min-width: 180px; background: var(--bg2); border: 1px solid var(--bd);
  border-radius: 6px; box-shadow: 0 8px 28px var(--shadow); padding: 3px;
}
.ci-it { padding: 2px 8px; border-radius: 3px; font-size: 12px; }
.ci-it.hi { background: var(--ac); color: var(--ac-fg, #fff); }
</style>
