<script setup lang="ts">
// Shared context / dropdown menu with submenus, keyboard navigation and type-ahead.
//   <ContextMenu v-if="menu" :x :y :items @select="id => ..." @close="menu = null" />
// items: { id?, label, hint?, danger?, disabled?, separator?, icon?, run?, children? }
// An item with `run` calls it; otherwise `id` is emitted through `select`. While open, every key event is swallowed.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { currentAccent } from "@/lib/accent";
import { useUiStore } from "@/stores/ui";
import Icon from "./Icon.vue";

export interface MenuItem {
  id?: string;
  label?: string;
  hint?: string;
  danger?: boolean;
  disabled?: boolean;
  separator?: boolean;
  icon?: string;
  run?: () => void;
  children?: MenuItem[];
}

const props = defineProps<{ x: number; y: number; items: MenuItem[] }>();
const emit = defineEmits<{ select: [id: string]; close: [] }>();
const ui = useUiStore();

const el = ref<HTMLElement | null>(null);
const subEl = ref<HTMLElement | null>(null);
const pos = ref({ x: props.x, y: props.y });
const idx = ref(-1);
/** index of the open submenu parent, and the highlighted child inside it */
const open = ref(-1);
const subIdx = ref(-1);
const subPos = ref<{ x: number; y: number; maxH: number }>({ x: 0, y: 0, maxH: 400 });
const accent = currentAccent();
let typed = "";
let typedTimer: number | undefined;

const subItems = computed(() => (open.value >= 0 ? props.items[open.value]?.children ?? [] : []));
const ok = (it: MenuItem) => !it.separator && !it.disabled;

function nextIndex(list: MenuItem[], from: number, dir: 1 | -1): number {
  const n = list.length;
  for (let k = 1; k <= n; k++) {
    const i = ((from < 0 ? (dir > 0 ? -1 : 0) : from) + dir * k + n * 2) % n;
    if (ok(list[i])) return i;
  }
  return -1;
}

function pick(it: MenuItem | undefined) {
  if (!it || !ok(it)) return;
  if (it.children) return void openSub(props.items.indexOf(it));
  emit("close");
  if (it.run) it.run();
  else if (it.id) emit("select", it.id);
}

async function openSub(i: number) {
  open.value = i;
  subIdx.value = -1;
  await nextTick();
  const parent = el.value?.querySelectorAll<HTMLElement>("[data-mi]")[i];
  const pr = parent?.getBoundingClientRect();
  const sr = subEl.value?.getBoundingClientRect();
  if (!pr) return;
  const w = sr?.width ?? 200;
  const x = pr.right + 2 + w > window.innerWidth ? Math.max(4, pr.left - w - 2) : pr.right + 2;
  const maxH = window.innerHeight - 12;
  const h = Math.min(sr?.height ?? 200, maxH);
  const y = Math.max(6, Math.min(pr.top - 4, window.innerHeight - h - 6));
  subPos.value = { x, y, maxH };
}

function typeAhead(ch: string): boolean {
  typed += ch.toLowerCase();
  clearTimeout(typedTimer);
  typedTimer = window.setTimeout(() => (typed = ""), 700);
  const inSub = open.value >= 0 && subIdx.value >= 0;
  const list = inSub ? subItems.value : props.items;
  const i = list.findIndex((it) => ok(it) && (it.label ?? "").toLowerCase().startsWith(typed));
  if (i >= 0) {
    if (inSub) subIdx.value = i;
    else idx.value = i;
  }
  return true;
}

function onKey(e: KeyboardEvent) {
  // The menu owns the keyboard while it is open: nothing may reach the grid / editors underneath.
  e.stopPropagation();
  const inSub = open.value >= 0 && subIdx.value >= 0;
  const k = e.key;
  if (k === "Escape") {
    e.preventDefault();
    if (open.value >= 0) {
      open.value = -1;
      subIdx.value = -1;
    } else emit("close");
  } else if (k === "ArrowDown" || k === "ArrowUp") {
    e.preventDefault();
    const dir = k === "ArrowDown" ? 1 : -1;
    if (open.value >= 0 && subItems.value.length) subIdx.value = nextIndex(subItems.value, subIdx.value, dir);
    else idx.value = nextIndex(props.items, idx.value, dir);
  } else if (k === "ArrowRight") {
    e.preventDefault();
    if (idx.value >= 0 && props.items[idx.value]?.children && ok(props.items[idx.value])) {
      openSub(idx.value).then(() => (subIdx.value = nextIndex(subItems.value, -1, 1)));
    }
  } else if (k === "ArrowLeft") {
    e.preventDefault();
    open.value = -1;
    subIdx.value = -1;
  } else if (k === "Enter" || k === " ") {
    e.preventDefault();
    if (inSub) pick(subItems.value[subIdx.value]);
    else if (idx.value >= 0) {
      const it = props.items[idx.value];
      if (it?.children) openSub(idx.value).then(() => (subIdx.value = nextIndex(subItems.value, -1, 1)));
      else pick(it);
    }
  } else if (k.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
    e.preventDefault();
    typeAhead(k);
  } else if (k === "Tab") {
    e.preventDefault();
  } else if (!(e.metaKey || e.ctrlKey) || ["a", "c", "v", "x", "z", "s", "backspace"].includes(k.toLowerCase())) {
    e.preventDefault();
  }
}
function onDown(e: MouseEvent) {
  const t = e.target as Node;
  if (!el.value?.contains(t) && !subEl.value?.contains(t)) emit("close");
}
function onWheel(e: Event) {
  const t = e.target as Node;
  if (!el.value?.contains(t) && !subEl.value?.contains(t)) emit("close");
}
const onBlur = () => emit("close");

watch(() => ui.overlayOpen, (o) => o && emit("close"));

onMounted(async () => {
  window.addEventListener("keydown", onKey, true);
  window.addEventListener("mousedown", onDown, true);
  window.addEventListener("wheel", onWheel, { capture: true, passive: true });
  window.addEventListener("blur", onBlur);
  window.addEventListener("resize", onBlur);
  await nextTick();
  const r = el.value?.getBoundingClientRect();
  if (r) {
    pos.value = {
      x: Math.max(4, Math.min(props.x, window.innerWidth - r.width - 4)),
      y: Math.max(4, Math.min(props.y, window.innerHeight - r.height - 4)),
    };
  }
});
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onKey, true);
  window.removeEventListener("mousedown", onDown, true);
  window.removeEventListener("wheel", onWheel, true);
  window.removeEventListener("blur", onBlur);
  window.removeEventListener("resize", onBlur);
  clearTimeout(typedTimer);
});

function hover(i: number, it: MenuItem) {
  if (it.disabled) return;
  idx.value = i;
  if (it.children) openSub(i);
  else {
    open.value = -1;
    subIdx.value = -1;
  }
}
</script>

<template>
  <Teleport to="body">
    <div
      ref="el" class="no-drag fixed min-w-[190px] rounded-[10px] border border-bd2 bg-pop p-1" role="menu" :data-accent="accent"
      :style="{ left: pos.x + 'px', top: pos.y + 'px', zIndex: 'var(--z-menu, 45)', boxShadow: '0 8px 28px var(--shadow)' }"
      @contextmenu.prevent
    >
      <template v-for="(it, i) in items" :key="i">
        <div v-if="it.separator" class="my-[3px] border-t border-bd" />
        <div
          v-else
          data-mi
          role="menuitem"
          class="flex items-center gap-2 rounded-[7px] px-2.5 py-[4px] whitespace-nowrap"
          :class="[
            it.disabled ? 'opacity-40' : idx === i || open === i ? 'bg-[var(--selbg)]' : '',
            it.danger && idx !== i && !it.disabled ? 'text-danger' : '',
          ]"
          @mouseenter="hover(i, it)"
          @click.stop="pick(it)"
        >
          <Icon v-if="it.icon" :name="it.icon" />
          <span class="flex-1 truncate">{{ it.label }}</span>
          <span v-if="it.children" class="opacity-70">▸</span>
          <span v-else-if="it.hint" class="font-mono text-[10px] opacity-60">{{ it.hint }}</span>
        </div>
      </template>
    </div>
    <div
      v-if="open >= 0 && subItems.length" ref="subEl"
      class="no-drag fixed min-w-[190px] overflow-auto rounded-[10px] border border-bd2 bg-pop p-1" role="menu" :data-accent="accent"
      :style="{ left: subPos.x + 'px', top: subPos.y + 'px', maxHeight: subPos.maxH + 'px', zIndex: 'var(--z-menu, 45)', boxShadow: '0 8px 28px var(--shadow)' }"
    >
      <template v-for="(ch, j) in subItems" :key="j">
        <div v-if="ch.separator" class="my-[3px] border-t border-bd" />
        <div
          v-else
          role="menuitem"
          class="flex items-center gap-2 rounded-[7px] px-2.5 py-[4px] whitespace-nowrap"
          :class="[ch.disabled ? 'opacity-40' : subIdx === j ? 'bg-[var(--selbg)]' : '']"
          @mouseenter="!ch.disabled && (subIdx = j)"
          @click.stop="pick(ch)"
        >
          <span class="flex-1 truncate">{{ ch.label }}</span>
          <span v-if="ch.hint" class="font-mono text-[10px] opacity-60">{{ ch.hint }}</span>
        </div>
      </template>
    </div>
  </Teleport>
</template>
