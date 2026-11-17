// Tabs store (owned by the "shell" agent).
//
// PREVIEW TABS (VS Code / PhpStorm style): openTable(..., { preview: true }) opens the table in
// a preview tab (italic title). Opening another table with preview:true REPLACES the current
// preview tab (at most one per project). A preview tab becomes permanent via:
//
//   tabs.promote(id: string): void     // sets tab.preview = false; no-op if already permanent
//
// Data views call tabs.promote(tab.id) on any deliberate interaction: WHERE / ORDER BY applied,
// column filter, sort, quick find, paging, a staged edit, column resize, record panel use.
// setDirty(id, true) promotes automatically. Console / structure tabs and openTable() without
// preview:true (FK navigation, ...) are always permanent. Double-clicking a tab promotes it.
// The flag is persisted with the tab state.
//
// FOCUS HANDOFF: opening / activating a tab does NOT move keyboard focus by default (a single
// click in the sidebar must keep focus in the tree so typing still starts speed search).
//   tabs.openTable(..., { focus: true })   tabs.openStructure(..., { focus: true })
//   tabs.activate(id, { focus: true })     request that the view takes focus once it is shown
//   tabs.consumeFocus(tabId): boolean      TableView / StructureView call this when they become
//                                          active (and after mounting): true exactly once if focus
//                                          was requested for that tab, then the request is cleared.
// Enter, double-click, palette, FK navigation and find-in-database pass focus: true.
//
// NAVIGATION HISTORY: every time a different tab becomes active a location is recorded
// (max 100, consecutive duplicates dropped, a new landing after going back truncates the forward
// stack). tabs.goBack() / tabs.goForward() return to the previous / next location: the tab is
// activated if it still exists, otherwise the table is reopened (as a preview tab when it
// originally was one). Closed consoles are skipped. canGoBack / canGoForward are reactive.
import { defineStore } from "pinia";
import { computed, ref, watch } from "vue";
import { newId, projectApi, queryApi } from "@/api";
import type { PersistedTabs, Tab } from "@/types";
import { useProjectsStore } from "./projects";
import { useUiStore } from "./ui";

export const useTabsStore = defineStore("tabs", () => {
  const tabs = ref<Tab[]>([]);
  const activeId = ref<string | null>(null);

  /** tab id that was asked to take focus (see consumeFocus) */
  const focusRequest = ref<string | null>(null);
  function consumeFocus(tabId: string): boolean {
    if (focusRequest.value !== tabId) return false;
    focusRequest.value = null;
    return true;
  }

  const active = computed(() => tabs.value.find((t) => t.id === activeId.value) ?? null);
  /** Project whose tabs are currently loaded (autosave target). */
  const currentProjectId = ref<string>("");
  useUiStore()._setActiveTabGetter(() => activeId.value);

  // ---- navigation history ----------------------------------------------------
  interface NavLoc {
    tabId: string;
    kind: Tab["kind"];
    connId: string;
    schema: string;
    table: string;
    preview: boolean;
  }
  const hist = ref<NavLoc[]>([]);
  const histPos = ref(-1);
  let navigating = false;
  const sameLoc = (a: NavLoc, b: NavLoc) =>
    a.tabId === b.tabId || (a.kind === b.kind && a.connId === b.connId && a.schema === b.schema && a.table === b.table && a.kind !== "console");

  function recordLocation() {
    if (navigating) return;
    const t = active.value;
    if (!t) return;
    const loc: NavLoc = { tabId: t.id, kind: t.kind, connId: t.connId, schema: t.schema ?? "", table: t.table ?? "", preview: !!t.preview };
    const cur = hist.value[histPos.value];
    if (cur && sameLoc(cur, loc)) {
      hist.value[histPos.value] = loc;
      return;
    }
    hist.value = [...hist.value.slice(0, histPos.value + 1), loc].slice(-100);
    histPos.value = hist.value.length - 1;
  }
  watch(activeId, recordLocation, { flush: "sync" });

  const canGoBack = computed(() => histPos.value > 0);
  const canGoForward = computed(() => histPos.value < hist.value.length - 1);

  /** Activates / reopens a location; false if it cannot be resolved (closed console, ...). */
  function resolveLoc(loc: NavLoc): boolean {
    if (tabs.value.some((t) => t.id === loc.tabId)) {
      activeId.value = loc.tabId;
      return true;
    }
    if (loc.kind === "console") return false;
    const same = tabs.value.find((t) => t.kind === loc.kind && t.connId === loc.connId && (t.schema ?? "") === loc.schema && t.table === loc.table);
    if (same) {
      activeId.value = same.id;
      return true;
    }
    if (!useProjectsStore().getConnection(loc.connId)) return false;
    if (loc.kind === "structure") openStructure(loc.connId, loc.schema, loc.table);
    else openTable(loc.connId, loc.schema, loc.table, { preview: loc.preview });
    return true;
  }

  function go(dir: 1 | -1) {
    let p = histPos.value + dir;
    navigating = true;
    try {
      while (p >= 0 && p < hist.value.length) {
        if (resolveLoc(hist.value[p])) {
          histPos.value = p;
          hist.value[p] = { ...hist.value[p], tabId: activeId.value ?? hist.value[p].tabId };
          return;
        }
        p += dir;
      }
    } finally {
      navigating = false;
    }
  }
  function goBack() {
    go(-1);
  }
  function goForward() {
    go(1);
  }

  function find(pred: (t: Tab) => boolean) {
    return tabs.value.find(pred);
  }

  /** Adds (or focuses an equal existing) tab and activates it. Returns the tab id. */
  function open(tab: Omit<Tab, "id"> & { id?: string }): string {
    const t: Tab = { ...tab, id: tab.id ?? newId() };
    tabs.value.push(t);
    activeId.value = t.id;
    return t.id;
  }

  /** Opens (or focuses) the data tab of a table. where/orderBy apply when given. */
  function openTable(
    connId: string,
    schema: string,
    table: string,
    opts: { where?: string; orderBy?: string; preview?: boolean; focus?: boolean } = {},
  ): string {
    const id = openTableInner(connId, schema, table, opts);
    if (opts.focus) focusRequest.value = id;
    return id;
  }
  function openTableInner(
    connId: string,
    schema: string,
    table: string,
    opts: { where?: string; orderBy?: string; preview?: boolean; focus?: boolean } = {},
  ): string {
    const { preview = false, focus: _focus, ...rest } = opts;
    const existing = find((t) => t.kind === "table" && t.connId === connId && (t.schema ?? "") === schema && t.table === table);
    if (existing) {
      if (rest.where !== undefined) existing.where = rest.where;
      if (rest.orderBy !== undefined) existing.orderBy = rest.orderBy;
      // opening an existing preview tab deliberately (double-click, where/orderBy) makes it permanent
      if (existing.preview && (!preview || rest.where !== undefined || rest.orderBy !== undefined)) existing.preview = false;
      activeId.value = existing.id;
      return existing.id;
    }
    if (preview) {
      const i = tabs.value.findIndex((t) => t.preview && !t.dirty);
      if (i >= 0) {
        const old = tabs.value[i];
        const t: Tab = { id: newId(), kind: "table", connId, schema, table, title: table, preview: true, ...rest };
        tabs.value.splice(i, 1, t);
        useUiStore().clearStatus(old.id);
        activeId.value = t.id;
        return t.id;
      }
    }
    return open({ kind: "table", connId, schema, table, title: table, ...(preview ? { preview: true } : {}), ...rest });
  }

  /** Makes a preview tab permanent (see the note at the top). */
  function promote(id: string) {
    const t = tabs.value.find((x) => x.id === id);
    if (t?.preview) t.preview = false;
  }

  /** Opens (or focuses) the structure tab of a table. */
  function openStructure(connId: string, schema: string, table: string, opts: { focus?: boolean } = {}): string {
    const id = openStructureInner(connId, schema, table);
    if (opts.focus) focusRequest.value = id;
    return id;
  }
  function openStructureInner(connId: string, schema: string, table: string): string {
    const existing = find((t) => t.kind === "structure" && t.connId === connId && (t.schema ?? "") === schema && t.table === table);
    if (existing) {
      activeId.value = existing.id;
      return existing.id;
    }
    return open({ kind: "structure", connId, schema, table, title: table });
  }

  /** Always opens a new SQL console for the connection. */
  function openConsole(connId: string, sql = "", schema = ""): string {
    const n = tabs.value.filter((t) => t.kind === "console" && t.connId === connId).length + 1;
    const id = open({ kind: "console", connId, schema, sql, title: n > 1 ? `Console ${n}` : "Console" });
    focusRequest.value = id; // a new console always takes focus (editor)
    return id;
  }

  function activate(id: string, opts: { focus?: boolean } = {}) {
    if (!tabs.value.some((t) => t.id === id)) return;
    activeId.value = id;
    if (opts.focus) focusRequest.value = id;
  }

  /** Closes a tab; asks for confirmation when dirty. Resolves false if the user cancelled. */
  async function close(id: string): Promise<boolean> {
    const i = tabs.value.findIndex((t) => t.id === id);
    if (i < 0) return true;
    const t = tabs.value[i];
    if (t.dirty) {
      const ok = await useUiStore().confirm({
        title: "Discard changes?",
        message: `"${t.title}" has unsaved changes. Close it and discard them?`,
        confirmText: "Discard",
        danger: true,
      });
      if (!ok) return false;
    }
    tabs.value.splice(i, 1);
    useUiStore().clearStatus(id);
    if (t.kind === "console") queryApi.CloseSession(id).catch(() => {});
    if (activeId.value === id) activeId.value = tabs.value[Math.min(i, tabs.value.length - 1)]?.id ?? null;
    return true;
  }

  async function closeOthers(id: string) {
    for (const t of [...tabs.value]) if (t.id !== id && !t.pinned) await close(t.id);
  }

  /** Closes every tab of a connection (e.g. after delete). No confirmation. */
  function closeConnection(connId: string) {
    tabs.value = tabs.value.filter((t) => t.connId !== connId);
    if (!tabs.value.some((t) => t.id === activeId.value)) activeId.value = tabs.value[0]?.id ?? null;
  }

  /** Moves a tab (drag reorder). */
  function move(from: number, to: number) {
    if (from === to || from < 0 || from >= tabs.value.length) return;
    const [t] = tabs.value.splice(from, 1);
    tabs.value.splice(Math.max(0, Math.min(to, tabs.value.length)), 0, t);
  }

  /** Activates the nth tab (1-based; 9 = last). */
  function activateNth(n: number) {
    const t = n >= 9 ? tabs.value[tabs.value.length - 1] : tabs.value[n - 1];
    if (t) activeId.value = t.id;
  }

  function setDirty(id: string, dirty: boolean) {
    const t = tabs.value.find((x) => x.id === id);
    if (t) {
      t.dirty = dirty;
      if (dirty) t.preview = false;
    }
  }

  function update(id: string, patch: Partial<Omit<Tab, "id">>) {
    const t = tabs.value.find((x) => x.id === id);
    if (t) Object.assign(t, patch);
  }

  /** Cycles tabs (Ctrl+Tab / Ctrl+Shift+Tab). */
  function cycle(dir: 1 | -1) {
    if (!tabs.value.length) return;
    const i = tabs.value.findIndex((t) => t.id === activeId.value);
    activeId.value = tabs.value[(i + dir + tabs.value.length) % tabs.value.length].id;
  }

  /** Persist / restore open tabs per project (UI state key "tabs:<projectId>"). */
  async function save(projectId: string) {
    const data: PersistedTabs = { tabs: tabs.value.map((t) => ({ ...t, dirty: false })), activeId: activeId.value };
    await projectApi.SetUIState(`tabs:${projectId}`, JSON.stringify(data));
  }
  async function restore(projectId: string, validConn?: (connId: string) => boolean) {
    let data: PersistedTabs = { tabs: [], activeId: null };
    try {
      const raw = await projectApi.GetUIState(`tabs:${projectId}`);
      if (raw) data = JSON.parse(raw);
    } catch {
      /* ignore corrupt state */
    }
    const list = (data.tabs ?? []).filter((t) => !validConn || validConn(t.connId));
    tabs.value = list;
    activeId.value = list.some((t) => t.id === data.activeId) ? data.activeId : (list[0]?.id ?? null);
  }

  /**
   * Switches the tab set to another project: saves the current project's tabs, then
   * restores the new project's. Called by App.vue when the active project changes.
   * Also (re)arms autosave. Dirty tabs of the old project are discarded (the user is
   * asked before switching away from dirty tabs: returns false when cancelled).
   */
  async function switchProject(projectId: string, validConn?: (connId: string) => boolean): Promise<boolean> {
    if (currentProjectId.value === projectId) return true;
    if (currentProjectId.value) {
      if (tabs.value.some((t) => t.dirty)) {
        const ok = await useUiStore().confirm({
          title: "Discard changes?",
          message: "Some tabs have unsaved changes. Switch project and discard them?",
          confirmText: "Discard",
          danger: true,
        });
        if (!ok) return false;
      }
      const old = currentProjectId.value;
      currentProjectId.value = "";
      await save(old).catch(() => {});
    }
    navigating = true;
    await restore(projectId, validConn);
    navigating = false;
    hist.value = [];
    histPos.value = -1;
    recordLocation();
    currentProjectId.value = projectId;
    return true;
  }

  /**
   * Switches the active project, asking first when open tabs have unsaved changes
   * (the project does not change until the question is answered). Resolves false if cancelled.
   */
  async function requestProjectSwitch(projectId: string): Promise<boolean> {
    const projects = useProjectsStore();
    if (projects.activeProjectId === projectId) return true;
    if (tabs.value.some((t) => t.dirty)) {
      const ok = await useUiStore().confirm({
        title: "Discard changes?",
        message: "Some tabs have unsaved changes. Switch project and discard them?",
        confirmText: "Discard",
        danger: true,
      });
      if (!ok) return false;
      for (const t of tabs.value) t.dirty = false; // answered: switchProject() will not ask again
    }
    await projects.setActiveProject(projectId);
    return true;
  }

  let saveTimer: ReturnType<typeof setTimeout> | undefined;
  watch(
    () => [tabs.value.map((t) => [t.id, t.kind, t.connId, t.schema, t.table, t.title, t.where, t.orderBy, t.sql, t.pinned, t.preview]), activeId.value],
    () => {
      const pid = currentProjectId.value;
      if (!pid) return;
      clearTimeout(saveTimer);
      saveTimer = setTimeout(() => {
        if (currentProjectId.value === pid) save(pid).catch(() => {});
      }, 500);
    },
    { deep: true },
  );

  return {
    tabs, activeId, active,
    open, openTable, openStructure, openConsole, activate, close, closeOthers, closeConnection,
    consumeFocus, goBack, goForward, canGoBack, canGoForward,
    promote, move, activateNth, setDirty, update, cycle, save, restore, switchProject, requestProjectSwitch, currentProjectId,
  };
});
