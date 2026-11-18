// Projects + connections store (owned by the "shell" agent).
// Baseline implementation; the shell agent may extend it but must keep the
// public API below stable (the data agent uses getConnection/isReadOnly/connColor/status).
import { defineStore } from "pinia";
import { computed, ref } from "vue";
import {
  apiError,
  connectionApi,
  onConnStatus,
  onSshHostKey,
  onSshPassphrase,
  type HostKeyPrompt,
  type SSHKeyPrompt,
  projectApi,
  type ConnectionConfig,
  type ConnectionStatus,
  type Project,
  type TestResult,
} from "@/api";
import { friendlyError } from "@/components/common/errors";
import { useUiStore } from "./ui";

const ACTIVE_PROJECT_KEY = "ui:activeProject";
/** Ids of "ask" connections that connected with an empty password (tried without asking next time). */
const NO_PASSWORD_KEY = "ui:noPassword";

/** A local database without SSH (socket or loopback host): worth a try without a password. */
function isLocal(c: ConnectionConfig): boolean {
  if (c.ssh.enabled) return false;
  if (c.socket) return true;
  return ["", "localhost", "127.0.0.1", "::1"].includes(c.host.trim().toLowerCase());
}

export const useProjectsStore = defineStore("projects", () => {
  const projects = ref<Project[]>([]);
  /** All connections of all projects. */
  const connections = ref<ConnectionConfig[]>([]);
  /** Live status per connection id (kept in sync with the "conn:status" event). */
  const statuses = ref<Record<string, ConnectionStatus>>({});
  const activeProjectId = ref<string>("");
  const loaded = ref(false);

  const activeProject = computed(() => projects.value.find((p) => p.id === activeProjectId.value) ?? null);
  /** Connections of the active project, in sort order. */
  const projectConnections = computed(() =>
    connections.value.filter((c) => c.projectId === activeProjectId.value).sort((a, b) => a.sortOrder - b.sortOrder),
  );

  function getConnection(id: string): ConnectionConfig | undefined {
    return connections.value.find((c) => c.id === id);
  }
  function isReadOnly(id: string): boolean {
    return getConnection(id)?.readOnly ?? false;
  }
  function isConnected(id: string): boolean {
    return statuses.value[id]?.connected ?? false;
  }
  /** CSS color for a connection ("transparent" for none), e.g. for style="--conn: ...". */
  function connColor(id: string): string {
    const c = getConnection(id)?.color ?? "none";
    return `var(--conn-${c || "none"})`;
  }

  let unsubscribe: (() => void) | null = null;
  let unsubHostKey: (() => void) | null = null;
  /** Latest "ssh:hostkey" event (host + fingerprint of an unknown SSH host). */
  let lastHostKey: HostKeyPrompt | null = null;
  let unsubPass: (() => void) | null = null;
  /** Latest "ssh:passphrase" event (key file that needs its passphrase). */
  let lastKeyPrompt: SSHKeyPrompt | null = null;

  /** Parses host / fingerprint out of a host_key_unknown error message when no event was seen. */
  function parseHostKey(message: string): HostKeyPrompt | null {
    const m = /for (\S+): (\S+) (SHA256:\S+)/.exec(message);
    return m ? { connId: "", host: m[1], keyType: m[2], fingerprint: m[3] } : null;
  }

  /**
   * Runs `fn`; when it rejects with passphrase_required asks for the key passphrase (ProvideKeyPassphrase) and retries;
   * when it rejects with host_key_unknown asks "Trust this host?", stores the key
   * with TrustHostKey and retries. host_key_mismatch is a hard, sticky red error (no trust option).
   * Other errors are rethrown unchanged.
   */
  async function withHostKeyTrust<T>(fn: () => Promise<T>): Promise<T> {
    const ui = useUiStore();
    for (let attempt = 0; ; attempt++) {
      try {
        return await fn();
      } catch (e) {
        const err = apiError(e);
        if (err.code === "host_key_mismatch") {
          ui.toast(`SSH host key CHANGED: ${err.message}. Someone may be intercepting the connection. Refusing to connect.`, "error", 0);
          throw e;
        }
        if ((err.code as string) === "passphrase_required" && attempt < 4) {
          const kp = lastKeyPrompt;
          lastKeyPrompt = null;
          if (!kp) throw e;
          const provided = await ui.askPassphrase(kp);
          if (!provided) throw e;
          continue;
        }
        if (err.code !== "host_key_unknown" || attempt >= 4) throw e;
        const p = lastHostKey ?? parseHostKey(err.message);
        lastHostKey = null;
        if (!p) throw e;
        const ok = await ui.confirm({
          title: "Trust this host?",
          message: `The SSH server ${p.host} is not known yet.\nKey type: ${p.keyType || "unknown"}\nFingerprint: ${p.fingerprint}\n\nOnly continue if this matches the fingerprint of your server.`,
          confirmText: "Trust and connect",
        });
        if (!ok) throw e;
        await connectionApi.TrustHostKey(p.host, p.fingerprint);
      }
    }
  }

  /** Loads projects, connections and statuses; subscribes to status events. Call once from App.vue. */
  async function init() {
    if (!unsubHostKey) unsubHostKey = onSshHostKey((p) => (lastHostKey = p));
    if (!unsubPass) unsubPass = onSshPassphrase((p) => (lastKeyPrompt = p));
    if (!unsubscribe) {
      unsubscribe = onConnStatus((s) => {
        statuses.value = { ...statuses.value, [s.connId]: s };
      });
    }
    try {
      const [ps, cs, st, saved, noPw] = await Promise.all([
        projectApi.Projects(),
        connectionApi.Connections(""),
        connectionApi.ConnectionStatuses(),
        projectApi.GetUIState(ACTIVE_PROJECT_KEY),
        projectApi.GetUIState(NO_PASSWORD_KEY).catch(() => ""),
      ]);
      try {
        noPassword = new Set(JSON.parse(noPw || "[]"));
      } catch {
        noPassword = new Set();
      }
      projects.value = ps;
      connections.value = cs;
      statuses.value = Object.fromEntries(st.map((s) => [s.connId, s]));
      activeProjectId.value = ps.find((p) => p.id === saved)?.id ?? ps[0]?.id ?? "";
      loaded.value = true;
    } catch (e) {
      useUiStore().toast(`Failed to load projects: ${apiError(e).message}`, "error");
    }
  }

  async function setActiveProject(id: string) {
    activeProjectId.value = id;
    await projectApi.SetUIState(ACTIVE_PROJECT_KEY, id);
  }

  async function saveProject(p: Partial<Project> & { name: string }): Promise<Project> {
    const saved = await projectApi.SaveProject({ id: "", sortOrder: 0, ...p });
    const i = projects.value.findIndex((x) => x.id === saved.id);
    if (i >= 0) projects.value[i] = saved;
    else projects.value.push(saved);
    return saved;
  }

  async function deleteProject(id: string) {
    await projectApi.DeleteProject(id);
    projects.value = projects.value.filter((p) => p.id !== id);
    connections.value = connections.value.filter((c) => c.projectId !== id);
    if (activeProjectId.value === id) await setActiveProject(projects.value[0]?.id ?? "");
  }

  /** Create (id "") or update. password: undefined = keep stored, "" = clear. */
  async function saveConnection(cfg: ConnectionConfig): Promise<ConnectionConfig> {
    const saved = await connectionApi.SaveConnection(cfg);
    const i = connections.value.findIndex((c) => c.id === saved.id);
    if (i >= 0) connections.value[i] = saved;
    else connections.value.push(saved);
    return saved;
  }

  async function deleteConnection(id: string) {
    await connectionApi.DeleteConnection(id);
    connections.value = connections.value.filter((c) => c.id !== id);
    const { [id]: _, ...rest } = statuses.value;
    statuses.value = rest;
  }

  async function duplicateConnection(id: string): Promise<ConnectionConfig> {
    const copy = await connectionApi.DuplicateConnection(id);
    connections.value.push(copy);
    return copy;
  }

  /** Connects; password used for passwordMode "ask", or when a "save" password is not saved. Throws on failure. */
  async function connect(id: string, password = ""): Promise<ConnectionStatus> {
    const st = await connectionApi.Connect(id, password);
    statuses.value = { ...statuses.value, [id]: st };
    return st;
  }

  /** Reloads one connection (e.g. hasPassword after a password was saved at connect). */
  async function refreshConnection(id: string) {
    try {
      const fresh = await connectionApi.GetConnection(id);
      const i = connections.value.findIndex((c) => c.id === id);
      if (i >= 0) connections.value[i] = fresh;
    } catch {
      /* keep the cached config */
    }
  }

  async function disconnect(id: string) {
    await connectionApi.Disconnect(id);
  }

  /**
   * Makes sure the connection is open: connects (asking for the password when the
   * connection uses passwordMode "ask", unless it connects without one). Resolves false when it failed or the user
   * cancelled; failures are toasted.
   */
  const connecting = new Map<string, Promise<boolean>>();
  let noPassword = new Set<string>();
  function markNoPassword(id: string, on: boolean) {
    if (noPassword.has(id) === on) return;
    if (on) noPassword.add(id);
    else noPassword.delete(id);
    projectApi.SetUIState(NO_PASSWORD_KEY, JSON.stringify([...noPassword])).catch(() => {});
  }
  async function ensureConnected(id: string): Promise<boolean> {
    if (isConnected(id)) return true;
    const existing = connecting.get(id);
    if (existing) return existing;
    const p = doConnect(id).finally(() => connecting.delete(id));
    connecting.set(id, p);
    return p;
  }
  async function doConnect(id: string): Promise<boolean> {
    const ui = useUiStore();
    const cfg = getConnection(id);
    if (!cfg) return false;
    try {
      let pw = "";
      if (cfg.passwordMode === "ask") {
        // a local database, or one that worked without a password before: try
        // without asking; the backend answers password_required if it wants one
        if (cfg.driver !== "sqlite" && (isLocal(cfg) || noPassword.has(id))) {
          try {
            await withHostKeyTrust(() => connect(id, ""));
            markNoPassword(id, true);
            return true;
          } catch (e) {
            if (apiError(e).code !== "password_required") throw e;
            markNoPassword(id, false);
          }
        }
        const entered = await ui.askPassword(id);
        if (entered === null) return false;
        pw = entered;
      }
      try {
        await withHostKeyTrust(() => connect(id, pw));
        if (cfg.passwordMode === "ask") markNoPassword(id, pw === "");
      } catch (e) {
        // "save" mode but no saved password (e.g. it used to be in the Keychain):
        // ask; the backend saves it once the connect succeeds
        if (cfg.passwordMode === "ask" || apiError(e).code !== "password_required") throw e;
        const entered = await ui.askPassword(id);
        if (entered === null) return false;
        await withHostKeyTrust(() => connect(id, entered));
        await refreshConnection(id);
      }
      return true;
    } catch (e) {
      const err = apiError(e);
      if (err.code !== "host_key_mismatch" && err.code !== "host_key_unknown") {
        const nice = friendlyError(err.message);
        ui.toast(`${cfg.name}: ${nice ?? err.message}`, "error", undefined, {
          detail: nice ? err.message : undefined,
          actions: [
            { label: "Retry", run: () => void ensureConnected(id) },
            { label: "Edit connection", run: () => ui.openModal("connection", { connId: id }) },
          ],
        });
      }
      return false;
    }
  }

  async function renameProject(id: string, name: string) {
    const p = projects.value.find((x) => x.id === id);
    if (p) await saveProject({ ...p, name });
  }

  /** Persists a new project order (ids in display order). */
  async function reorderProjects(ids: string[]) {
    const byId = new Map(projects.value.map((p) => [p.id, p]));
    projects.value = ids.map((id) => byId.get(id)!).filter(Boolean).map((p, i) => ({ ...p, sortOrder: i }));
    await projectApi.ReorderProjects(ids);
  }

  /** Persists a new connection order within a project. */
  async function reorderConnections(projectId: string, ids: string[]) {
    const before = connections.value.map((c) => [c, c.sortOrder, c.projectId] as const);
    ids.forEach((id, i) => {
      const c = getConnection(id);
      if (c) {
        c.sortOrder = i;
        c.projectId = projectId;
      }
    });
    try {
      await connectionApi.ReorderConnections(projectId, ids);
    } catch (e) {
      for (const [c, so, pid] of before) {
        c.sortOrder = so;
        c.projectId = pid;
      }
      throw e;
    }
  }

  function testConnection(cfg: ConnectionConfig): Promise<TestResult> {
    return withHostKeyTrust(() => connectionApi.TestConnection(cfg));
  }

  /** Empty config for the connection form. */
  function blankConnection(driver: ConnectionConfig["driver"] = "mysql"): ConnectionConfig {
    return {
      id: "", projectId: activeProjectId.value, name: "", driver,
      host: "", port: 0, socket: "", user: "", passwordMode: "save", hasPassword: false,
      database: "", filePath: "", defaultSchema: "", sslMode: "", params: {},
      ssh: { enabled: false, host: "", port: 0, user: "", authMethod: "auto", keyPath: "", hasPassword: false, hasKeyPassphrase: false },
      color: "none", readOnly: false, sortOrder: 0, createdAt: "", updatedAt: "",
    };
  }

  return {
    projects, connections, statuses, activeProjectId, loaded,
    activeProject, projectConnections,
    getConnection, isReadOnly, isConnected, connColor,
    init, setActiveProject, saveProject, deleteProject,
    saveConnection, deleteConnection, duplicateConnection,
    connect, disconnect, testConnection, blankConnection,
    ensureConnected, withHostKeyTrust, renameProject, reorderProjects, reorderConnections, refreshConnection,
  };
});
