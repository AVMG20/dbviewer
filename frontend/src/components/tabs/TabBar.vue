<script setup lang="ts">
// Tab bar (shell agent): tabs with connection color stripe, kind icon, dirty / pending
// badge, close button, drag reorder, context menu; plus the connection context bar below.
// Console tabs can be renamed: double-click or "Rename…" edits the title inline (Enter saves, Esc cancels;
// an empty name restores automatic naming, see tabs.rename / tabs.autoTitle).
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { kbd } from "@/lib/kbd";
import { useChangesStore } from "@/stores/changes";
import { useProjectsStore } from "@/stores/projects";
import { useTabsStore } from "@/stores/tabs";
import { useUiStore } from "@/stores/ui";
import type { Tab } from "@/types";
import Icon from "@/components/common/Icon.vue";
import ContextMenu, { type MenuItem } from "@/components/common/ContextMenu.vue";

const projects = useProjectsStore();
const tabs = useTabsStore();
const ui = useUiStore();
const changes = useChangesStore();

const strip = ref<HTMLElement | null>(null);
const icon = { table: "table", console: "sql", structure: "structure" } as const;

function pending(t: Tab): number {
  try {
    return changes.count(t.id);
  } catch {
    return 0;
  }
}
function connOf(t: Tab) {
  return projects.getConnection(t.connId);
}
function isRed(t: Tab) {
  return connOf(t)?.color === "red";
}

// ---- new console ----------------------------------------------------------------
function newConsole() {
  const connId = tabs.active?.connId ?? projects.projectConnections[0]?.id;
  if (!connId) {
    ui.toast("Create a connection first", "info");
    return;
  }
  tabs.openConsole(connId);
}

// ---- drag reorder -----------------------------------------------------------------
const dragIdx = ref(-1);
const overIdx = ref(-1);
function onDragStart(e: DragEvent, i: number) {
  dragIdx.value = i;
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", tabs.tabs[i].title);
  }
}
function onDragOver(e: DragEvent, i: number) {
  if (dragIdx.value < 0) return;
  e.preventDefault();
  overIdx.value = i;
}
function onDrop(e: DragEvent, i: number) {
  e.preventDefault();
  if (dragIdx.value >= 0) tabs.move(dragIdx.value, i);
  dragIdx.value = overIdx.value = -1;
}
function onDragEnd() {
  dragIdx.value = overIdx.value = -1;
}

function onTabClick(e: MouseEvent, t: Tab) {
  if (e.shiftKey) void tabs.close(t.id); // Shift+click closes (dirty confirm included)
  else tabs.activate(t.id, { focus: true });
}

// ---- rename (console tabs) ----------------------------------------------------------
const renaming = ref<string | null>(null);
const renameText = ref("");
async function startRename(t: Tab) {
  if (t.kind !== "console") return;
  renaming.value = t.id;
  renameText.value = t.title;
  await nextTick();
  revealActive();
  const el = strip.value?.querySelector<HTMLInputElement>("input.rename");
  el?.focus();
  el?.select();
}
function endRename(save: boolean) {
  const id = renaming.value;
  if (!id) return;
  renaming.value = null;
  if (save) tabs.rename(id, renameText.value);
  if (id === tabs.activeId) ui.focusActiveView();
}
function onRenameKey(e: KeyboardEvent) {
  e.stopPropagation();
  if (e.key === "Enter") {
    e.preventDefault();
    endRename(true);
  } else if (e.key === "Escape") {
    e.preventDefault();
    endRename(false);
  }
}
function onTabDblClick(t: Tab) {
  if (t.kind === "console") void startRename(t);
  else tabs.promote(t.id);
}

// ---- mouse ------------------------------------------------------------------------
function onAuxClick(e: MouseEvent, t: Tab) {
  if (e.button === 1) {
    e.preventDefault();
    void tabs.close(t.id);
  }
}
function onWheel(e: WheelEvent) {
  if (strip.value && Math.abs(e.deltaY) > Math.abs(e.deltaX)) strip.value.scrollLeft += e.deltaY;
}

// ---- keep the active tab visible / overflow ----------------------------------------
const overflowing = ref(false);
function measure() {
  const el = strip.value;
  overflowing.value = !!el && el.scrollWidth > el.clientWidth + 1;
}
function revealActive() {
  const el = strip.value;
  const t = el?.querySelector<HTMLElement>('.tab[data-active="true"]');
  if (!el || !t) return;
  // measured from the rendered boxes (offsetLeft depends on the offsetParent chain)
  const box = el.getBoundingClientRect();
  const r = t.getBoundingClientRect();
  const left = r.left - box.left + el.scrollLeft;
  const right = left + r.width;
  if (left < el.scrollLeft + 8) el.scrollLeft = Math.max(0, left - 24);
  else if (right > el.scrollLeft + el.clientWidth - 8) el.scrollLeft = Math.min(right - el.clientWidth + 24, el.scrollWidth - el.clientWidth);
}
let ro: ResizeObserver | null = null;
onMounted(() => {
  if (strip.value) {
    ro = new ResizeObserver(() => {
      measure();
      revealActive();
    });
    ro.observe(strip.value);
  }
  measure();
});
onBeforeUnmount(() => ro?.disconnect());
// also when tabs are reordered / renamed / pinned (the active tab may move or grow off-screen)
watch(
  () => [tabs.activeId, tabs.tabs.map((t) => `${t.id}\u0000${t.title}\u0000${t.pinned ? 1 : 0}`).join("\u0001")] as const,
  async () => {
    await nextTick();
    measure();
    revealActive();
  },
  { flush: "post" },
);

// ---- "all tabs" dropdown --------------------------------------------------------------
const listOpen = ref(false);
const listPos = ref({ x: 0, y: 0 });
const listQuery = ref("");
const listIdx = ref(0);
const listInput = ref<HTMLInputElement | null>(null);
const listEl = ref<HTMLElement | null>(null);
const listItems = computed(() => {
  const q = listQuery.value.trim().toLowerCase();
  return tabs.tabs.filter((t) => !q || t.title.toLowerCase().includes(q) || (connOf(t)?.name ?? "").toLowerCase().includes(q));
});
async function openList(e: MouseEvent) {
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
  listPos.value = { x: Math.max(8, Math.min(r.right - 300, window.innerWidth - 308)), y: r.bottom + 4 };
  listQuery.value = "";
  listIdx.value = Math.max(0, tabs.tabs.findIndex((t) => t.id === tabs.activeId));
  listOpen.value = true;
  await nextTick();
  listInput.value?.focus();
  scrollListActive();
}
function closeList(refocus = true) {
  listOpen.value = false;
  if (refocus) ui.focusActiveView();
}
function pickList(t: Tab | undefined) {
  if (!t) return;
  tabs.activate(t.id, { focus: true });
  listOpen.value = false;
}
function scrollListActive() {
  nextTick(() => listEl.value?.querySelector<HTMLElement>(".li.on")?.scrollIntoView({ block: "nearest" }));
}
function onListKey(e: KeyboardEvent) {
  const n = listItems.value.length;
  if (e.key === "ArrowDown") { e.preventDefault(); listIdx.value = n ? (listIdx.value + 1) % n : 0; scrollListActive(); }
  else if (e.key === "ArrowUp") { e.preventDefault(); listIdx.value = n ? (listIdx.value - 1 + n) % n : 0; scrollListActive(); }
  else if (e.key === "Enter") { e.preventDefault(); pickList(listItems.value[listIdx.value]); }
  else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closeList(); }
}
watch(listQuery, () => (listIdx.value = 0));

// ---- context menu -----------------------------------------------------------------
const menu = ref<{ x: number; y: number; tab: Tab } | null>(null);
const menuItems = computed<MenuItem[]>(() => {
  const t = menu.value?.tab;
  if (!t) return [];
  const i = tabs.tabs.findIndex((x) => x.id === t.id);
  return [
    { id: "close", label: "Close", hint: kbd("⌘W") },
    { id: "others", label: "Close others", disabled: tabs.tabs.length < 2 },
    { id: "right", label: "Close to the right", disabled: i >= tabs.tabs.length - 1 },
    { id: "all", label: "Close all" },
    { separator: true },
    { id: "pin", label: t.pinned ? "Unpin" : "Pin" },
    ...(t.kind === "console" ? [{ id: "rename", label: "Rename…" }] : []),
    ...(t.kind === "table" ? [{ id: "structure", label: "Open structure" }] : []),
    ...(t.kind === "structure" ? [{ id: "data", label: "Open data" }] : []),
    { id: "reveal", label: "Reveal in sidebar" },
  ];
});
let menuTab: Tab | null = null;
watch(menu, (m) => m && (menuTab = m.tab));
async function runMenu(id: string) {
  const t = menuTab;
  if (!t) return;
  if (id === "close") await tabs.close(t.id);
  else if (id === "others") await tabs.closeOthers(t.id);
  else if (id === "right") {
    const i = tabs.tabs.findIndex((x) => x.id === t.id);
    for (const o of tabs.tabs.slice(i + 1)) if (!o.pinned) await tabs.close(o.id);
  } else if (id === "all") {
    for (const o of [...tabs.tabs]) if (!o.pinned) await tabs.close(o.id);
  } else if (id === "pin") tabs.update(t.id, { pinned: !t.pinned });
  else if (id === "rename") {
    tabs.activate(t.id);
    setTimeout(() => void startRename(t)); // after the menu closed and restored focus
  }
  else if (id === "structure" && t.table) tabs.openStructure(t.connId, t.schema ?? "", t.table);
  else if (id === "data" && t.table) tabs.openTable(t.connId, t.schema ?? "", t.table);
  else if (id === "reveal") ui.revealInSidebar(t.connId, t.schema, t.table);
}

</script>

<template>
  <div class="flex-none">
    <div class="flex border-b border-bd bg-bg2" :style="{ height: 'var(--tabbar-h)' }">
      <div ref="strip" class="strip relative flex min-w-0 flex-1 overflow-x-auto overflow-y-hidden" @wheel.passive="onWheel" role="tablist">
        <div
          v-for="(t, i) in tabs.tabs"
          :key="t.id"
          class="tab group"
          :class="{ act: t.id === tabs.activeId, over: overIdx === i && dragIdx !== i, dragging: dragIdx === i }"
          :data-active="t.id === tabs.activeId"
          :style="{ '--conn': projects.connColor(t.connId) }"
          :data-accent="connOf(t)?.color !== 'none' ? connOf(t)?.color : undefined"
          role="tab"
          :aria-selected="t.id === tabs.activeId"
          :title="`${t.title}${connOf(t) ? ' — ' + connOf(t)!.name : ''}`"
          :draggable="renaming !== t.id"
          @click="onTabClick($event, t)"
          @dblclick="onTabDblClick(t)"
          @mousedown.middle.prevent
          @auxclick="onAuxClick($event, t)"
          @contextmenu.prevent="menu = { x: $event.clientX, y: $event.clientY, tab: t }"
          @dragstart="onDragStart($event, i)"
          @dragover="onDragOver($event, i)"
          @drop="onDrop($event, i)"
          @dragend="onDragEnd"
        >
          <span class="conn-dot" />
          <Icon :name="icon[t.kind]" class="dim" />
          <input
            v-if="renaming === t.id" v-model="renameText" class="rename" spellcheck="false" aria-label="Tab name"
            @keydown="onRenameKey" @blur="endRename(true)" @click.stop @dblclick.stop @mousedown.stop
          />
          <span v-else class="title truncate" :class="{ mono: t.kind !== 'console', preview: t.preview }">{{ t.title }}</span>
          <span v-if="connOf(t)?.readOnly" :class="isRed(t) ? 'text-danger' : 'dim'" title="Read-only"><Icon name="lock" :size="10" /></span>
          <span v-if="pending(t) > 0" class="bdg" :title="`${pending(t)} pending change(s)`">{{ pending(t) }}</span>
          <span v-else-if="t.dirty" class="dirty" title="Unsaved changes" />
          <button class="x" tabindex="-1" :title="kbd('Close (⌘W)')" @click.stop="tabs.close(t.id)"><Icon name="x" :size="10" /></button>
        </div>
      </div>
      <button v-if="overflowing" class="btn ghost icon no-drag !my-[2px] flex-none" title="All tabs" aria-label="All tabs" @click="openList">
        <Icon name="chev" class="rotate-90" />
      </button>
      <button class="btn ghost no-drag !mx-1 !my-[3px] flex-none" :title="kbd('New SQL console (⌘T)')" @click="newConsole">
        <Icon name="plus" /> SQL
      </button>
    </div>

    <Teleport to="body">
      <div v-if="listOpen" class="fixed inset-0" :style="{ zIndex: 'var(--z-menu)' }" @mousedown.self="closeList()">
        <div class="tablist no-drag" :style="{ left: listPos.x + 'px', top: listPos.y + 'px' }" role="listbox">
          <input ref="listInput" v-model="listQuery" class="input !h-6 w-full" placeholder="Filter tabs…" spellcheck="false" @keydown="onListKey" />
          <div ref="listEl" class="max-h-[320px] overflow-auto pt-1">
            <div
              v-for="(t, i) in listItems"
              :key="t.id"
              class="li"
              :class="{ on: i === listIdx, cur: t.id === tabs.activeId }"
              :style="{ '--conn': projects.connColor(t.connId) }"
              role="option"
              @mousemove="listIdx = i"
              @click="pickList(t)"
            >
              <span class="conn-dot" />
              <Icon :name="icon[t.kind]" class="dim" />
              <span class="truncate" :class="{ mono: t.kind !== 'console', preview: t.preview }">{{ t.title }}</span>
              <span class="dim ml-auto flex-none truncate text-[10.5px]">{{ connOf(t)?.name }}</span>
            </div>
            <div v-if="!listItems.length" class="dim px-2 py-2">No matching tabs.</div>
          </div>
        </div>
      </div>
    </Teleport>
    <ContextMenu v-if="menu" :x="menu.x" :y="menu.y" :items="menuItems" @select="runMenu" @close="menu = null" />
  </div>
</template>

<style scoped>
.strip { scrollbar-width: none; }
.strip::-webkit-scrollbar { display: none; }
.tab {
  position: relative; display: flex; flex: none; align-items: center; gap: 6px; min-width: 110px; max-width: 220px;
  padding: 0 6px 0 10px; margin: 0 1px; border-radius: var(--r) var(--r) 0 0;
  color: var(--dim); white-space: nowrap; cursor: default;
}
.tab:hover { background: var(--hov); color: var(--tx); }
.tab.act { background: var(--bg); color: var(--tx); }
.tab.act::before {
  content: ""; position: absolute; left: 8px; right: 8px; top: 0; height: 2px; border-radius: 0 0 2px 2px; background: var(--conn);
}
.tab.act::after { content: ""; position: absolute; left: 0; right: 0; bottom: -1px; height: 1px; background: var(--bg); }
.tab.over { box-shadow: inset 2px 0 0 var(--ac); }
.tab.dragging { opacity: .5; }
.title { min-width: 0; }
.rename {
  min-width: 0; width: 100%; height: 18px; padding: 0 4px; border: 1px solid var(--ac); border-radius: 3px;
  background: var(--bg); color: var(--tx); font: inherit; outline: none;
}
.title.preview, .preview { font-style: italic; }
.tablist {
  position: fixed; width: 300px; padding: 6px; border: 1px solid var(--bd2); border-radius: 10px; background: var(--pop);
  box-shadow: 0 14px 40px var(--shadow);
}
.li { display: flex; align-items: center; gap: 7px; height: 26px; padding: 0 8px; border-radius: 7px; cursor: default; }
.li.on { background: var(--selbg); }
.li.cur { font-weight: 600; }
.x {
  display: inline-flex; align-items: center; justify-content: center; width: 16px; height: 16px;
  border: 0; border-radius: 3px; background: none; color: var(--dim); opacity: 0; padding: 0;
}
.tab:hover .x, .tab.act .x { opacity: 1; }
.x:hover { background: var(--bg3); color: var(--tx); }
.bdg { background: var(--ac); color: var(--ac-fg, #fff); border-radius: 8px; padding: 0 5px; font-size: 10px; line-height: 14px; }
.dirty { width: 7px; height: 7px; border-radius: 50%; background: var(--warn); flex: none; }
</style>
