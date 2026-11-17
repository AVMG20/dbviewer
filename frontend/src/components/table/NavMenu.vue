<script setup lang="ts">
// Small dropdown for Cmd+click navigation: "References" and "Referenced by" entries, keyboard navigable
// (arrows, Enter, Esc), type-ahead filter, lazily loaded dim row counts.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { dataApi } from "@/api";
import { currentAccent } from "@/lib/accent";
import { fmtNum } from "@/lib/format";
import type { NavEntry } from "@/lib/fkNav";
import { useUiStore } from "@/stores/ui";

const props = defineProps<{ x: number; y: number; entries: NavEntry[]; connId: string }>();
const emit = defineEmits<{ pick: [e: NavEntry]; close: [] }>();
const ui = useUiStore();
const accent = currentAccent();
const el = ref<HTMLElement | null>(null);
const pos = ref({ x: props.x, y: props.y });
const filter = ref("");
const hi = ref(0);
const counts = ref<Record<number, number | "…" | "?">>({});
const cancels: { cancel: () => void }[] = [];

const visible = computed(() => {
  const f = filter.value.toLowerCase();
  return props.entries.map((e, i) => ({ e, i })).filter(({ e }) => !f || e.label.toLowerCase().includes(f));
});
watch(filter, () => (hi.value = 0));

function pick(i: number) {
  const it = visible.value[i];
  if (it) emit("pick", it.e);
}
function onKey(e: KeyboardEvent) {
  e.stopPropagation();
  const n = visible.value.length;
  if (e.key === "Escape") {
    e.preventDefault();
    if (filter.value) filter.value = "";
    else emit("close");
  } else if (e.key === "ArrowDown") {
    e.preventDefault();
    hi.value = n ? (hi.value + 1) % n : 0;
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    hi.value = n ? (hi.value - 1 + n) % n : 0;
  } else if (e.key === "Enter") {
    e.preventDefault();
    pick(hi.value);
  } else if (e.key === "Backspace") {
    e.preventDefault();
    filter.value = filter.value.slice(0, -1);
  } else if (e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
    e.preventDefault();
    filter.value += e.key;
  } else if (e.key === "Tab") e.preventDefault();
}
function onDown(e: MouseEvent) {
  if (!el.value?.contains(e.target as Node)) emit("close");
}
const onBlur = () => emit("close");
watch(() => ui.overlayOpen, (o) => o && emit("close"));

onMounted(async () => {
  window.addEventListener("keydown", onKey, true);
  window.addEventListener("mousedown", onDown, true);
  window.addEventListener("blur", onBlur);
  await nextTick();
  const r = el.value?.getBoundingClientRect();
  if (r) pos.value = { x: Math.max(4, Math.min(props.x, window.innerWidth - r.width - 6)), y: Math.max(4, Math.min(props.y + 8, window.innerHeight - r.height - 6)) };
  // lazy row counts (cancelled when the menu closes); a few at a time so the menu stays snappy
  props.entries.slice(0, 12).forEach((en, i) => {
    counts.value[i] = "…";
    const p = dataApi.CountRows({
      connId: props.connId, schema: en.schema, table: en.table, where: en.where, orderBy: "", limit: 0, offset: 0, columnFilters: [], countTotal: true,
    });
    cancels.push(p);
    p.then((n) => (counts.value[i] = n)).catch(() => (counts.value[i] = "?"));
  });
});
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onKey, true);
  window.removeEventListener("mousedown", onDown, true);
  window.removeEventListener("blur", onBlur);
  cancels.forEach((c) => c.cancel());
});
function countText(i: number): string {
  const c = counts.value[i];
  return c === undefined || c === "?" ? "" : c === "…" ? "…" : `${fmtNum(c)} row${c === 1 ? "" : "s"}`;
}
function showHeading(vi: number): boolean {
  return vi === 0 || visible.value[vi].e.heading !== visible.value[vi - 1].e.heading;
}
</script>

<template>
  <Teleport to="body">
    <div
      ref="el" class="nav" role="listbox" :data-accent="accent" :style="{ left: pos.x + 'px', top: pos.y + 'px' }"
      @contextmenu.prevent
    >
      <div v-if="filter" class="nav-f mono">{{ filter }}</div>
      <template v-for="(it, vi) in visible" :key="it.i">
        <div v-if="showHeading(vi)" class="nav-h">{{ it.e.heading }}</div>
        <div class="nav-i" :class="{ hi: hi === vi }" role="option" @mouseenter="hi = vi" @click="pick(vi)">
          <span class="mono truncate">{{ it.e.label }}</span>
          <span class="nav-c">{{ countText(it.i) }}</span>
        </div>
      </template>
      <div v-if="!visible.length" class="nav-i dim">No match</div>
    </div>
  </Teleport>
</template>

<style scoped>
.nav {
  position: fixed; z-index: var(--z-menu, 45); min-width: 230px; max-width: 420px; max-height: 60vh; overflow: auto; padding: 3px;
  background: var(--bg2); border: 1px solid var(--bd); border-radius: 6px; box-shadow: 0 8px 28px var(--shadow);
}
.nav-h { padding: 4px 8px 2px; font-size: 10px; text-transform: uppercase; letter-spacing: .05em; color: var(--dim); }
.nav-f { padding: 2px 8px; font-size: 11px; color: var(--ac); border-bottom: 1px solid var(--bd); margin-bottom: 2px; }
.nav-i { display: flex; align-items: center; gap: 14px; padding: 3px 8px; border-radius: 3px; white-space: nowrap; }
.nav-i.hi { background: var(--ac); color: var(--ac-fg, #fff); }
.nav-c { margin-left: auto; font-size: 10px; color: var(--dim); }
.nav-i.hi .nav-c { color: color-mix(in srgb, var(--ac-fg, #fff) 70%, transparent); }
</style>
