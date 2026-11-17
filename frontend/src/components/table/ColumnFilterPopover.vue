<script setup lang="ts">
// Per-column filter popover (=, !=, contains, starts with, is null, is not null, in, between).
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import type { ColumnFilter, FilterOp } from "@/api";
import { currentAccent } from "@/lib/accent";

const props = defineProps<{ column: string; type: string; rect: DOMRect; existing?: ColumnFilter }>();
const emit = defineEmits<{ apply: [f: ColumnFilter]; clear: []; close: [] }>();

interface OpDef { op: FilterOp; label: string }
const OPS: OpDef[] = [
  { op: "eq", label: "equals (=)" },
  { op: "ne", label: "not equals (≠)" },
  { op: "contains", label: "contains" },
  { op: "not_contains", label: "does not contain" },
  { op: "starts_with", label: "starts with" },
  { op: "ends_with", label: "ends with" },
  { op: "gt", label: "greater than (>)" },
  { op: "gte", label: "at least (≥)" },
  { op: "lt", label: "less than (<)" },
  { op: "lte", label: "at most (≤)" },
  { op: "is_null", label: "is NULL" },
  { op: "not_null", label: "is not NULL" },
  { op: "in", label: "in list" },
  { op: "not_in", label: "not in list" },
  { op: "between", label: "between" },
];

const op = ref<FilterOp>((props.existing?.op as FilterOp) ?? "eq");
const value = ref(props.existing?.value ?? "");
const list = ref((props.existing?.values ?? []).join("\n"));
const v1 = ref(props.existing?.values?.[0] ?? "");
const v2 = ref(props.existing?.values?.[1] ?? "");
const root = ref<HTMLElement | null>(null);
const accent = currentAccent();
const input = ref<HTMLInputElement | HTMLTextAreaElement | null>(null);
const pos = ref({ x: props.rect.left, y: props.rect.bottom + 4 });

const needsNone = computed(() => op.value === "is_null" || op.value === "not_null");
const isList = computed(() => op.value === "in" || op.value === "not_in");
const isBetween = computed(() => op.value === "between");

onMounted(async () => {
  await nextTick();
  const r = root.value?.getBoundingClientRect();
  if (r) pos.value = { x: Math.max(6, Math.min(props.rect.left, window.innerWidth - r.width - 8)), y: Math.min(props.rect.bottom + 4, window.innerHeight - r.height - 8) };
  input.value?.focus();
  window.addEventListener("mousedown", onOutside, true);
});
onBeforeUnmount(() => window.removeEventListener("mousedown", onOutside, true));
function onOutside(e: MouseEvent) {
  if (!root.value?.contains(e.target as Node)) emit("close");
}

function apply() {
  const f: ColumnFilter = { column: props.column, op: op.value, value: "", values: [] };
  if (isList.value) {
    f.values = list.value.split(/[\n,]/).map((s) => s.trim()).filter((s) => s !== "");
    if (!f.values.length) return;
  } else if (isBetween.value) {
    f.values = [v1.value, v2.value];
  } else if (!needsNone.value) f.value = value.value;
  emit("apply", f);
}
function onKey(e: KeyboardEvent) {
  if (e.key === "Escape") {
    e.stopPropagation();
    emit("close");
  } else if (e.key === "Enter" && !(e.target instanceof HTMLTextAreaElement && !e.metaKey)) {
    e.preventDefault();
    e.stopPropagation();
    apply();
  }
}
</script>

<template>
  <Teleport to="body">
    <div ref="root" class="fp" :data-accent="accent" :style="{ left: pos.x + 'px', top: pos.y + 'px' }" @keydown="onKey">
      <div class="fp-h"><b class="mono">{{ column }}</b> <span class="dim mono">{{ type }}</span></div>
      <select v-model="op" class="input fp-sel">
        <option v-for="o in OPS" :key="o.op" :value="o.op">{{ o.label }}</option>
      </select>
      <input v-if="!needsNone && !isList && !isBetween" ref="input" v-model="value" class="input mono" placeholder="Value" spellcheck="false" />
      <template v-if="isBetween">
        <input ref="input" v-model="v1" class="input mono" placeholder="From" spellcheck="false" />
        <input v-model="v2" class="input mono" placeholder="To" spellcheck="false" />
      </template>
      <textarea v-if="isList" ref="input" v-model="list" class="input mono fp-ta" placeholder="One value per line (or comma separated)" spellcheck="false"></textarea>
      <div class="fp-f">
        <button v-if="existing" class="btn ghost" @click="emit('clear')">Remove</button>
        <span class="flex-1"></span>
        <button class="btn ghost" @click="emit('close')">Cancel</button>
        <button class="btn primary" @click="apply">Apply <kbd class="kk">↵</kbd></button>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.fp {
  position: fixed; z-index: var(--z-popover, 40); width: 250px; padding: 8px; display: flex; flex-direction: column; gap: 6px;
  background: var(--bg2); border: 1px solid var(--bd); border-radius: 8px; box-shadow: 0 12px 40px var(--shadow);
}
.fp-h { display: flex; gap: 6px; align-items: baseline; }
.fp-sel { height: 24px; }
.fp-ta { height: 84px; padding: 4px 6px; resize: vertical; }
.fp-f { display: flex; gap: 6px; margin-top: 2px; }
</style>
