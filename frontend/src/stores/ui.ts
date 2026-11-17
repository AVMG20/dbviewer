// UI store (owned by the "shell" agent): theme, toasts, command palette, modals,
// layout, status bar info.
//
// ============================== PUBLIC API (stable) ==============================
//   theme: "dark" | "light" | "system"; effectiveTheme; setTheme(t); cycleTheme()
//   toast(message, kind = "info", timeout?) -> id   kinds: info|success|warning|error
//        (default timeout 3s, errors 6s, 0 = sticky). dismissToast(id)
//   paletteOpen, openPalette(), closePalette(), togglePalette()
//   modal: { name, props } | null; openModal(name, props), closeModal()
//        MODAL STACK: openModal() replaces the stack with a new base dialog; confirm() and
//        askPassword() push on top of whatever is open, so Cancel returns to the dialog
//        underneath intact. closeModal() closes the topmost. `modal` is the topmost one,
//        `modalStack` the whole stack.
//        built-in names rendered by the shell: connection, password, confirm.
//        Other names (saveChanges, export, findInDatabase, ...) are rendered by the
//        component registered with registerModal(name, Component); the component gets
//        the modal props as component props and an `onClose`-less contract: call
//        ui.closeModal() itself. Use <Modal> from components/common/Modal.vue as shell.
//   registerModal(name, component)      e.g. in TableView: ui.registerModal("export", ExportModal)
//   confirm({title, message, sql?, confirmText?, danger?}) -> Promise<boolean>
//   askPassword(connId) -> Promise<string | null>   (null = cancelled)
//   askPassphrase({keyPath, keyType?, fingerprint?}) -> Promise<boolean>  (SSH key passphrase; true = provided)
//   overlayOpen (reactive boolean): true while any modal, the palette or a <Modal> is open.
//        Watch it to close popovers / ignore shortcuts. Local dialogs built on common/Modal.vue
//        count automatically (registerOverlay()).
//   focusActiveView() / restoreFocus(prevEl): focus the active tab's [data-focus-target] / .dg /
//        CodeMirror (else the sidebar tree). Call after closing dialogs.
//   setStatus({ tabId?, durationMs?, rows?, total?, message? })
//        Status bar info for a tab (defaults to the active tab). Data views call it
//        after each load / query, e.g. ui.setStatus({ tabId: tab.id, durationMs: r.durationMs, rows: r.rows.length, total: r.total }).
//        Fields not given keep their previous value; clearStatus(tabId) resets.
//   sidebarWidth, sidebarVisible, toggleSidebar(); loadPrefs() (called by App)
//   revealRequest / revealInSidebar(connId, schema?, table?): expands the tree to the object
//   focusSidebar(): moves keyboard focus to the sidebar tree
//   refreshConnection(connId): reloads the sidebar tree + search index of a connection
// =================================================================================
import { defineStore } from "pinia";
import { computed, markRaw, ref, shallowRef, watch, type Component } from "vue";
import { projectApi } from "@/api";
import type { ConfirmOptions, ModalName, ModalState, Theme, Toast, ToastKind } from "@/types";

let toastSeq = 0;
const PREFS_KEY = "ui:prefs";

export interface StatusInfo {
  durationMs?: number;
  rows?: number;
  /** total row count when known (-1 / undefined = unknown) */
  total?: number;
  message?: string;
}

export interface RevealRequest {
  seq: number;
  connId: string;
  schema?: string;
  table?: string;
}

export const useUiStore = defineStore("ui", () => {
  // ---- theme -------------------------------------------------------------
  const theme = ref<Theme>("system");
  const mq = window.matchMedia?.("(prefers-color-scheme: dark)");
  const systemDark = ref(mq?.matches ?? true);
  /** Effective theme applied to <html data-theme>. */
  const effectiveTheme = computed<"dark" | "light">(() =>
    theme.value === "system" ? (systemDark.value ? "dark" : "light") : theme.value,
  );
  function applyTheme() {
    document.documentElement.dataset.theme = effectiveTheme.value;
  }
  function setTheme(t: Theme) {
    theme.value = t;
    applyTheme();
  }
  function cycleTheme() {
    const order: Theme[] = ["system", "dark", "light"];
    setTheme(order[(order.indexOf(theme.value) + 1) % order.length]);
  }
  mq?.addEventListener("change", (e) => {
    systemDark.value = e.matches;
    applyTheme();
  });

  // ---- toasts --------------------------------------------------------------
  const toasts = ref<Toast[]>([]);
  /** Repeat counts of collapsed identical toasts ("... ×5"), by toast id. */
  const toastCounts = ref<Record<number, number>>({});
  const toastTimers = new Map<number, ReturnType<typeof setTimeout>>();
  function toast(message: string, kind: ToastKind = "info", timeout?: number): number {
    const t = timeout ?? (kind === "error" ? 6000 : 3000);
    const same = toasts.value.find((x) => x.message === message && x.kind === kind);
    const id = same?.id ?? ++toastSeq;
    if (same) {
      toastCounts.value = { ...toastCounts.value, [id]: (toastCounts.value[id] ?? 1) + 1 };
      clearTimeout(toastTimers.get(id));
    } else {
      toasts.value.push({ id, message, kind, timeout: t });
      if (toasts.value.length > 5) dismissToast(toasts.value[0].id);
    }
    if (t > 0) toastTimers.set(id, setTimeout(() => dismissToast(id), t));
    return id;
  }
  function dismissToast(id: number) {
    toasts.value = toasts.value.filter((t) => t.id !== id);
    clearTimeout(toastTimers.get(id));
    toastTimers.delete(id);
    const { [id]: _c, ...rest } = toastCounts.value;
    toastCounts.value = rest;
  }

  // ---- command palette (Cmd+K) --------------------------------------------
  const paletteOpen = ref(false);
  function openPalette() {
    paletteOpen.value = true;
  }
  function closePalette() {
    paletteOpen.value = false;
  }
  function togglePalette() {
    paletteOpen.value = !paletteOpen.value;
  }

  // ---- modals (stack) ------------------------------------------------------
  interface StackEntry extends ModalState {
    id: number;
  }
  let entrySeq = 0;
  const modalStack = ref<StackEntry[]>([]);
  /** The topmost modal (what used to be the single modal). */
  const modal = computed<ModalState | null>(() => modalStack.value[modalStack.value.length - 1] ?? null);
  const modalComponents = shallowRef<Record<string, Component>>({});
  const pending = new Map<number, (v: unknown) => void>();

  function pushEntry(name: ModalName, props: Record<string, unknown>): StackEntry {
    const e: StackEntry = { id: ++entrySeq, name, props };
    modalStack.value = [...modalStack.value, e];
    return e;
  }
  function removeEntry(id: number, result?: unknown) {
    modalStack.value = modalStack.value.filter((e) => e.id !== id);
    const r = pending.get(id);
    pending.delete(id);
    r?.(result);
  }
  /** Opens a modal as the base dialog (replaces the current stack; pending confirms resolve false). */
  function openModal(name: ModalName, props: Record<string, unknown> = {}) {
    for (const e of [...modalStack.value]) removeEntry(e.id, e.name === "password" ? null : false);
    pushEntry(name, props);
  }
  /** Closes the topmost modal (a pending confirm resolves false, a password prompt null). */
  function closeModal() {
    const top = modalStack.value[modalStack.value.length - 1];
    if (top) removeEntry(top.id, top.name === "password" ? null : false);
  }
  /** Registers the component that renders a modal name (saveChanges, export, findInDatabase, ...). */
  function registerModal(name: ModalName | string, component: Component) {
    modalComponents.value = { ...modalComponents.value, [name]: markRaw(component) };
  }

  // ---- overlays (any dialog / palette) -------------------------------------
  const overlayCount = ref(0);
  /** Local overlays (e.g. a <Modal> rendered by a component) call this; returns the unregister fn. */
  function registerOverlay(): () => void {
    overlayCount.value++;
    let done = false;
    return () => {
      if (!done) {
        done = true;
        overlayCount.value--;
      }
    };
  }
  /** True while a modal, the palette or any <Modal> is open: global shortcuts should stand down. */
  const overlayOpen = computed(() => modalStack.value.length > 0 || paletteOpen.value || overlayCount.value > 0);

  // ---- confirm dialog (stacks on top of the open modal) ----------------------
  function confirm(opts: ConfirmOptions): Promise<boolean> {
    return new Promise((resolve) => {
      const e = pushEntry("confirm", { ...opts });
      pending.set(e.id, (v) => resolve(v === true));
    });
  }
  /** Called by the confirm modal component (resolves the topmost confirm). */
  function resolveConfirm(ok: boolean) {
    const top = [...modalStack.value].reverse().find((e) => e.name === "confirm");
    if (top) removeEntry(top.id, ok);
  }

  // ---- password prompt (one prompt per connection) ---------------------------
  const pendingPw = new Map<string, Promise<string | null>>();
  function askPassword(connId: string): Promise<string | null> {
    const existing = pendingPw.get(connId);
    if (existing) return existing;
    const p = new Promise<string | null>((resolve) => {
      const e = pushEntry("password", { connId });
      pending.set(e.id, (v) => resolve(typeof v === "string" ? v : null));
    }).finally(() => pendingPw.delete(connId));
    pendingPw.set(connId, p);
    return p;
  }
  /** Called by the password modal component (resolves the topmost prompt). */
  function resolvePassword(pw: string | null) {
    const top = [...modalStack.value].reverse().find((e) => e.name === "password");
    if (top) removeEntry(top.id, pw);
  }

  // ---- SSH key passphrase prompt ------------------------------------------------
  /** Opens the passphrase dialog for an encrypted SSH key; resolves true once ProvideKeyPassphrase succeeded, false if cancelled. */
  function askPassphrase(prompt: { keyPath: string; keyType?: string; fingerprint?: string }): Promise<boolean> {
    return new Promise((resolve) => {
      const e = pushEntry("passphrase" as ModalName, { ...prompt });
      pending.set(e.id, (v) => resolve(v === true));
    });
  }
  /** Called by the passphrase dialog. */
  function resolvePassphrase(ok: boolean) {
    const top = [...modalStack.value].reverse().find((e) => (e.name as string) === "passphrase");
    if (top) removeEntry(top.id, ok);
  }

  // ---- focus helpers ----------------------------------------------------------
  /**
   * Focuses the active tab's main target: an element with [data-focus-target], else the
   * grid (.dg), else the CodeMirror content; falls back to the sidebar tree.
   */
  function focusActiveView() {
    const root = document.querySelector('[data-tab-view][data-active="true"]');
    const el = root?.querySelector<HTMLElement>("[data-focus-target], .dg, .cm-content");
    if (el) el.focus({ preventScroll: true });
    else document.querySelector<HTMLElement>('[role="tree"]')?.focus({ preventScroll: true });
  }
  /** Restores focus to `prev` if it is still in the document, else to the active view. */
  function restoreFocus(prev: Element | null) {
    if (prev instanceof HTMLElement && prev !== document.body && document.contains(prev)) prev.focus({ preventScroll: true });
    else focusActiveView();
  }

  // ---- status bar info -----------------------------------------------------
  /** Keyed by tab id; the status bar shows the active tab's entry. */
  const statusByTab = ref<Record<string, StatusInfo>>({});
  let activeTabGetter: () => string | null = () => null;
  /** Wired by the tabs store so setStatus() without tabId targets the active tab. */
  function _setActiveTabGetter(fn: () => string | null) {
    activeTabGetter = fn;
  }
  function setStatus(info: StatusInfo & { tabId?: string }) {
    const { tabId, ...rest } = info;
    const id = tabId ?? activeTabGetter();
    if (!id) return;
    statusByTab.value = { ...statusByTab.value, [id]: { ...statusByTab.value[id], ...rest } };
  }
  function clearStatus(tabId: string) {
    const { [tabId]: _, ...rest } = statusByTab.value;
    statusByTab.value = rest;
  }

  // ---- layout --------------------------------------------------------------
  const sidebarWidth = ref(268);
  const sidebarVisible = ref(true);
  function toggleSidebar() {
    sidebarVisible.value = !sidebarVisible.value;
  }

  const revealRequest = ref<RevealRequest | null>(null);
  let revealSeq = 0;
  function revealInSidebar(connId: string, schema?: string, table?: string) {
    sidebarVisible.value = true;
    revealRequest.value = { seq: ++revealSeq, connId, schema, table };
  }
  const refreshRequest = ref<{ seq: number; connId: string } | null>(null);
  let refreshSeq = 0;
  function refreshConnection(connId: string) {
    refreshRequest.value = { seq: ++refreshSeq, connId };
  }
  const focusSidebarSeq = ref(0);
  function focusSidebar() {
    sidebarVisible.value = true;
    focusSidebarSeq.value++;
  }

  // ---- persistence (ui:prefs) ----------------------------------------------
  let prefsLoaded = false;
  async function loadPrefs() {
    try {
      const raw = await projectApi.GetUIState(PREFS_KEY);
      if (raw) {
        const p = JSON.parse(raw);
        if (p.theme === "dark" || p.theme === "light" || p.theme === "system") theme.value = p.theme;
        if (typeof p.sidebarWidth === "number") sidebarWidth.value = Math.min(560, Math.max(180, p.sidebarWidth));
        if (typeof p.sidebarVisible === "boolean") sidebarVisible.value = p.sidebarVisible;
      }
    } catch {
      /* first run / backend not ready: keep defaults */
    }
    prefsLoaded = true;
    applyTheme();
  }
  let saveTimer: ReturnType<typeof setTimeout> | undefined;
  watch([theme, sidebarWidth, sidebarVisible], () => {
    if (!prefsLoaded) return;
    clearTimeout(saveTimer);
    saveTimer = setTimeout(() => {
      projectApi
        .SetUIState(PREFS_KEY, JSON.stringify({ theme: theme.value, sidebarWidth: sidebarWidth.value, sidebarVisible: sidebarVisible.value }))
        .catch(() => {});
    }, 400);
  });

  applyTheme();

  return {
    theme, effectiveTheme, setTheme, cycleTheme, applyTheme,
    toasts, toastCounts, toast, dismissToast,
    paletteOpen, openPalette, closePalette, togglePalette,
    modal, modalStack, openModal, closeModal, modalComponents, registerModal,
    overlayOpen, registerOverlay,
    confirm, resolveConfirm,
    askPassword, resolvePassword, askPassphrase, resolvePassphrase,
    focusActiveView, restoreFocus,
    statusByTab, setStatus, clearStatus, _setActiveTabGetter,
    sidebarWidth, sidebarVisible, toggleSidebar,
    revealRequest, revealInSidebar, focusSidebarSeq, focusSidebar, refreshRequest, refreshConnection,
    loadPrefs,
  };
});
