<script setup lang="ts">
// App shell (owned by the "shell" agent): title bar, sidebar, tab area, status bar,
// overlays. Global shortcuts live here; view-local shortcuts live in the views and
// only fire when their tab is active.
import { computed, onBeforeUnmount, onMounted, watch } from "vue";
import { kbd } from "@/lib/kbd";
import { useProjectsStore } from "@/stores/projects";
import { useTabsStore } from "@/stores/tabs";
import { useUiStore } from "@/stores/ui";
import Sidebar from "@/components/sidebar/Sidebar.vue";
import TabBar from "@/components/tabs/TabBar.vue";
import StatusBar from "@/components/statusbar/StatusBar.vue";
import CommandPalette from "@/components/palette/CommandPalette.vue";
import ConnectionModal from "@/components/connection/ConnectionModal.vue";
import ConfirmModal from "@/components/common/ConfirmModal.vue";
import PasswordModal from "@/components/common/PasswordModal.vue";
import PassphraseModal from "@/components/common/PassphraseModal.vue";
import SettingsModal from "@/components/common/SettingsModal.vue";
import ShortcutsModal from "@/components/common/ShortcutsModal.vue";
import { loadRecent, pruneRecent, pushRecent } from "@/lib/recent";
import Modal from "@/components/common/Modal.vue";
import Toasts from "@/components/common/Toasts.vue";
import Icon from "@/components/common/Icon.vue";
import TableView from "@/components/table/TableView.vue";
import SqlConsole from "@/components/console/SqlConsole.vue";
import StructureView from "@/components/structure/StructureView.vue";
import { registerDataModals } from "@/components/table/registerModals";
import { isMod } from "@/components/common/keys";

const projects = useProjectsStore();
const tabs = useTabsStore();
const ui = useUiStore();

const activeConn = computed(() => (tabs.active ? projects.getConnection(tabs.active.connId) : undefined));
const titleText = computed(() => {
  const t = tabs.active;
  if (!t) return projects.activeProject?.name ?? "DB Viewer";
  return `${projects.activeProject?.name ?? ""} — ${activeConn.value?.name ?? ""}${t.table ? " · " + t.table : ""}`;
});
const themeIcon = computed(() => (ui.theme === "system" ? "monitor" : ui.theme === "dark" ? "moon" : "sun"));

function accentOf(connId: string): string | undefined {
  const c = projects.getConnection(connId)?.color;
  return c && c !== "none" ? c : undefined;
}

registerDataModals();
ui.registerModal("passphrase", PassphraseModal);
ui.registerModal("settings", SettingsModal);
ui.registerModal("shortcuts", ShortcutsModal);

// Recently opened tables (empty Cmd+K lists them first).
watch(() => projects.activeProjectId, (id) => id && void loadRecent(id), { immediate: true });
watch(
  () => tabs.active,
  (t) => {
    // the old project's tabs are still shown while a project switch restores the new ones
    if (projects.getConnection(t?.connId ?? "")?.projectId !== projects.activeProjectId) return;
    if (t?.table && (t.kind === "table" || t.kind === "structure")) pushRecent({ kind: t.kind, connId: t.connId, schema: t.schema ?? "", table: t.table });
  },
);
watch(() => projects.connections.length, () => pruneRecent((id) => !!projects.getConnection(id)));


onMounted(async () => {
  await ui.loadPrefs();
  await projects.init();
});

// Restore / switch the tab set whenever the active project changes.
watch(
  () => [projects.loaded, projects.activeProjectId] as const,
  async ([loaded, id]) => {
    if (!loaded || !id) return;
    const ok = await tabs.switchProject(id, (connId) => !!projects.getConnection(connId));
    if (!ok && tabs.currentProjectId) await projects.setActiveProject(tabs.currentProjectId);
  },
  { immediate: true },
);

// ---- global shortcuts ---------------------------------------------------------------
function newConsole() {
  const id = tabs.active?.connId ?? projects.projectConnections[0]?.id;
  if (id) tabs.openConsole(id);
  else ui.toast("Create a connection first", "info");
}

/** After a shortcut changes the active tab, keep the keyboard flow: focus the new view (or the tree if none). */
function refocus() {
  setTimeout(() => ui.focusActiveView(), 40);
}

function onKeyCapture(e: KeyboardEvent) {
  const mod = isMod(e);
  const key = e.key.toLowerCase();
  if (mod && !e.altKey && key === "k" && !e.shiftKey) {
    e.preventDefault();
    e.stopPropagation();
    if (ui.modalStack.length || ui.overlayOpen && !ui.paletteOpen) return;
    ui.togglePalette();
    return;
  }
  // Zoom works everywhere, also with a dialog open.
  if (mod && !e.altKey && (e.key === "=" || e.key === "+")) return void (e.preventDefault(), ui.zoomIn());
  if (mod && !e.altKey && (e.key === "-" || e.key === "_")) return void (e.preventDefault(), ui.zoomOut());
  if (mod && !e.altKey && !e.shiftKey && e.key === "0") return void (e.preventDefault(), ui.zoomReset());
  if (ui.overlayOpen) return; // dialogs, palette and <Modal>s own the keyboard
  if (mod && !e.altKey && !e.shiftKey && e.key === ",") {
    e.preventDefault();
    e.stopPropagation();
    ui.openModal("settings");
    return;
  }
  const target = e.target as HTMLElement | null;
  const inText = !!target?.closest("input, textarea, select, [contenteditable='true'], .cm-editor");
  // Mod+/ in the SQL editor toggles a comment; a typed "?" in the grid starts editing a cell and in
  // the sidebar tree extends the speed search
  const typing = inText || !!target?.closest(".dg, [role='tree']");
  if ((mod && !e.altKey && e.key === "/" && !target?.closest(".cm-editor")) || (e.key === "?" && !mod && !e.altKey && !typing)) {
    e.preventDefault();
    e.stopPropagation();
    ui.openModal("shortcuts");
    return;
  }
  if (mod && !e.altKey && !e.shiftKey && key === "t") {
    e.preventDefault();
    e.stopPropagation();
    newConsole();
  } else if (mod && !e.altKey && !e.shiftKey && key === "w") {
    e.preventDefault();
    e.stopPropagation();
    if (tabs.activeId) void tabs.close(tabs.activeId).then(refocus);
  } else if (mod && e.shiftKey && !e.altKey && key === "f") {
    e.preventDefault();
    e.stopPropagation();
    ui.openModal("findInDatabase", { connId: tabs.active?.connId ?? "" });
  } else if (isMod(e) && !e.shiftKey && !e.altKey && /^[1-9]$/.test(e.key)) {
    e.preventDefault();
    tabs.activateNth(Number(e.key));
    refocus();
  } else if (e.ctrlKey && e.key === "Tab") {
    e.preventDefault();
    tabs.cycle(e.shiftKey ? -1 : 1);
    refocus();
  } else if (isMod(e) && e.shiftKey && (e.key === "]" || e.key === "}")) {
    e.preventDefault();
    tabs.cycle(1);
    refocus();
  } else if (isMod(e) && e.shiftKey && (e.key === "[" || e.key === "{")) {
    e.preventDefault();
    tabs.cycle(-1);
    refocus();
  } else if (isMod(e) && !e.shiftKey && !e.altKey && e.key === "[") {
    e.preventDefault();
    tabs.goBack();
    refocus();
  } else if (isMod(e) && !e.shiftKey && !e.altKey && e.key === "]") {
    e.preventDefault();
    tabs.goForward();
    refocus();
  } else if (isMod(e) && !e.shiftKey && !e.altKey && key === "b") {
    e.preventDefault();
    ui.toggleSidebar();
  } else if (isMod(e) && !e.shiftKey && !e.altKey && key === "e") {
    // focus the sidebar tree (speed search)
    e.preventDefault();
    ui.focusSidebar();
  }
}
// Mouse back / forward buttons (3 / 4): navigate history, never the page itself.
function onMouseNav(e: MouseEvent) {
  if (e.button !== 3 && e.button !== 4) return;
  e.preventDefault();
  e.stopPropagation();
  if (e.type !== "mouseup" || ui.overlayOpen) return;
  if (e.button === 3) tabs.goBack();
  else tabs.goForward();
  refocus();
}
// Guard against the webview's own history navigation.
function onPopState() {
  history.pushState(null, "", location.href);
}
onMounted(() => {
  window.addEventListener("keydown", onKeyCapture, true);
  for (const t of ["mousedown", "mouseup", "auxclick"]) window.addEventListener(t, onMouseNav as EventListener, true);
  history.pushState(null, "", location.href);
  window.addEventListener("popstate", onPopState);
});
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onKeyCapture, true);
  for (const t of ["mousedown", "mouseup", "auxclick"]) window.removeEventListener(t, onMouseNav as EventListener, true);
  window.removeEventListener("popstate", onPopState);
});

// Disable the webview's default context menu outside of text fields.
function onContext(e: MouseEvent) {
  const t = e.target as HTMLElement;
  if (!t.closest("input, textarea, .selectable, .cm-editor")) e.preventDefault();
}
</script>

<template>
  <div class="flex h-full flex-col" @contextmenu="onContext">
    <!-- Title bar: macOS traffic lights overlay the left inset; the bar is a drag region. -->
    <header
      class="drag relative flex flex-none items-center border-b border-bd bg-bg2"
      :style="{ height: 'var(--titlebar-h)', paddingLeft: 'var(--traffic-w)' }"
    >
      <button class="btn ghost no-drag" :title="kbd('Toggle sidebar (⌘B)')" @click="ui.toggleSidebar()"><Icon name="sidebar" :size="14" /></button>
      <div class="pointer-events-none absolute inset-x-0 text-center dim" :style="{ paddingLeft: '200px', paddingRight: '200px' }">
        <span class="block truncate">{{ titleText }}</span>
      </div>
      <div class="flex-1" />
      <div class="no-drag flex items-center gap-1 pr-3">
        <button class="btn ghost" @click="ui.openPalette()"><Icon name="search" /> Jump to <kbd>{{ kbd("⌘K") }}</kbd></button>
        <button class="btn ghost" :title="`Theme: ${ui.theme} (click to cycle: system, dark, light)`" @click="ui.cycleTheme()">
          <Icon :name="themeIcon" :size="14" />
        </button>
        <button class="btn ghost" :title="kbd('Keyboard shortcuts (⌘/)')" aria-label="Keyboard shortcuts" @click="ui.openModal('shortcuts')">
          <Icon name="keyboard" :size="14" />
        </button>
        <button class="btn ghost" :title="kbd('Settings (⌘,)')" aria-label="Settings" @click="ui.openModal('settings')">
          <Icon name="gear" :size="14" />
        </button>
      </div>
    </header>

    <div class="flex min-h-0 flex-1">
      <Sidebar />
      <main class="flex min-w-0 flex-1 flex-col">
        <TabBar />
        <div class="relative min-h-0 flex-1">
          <template v-for="t in tabs.tabs" :key="t.id">
            <div v-show="t.id === tabs.activeId" data-tab-view :data-tab-id="t.id" :data-active="t.id === tabs.activeId" class="absolute inset-0 flex flex-col" :data-accent="accentOf(t.connId)">
              <TableView v-if="t.kind === 'table'" :tab="t" :active="t.id === tabs.activeId" />
              <SqlConsole v-else-if="t.kind === 'console'" :tab="t" :active="t.id === tabs.activeId" />
              <StructureView v-else-if="t.kind === 'structure'" :tab="t" :active="t.id === tabs.activeId" />
            </div>
          </template>
          <div v-if="!tabs.tabs.length" class="flex h-full flex-col items-center justify-center gap-3 text-center">
            <div class="dim">Open a table from the sidebar, or jump to anything with <kbd>{{ kbd("⌘K") }}</kbd>.</div>
            <div class="flex gap-2">
              <button class="btn" @click="ui.openPalette()"><Icon name="search" /> Jump to…</button>
              <button class="btn" @click="ui.openModal('connection', { projectId: projects.activeProjectId })"><Icon name="plus" /> New connection</button>
            </div>
            <div class="dim text-[11px]">In the sidebar, just start typing to search tables and columns. Press <kbd>{{ kbd("⌘/") }}</kbd> for all shortcuts.</div>
          </div>
        </div>
      </main>
    </div>

    <StatusBar />

    <CommandPalette v-if="ui.paletteOpen" />
    <!-- Modal stack: every entry stays mounted, the last one is on top (confirm over a form keeps the form). -->
    <template v-for="m in ui.modalStack" :key="m.id">
      <ConnectionModal v-if="m.name === 'connection'" v-bind="m.props" />
      <ConfirmModal v-else-if="m.name === 'confirm'" v-bind="m.props as any" />
      <PasswordModal v-else-if="m.name === 'password'" v-bind="m.props as any" />
      <component :is="ui.modalComponents[m.name]" v-else-if="ui.modalComponents[m.name]" v-bind="m.props" />
      <Modal v-else :title="m.name" :width="380" @close="ui.closeModal()">
        <div class="dim">This dialog is not available yet.</div>
        <template #footer><button class="btn" @click="ui.closeModal()">Close</button></template>
      </Modal>
    </template>
    <Toasts />
  </div>
</template>
