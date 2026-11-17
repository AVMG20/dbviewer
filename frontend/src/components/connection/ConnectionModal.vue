<script setup lang="ts">
// Connection modal (shell agent): create / edit, test, duplicate, delete, .env import.
// Opened with ui.openModal("connection", { connId } | { projectId }).
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { apiError, connectionApi, type ConnectionConfig, type EnvImportResult, type SSHAuthAttempt } from "@/api";
import { useProjectsStore } from "@/stores/projects";
import { useTabsStore } from "@/stores/tabs";
import { useUiStore } from "@/stores/ui";
import { CONN_COLORS, DRIVER_BADGE, DRIVER_LABEL } from "@/types";
import Icon from "@/components/common/Icon.vue";
import Modal from "@/components/common/Modal.vue";
import { invalidate } from "@/components/sidebar/schemaCache";
import { friendlyError } from "@/components/common/errors";

const projects = useProjectsStore();
const tabs = useTabsStore();
const ui = useUiStore();

const props = defineProps<{ connId?: string; projectId?: string }>();
// Captured once: a confirm dialog stacked on top must not change what this form edits.
const connId = computed(() => props.connId ?? "");
const isEdit = !!props.connId;
const form = ref<ConnectionConfig>(projects.blankConnection("mysql"));
const ready = ref(false);

// write-only secrets
const pw = ref("");
const pwTouched = ref(false);
const sshPw = ref("");
const sshPwTouched = ref(false);
const sshPass = ref("");
const sshPassTouched = ref(false);
const hadPassword = ref(false);

const netMode = ref<"tcp" | "socket">("tcp");
const busy = ref(false);
const error = ref("");
const test = ref<{ state: "idle" | "running" | "ok" | "fail"; text: string; raw?: string }>({ state: "idle", text: "" });
const showRaw = ref(false);
const showPw = ref(false);
const advOpen = ref(false);

// ---- Test SSH (separate from the full test; shown inside the SSH section) -----------------
const sshTest = ref<{ state: "idle" | "running" | "ok" | "warn" | "fail"; text: string; tried?: SSHAuthAttempt[]; raw?: string }>({ state: "idle", text: "" });
const showSshRaw = ref(false);

function sshFailure(err: { code: string; message: string }) {
  const raw = err.message;
  let text = raw; // the backend message already says what was tried (and the ssh-add hint)
  if ((err.code as string) === "host_key_mismatch") text = `The SSH host key changed: refusing to connect.\n${raw}`;
  else if ((err.code as string) === "passphrase_required") text = "The SSH key needs its passphrase (cancelled).";
  else if (err.code === "not_implemented") text = `Not supported yet: ${raw}`;
  sshTest.value = { state: "fail", text };
  showSshRaw.value = false;
}
const isSshError = (code: string, msg: string): boolean =>
  ["ssh_error", "host_key_unknown", "host_key_mismatch", "passphrase_required"].includes(code) || /\bssh\b/i.test(msg);

async function runSshTest() {
  if (sshTest.value.state === "running") return;
  const c = buildConfig(true);
  if (!c.name) c.name = "test";
  c.ssh.enabled = true;
  if (!c.ssh.host.trim()) {
    sshTest.value = { state: "fail", text: "Enter the SSH host (or a ~/.ssh/config alias) first" };
    return;
  }
  sshTest.value = { state: "running", text: "Testing SSH…" };
  try {
    // host-key trust and key passphrase prompts are handled by withHostKeyTrust, then the call is retried
    const r = await projects.withHostKeyTrust(() => connectionApi.TestSSH(c));
    const via = [r.authMethod, r.proxyJump ? `jump host ${r.proxyJump}` : ""].filter(Boolean).join(" · ");
    const who = `Authenticated as ${r.user || form.value.ssh.user || "user"} via ${via}`;
    const ver = r.serverVersion ? ` · ${r.serverVersion}` : "";
    const detail = r.hostKeyFingerprint ? `Host key ${r.hostKeyFingerprint}${r.address ? ` (${r.address})` : ""}` : undefined;
    if (r.dbReachable) {
      sshTest.value = { state: "ok", text: `${who}${ver} · DB port reachable`, tried: r.tried, raw: detail };
    } else {
      sshTest.value = {
        state: "warn",
        text: `SSH OK · DB port not reachable from the SSH host${r.dbEndpoint ? ` (${r.dbEndpoint})` : ""}${r.dbError ? `: ${r.dbError}` : ""}\n${who}${ver}`,
        tried: r.tried,
        raw: detail,
      };
    }
  } catch (e) {
    sshFailure(apiError(e));
  }
}
watch(
  () => sshTest.value.state,
  () => nextTick(() => document.querySelector('[data-sshresult]')?.scrollIntoView({ block: 'nearest' })),
);
async function copySsh() {
  const t = sshTest.value;
  const lines = [t.text, ...(t.tried ?? []).map((x) => `${x.method}${x.key ? ` ${x.key}` : ""}: ${x.result}`), t.raw ?? ""].filter(Boolean);
  try {
    await navigator.clipboard.writeText(lines.join("\n"));
    ui.toast("Copied", "success", 1200);
  } catch {
    ui.toast("Select the text and copy it manually", "info");
  }
}

async function copyResult() {
  try {
    await navigator.clipboard.writeText(test.value.raw || test.value.text);
    ui.toast("Copied", "success", 1200);
  } catch {
    ui.toast("Select the text and copy it manually", "info");
  }
}

// .env import panel
const envText = ref("");
const envNote = ref<{ ok: boolean; text: string[] } | null>(null);
const envBusy = ref(false);

const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v));

onMounted(async () => {
  let cfg: ConnectionConfig | undefined;
  if (isEdit) {
    try {
      cfg = await connectionApi.GetConnection(connId.value);
    } catch {
      cfg = projects.getConnection(connId.value);
    }
    if (!cfg) {
      ui.toast("Connection not found", "error");
      ui.closeModal();
      return;
    }
  } else {
    cfg = projects.blankConnection("mysql");
    const pid = props.projectId ?? "";
    if (pid) cfg.projectId = pid;
  }
  form.value = clone(cfg);
  if ((form.value.ssh.authMethod as string) === "agent" || !form.value.ssh.authMethod) form.value.ssh.authMethod = "auto";
  hadPassword.value = cfg.hasPassword;
  netMode.value = cfg.socket ? "socket" : "tcp";
  advOpen.value = !!cfg.sslMode;
  ready.value = true;
});

watch(
  () => [form.value, pw.value, sshPw.value, sshPass.value, netMode.value],
  () => {
    if (test.value.state !== "running") test.value = { state: "idle", text: "" };
  },
  { deep: true },
);

const driver = computed(() => form.value.driver);
const isSqlite = computed(() => driver.value === "sqlite");
const portStr = computed({
  get: () => (form.value.port ? String(form.value.port) : ""),
  set: (v: string) => (form.value.port = Math.max(0, parseInt(v, 10) || 0)),
});
const sshPortStr = computed({
  get: () => (form.value.ssh.port ? String(form.value.ssh.port) : ""),
  set: (v: string) => (form.value.ssh.port = Math.max(0, parseInt(v, 10) || 0)),
});
const defaultPort = computed(() => (driver.value === "postgres" ? 5432 : 3306));
const socketPlaceholder = computed(() =>
  driver.value === "postgres" ? "/var/run/postgresql  (socket directory)" : "/opt/workbench/data/run/mysqld/mysql84.sock",
);
const savePassword = computed({
  get: () => form.value.passwordMode === "save",
  set: (v: boolean) => (form.value.passwordMode = v ? "save" : "ask"),
});
const colorHex: Record<string, string> = {
  none: "transparent", green: "var(--conn-green)", yellow: "var(--conn-yellow)", orange: "var(--conn-orange)",
  red: "var(--conn-red)", blue: "var(--conn-blue)", purple: "var(--conn-purple)",
};
const colorName: Record<string, string> = {
  none: "No color", green: "Green", yellow: "Yellow", orange: "Orange", red: "Red", blue: "Blue", purple: "Purple",
};

// ---- environment presets: set color + read-only together (the saved model stays color + readOnly) ----
type EnvId = "none" | "local" | "staging" | "production";
const ENV_PRESETS: { id: EnvId; label: string; color: ConnectionConfig["color"]; readOnly: boolean; hint: string }[] = [
  { id: "none", label: "None", color: "none", readOnly: false, hint: "No color, writes allowed" },
  { id: "local", label: "Local", color: "green", readOnly: false, hint: "Green, writes allowed" },
  { id: "staging", label: "Staging", color: "orange", readOnly: false, hint: "Orange, writes allowed" },
  { id: "production", label: "Production", color: "red", readOnly: true, hint: "Red, read-only" },
];
/** The preset matching the current color + read-only, or "custom" when they were set by hand. */
const env = computed<EnvId | "custom">(
  () => ENV_PRESETS.find((p) => p.color === form.value.color && p.readOnly === !!form.value.readOnly)?.id ?? "custom",
);
function applyPreset(id: EnvId) {
  const p = ENV_PRESETS.find((x) => x.id === id)!;
  form.value.color = p.color;
  form.value.readOnly = p.readOnly;
}
const suggestReadOnly = computed(() => form.value.color === "red" && !form.value.readOnly);

function setDriver(d: ConnectionConfig["driver"]) {
  form.value.driver = d;
  if (d === "sqlite") {
    netMode.value = "tcp";
  }
  if (d === "postgres" && !form.value.database && !isEdit) form.value.database = "";
}

/** The config to send: only fields relevant for the driver / mode; secrets only when touched. */
function buildConfig(forTest = false): ConnectionConfig {
  const c = clone(form.value);
  c.name = c.name.trim();
  if (c.driver === "sqlite") {
    c.host = ""; c.port = 0; c.socket = ""; c.user = ""; c.database = ""; c.sslMode = "";
    c.ssh.enabled = false;
    c.password = undefined;
  } else {
    c.filePath = "";
    if (netMode.value === "socket") {
      c.host = ""; c.port = 0; c.sslMode = "";
    } else c.socket = "";
    c.host = c.host.trim();
    c.socket = c.socket.trim();
    c.password = pwTouched.value ? pw.value : undefined;
    if (!forTest && c.passwordMode === "ask" && (hadPassword.value || pwTouched.value)) c.password = "";
    if (forTest && c.passwordMode === "ask" && pwTouched.value) c.password = pw.value;
  }
  c.ssh.password = sshPwTouched.value ? sshPw.value : undefined;
  c.ssh.keyPassphrase = sshPassTouched.value ? sshPass.value : undefined;
  if (!c.ssh.enabled) {
    c.ssh.password = undefined;
    c.ssh.keyPassphrase = undefined;
  }
  return c;
}

function validate(): string {
  const c = form.value;
  if (!c.name.trim()) return "Name is required";
  if (c.driver === "sqlite") return c.filePath.trim() ? "" : "SQLite file path is required";
  if (netMode.value === "socket" && !c.socket.trim()) return "Socket path is required";
  if (c.ssh.enabled && !c.ssh.host.trim()) return "SSH host is required";
  return "";
}

async function runTest() {
  if (test.value.state === "running") return;
  const c = buildConfig(true);
  if (isSqlite.value ? !c.filePath : false) {
    test.value = { state: "fail", text: "Enter the SQLite file path first" };
    return;
  }
  if (!c.name) c.name = "test";
  test.value = { state: "running", text: "Testing…" };
  try {
    const r = await projects.testConnection(c);
    test.value = {
      state: "ok",
      text: [r.message || "Connected", r.message.includes(r.serverVersion) ? "" : r.serverVersion, r.latencyMs ? `${r.latencyMs} ms` : ""]
        .filter(Boolean)
        .join(" · "),
    };
  } catch (e) {
    const err = apiError(e);
    const raw = err.message;
    if (form.value.ssh.enabled && isSshError(err.code, raw)) sshFailure(err); // show it where the layer is
    test.value = {
      state: "fail",
      text: err.code === "not_implemented" ? `Not supported yet: ${raw}` : (friendlyError(raw) ?? raw),
      raw: err.code === "not_implemented" || !friendlyError(raw) ? undefined : raw,
    };
    showRaw.value = false;
  }
}

async function save() {
  if (busy.value) return;
  error.value = validate();
  if (error.value) return;
  busy.value = true;
  try {
    const saved = await projects.saveConnection(buildConfig());
    invalidate(saved.id);
    ui.toast(isEdit ? `Saved "${saved.name}"` : `Created "${saved.name}"`, "success");
    ui.closeModal();
  } catch (e) {
    error.value = apiError(e).message;
    busy.value = false;
  }
}

async function duplicate() {
  busy.value = true;
  try {
    const d = await projects.duplicateConnection(connId.value);
    ui.toast(`Created "${d.name}"`, "success");
    ui.closeModal();
    ui.openModal("connection", { connId: d.id });
  } catch (e) {
    error.value = apiError(e).message;
    busy.value = false;
  }
}

async function remove() {
  const name = form.value.name;
  const ok = await ui.confirm({
    title: "Delete connection?",
    message: `"${name}" and its saved password, query history and open tabs will be deleted. This cannot be undone.`,
    confirmText: "Delete",
    danger: true,
  });
  if (!ok) return;
  try {
    tabs.closeConnection(connId.value);
    await projects.deleteConnection(connId.value);
    invalidate(connId.value);
    ui.closeModal();
  } catch (e) {
    ui.toast(apiError(e).message, "error");
  }
}

async function browse() {
  try {
    const p = await connectionApi.OpenSQLiteFileDialog();
    if (p) {
      form.value.filePath = p;
      if (!form.value.name.trim()) form.value.name = p.split("/").pop() ?? "";
    }
  } catch {
    ui.toast("The native file picker is not available here; type the path instead.", "info");
  }
}

// ---- paste a connection string / URL / .env -------------------------------------------
interface Snapshot {
  form: ConnectionConfig;
  pw: string;
  pwTouched: boolean;
  netMode: "tcp" | "socket";
}
const snapshot = ref<Snapshot | null>(null);
const envFocus = ref(false);

/** Heuristic: does this pasted text look like a connection string / .env rather than a plain value? */
function looksLikeConnString(text: string): boolean {
  const t = text.trim();
  if (!t || t.length > 4000) return false;
  if (t.includes("://") || /^jdbc:/i.test(t) || /@(tcp|unix)\(/i.test(t)) return true;
  if (/DB_CONNECTION=|DATABASE_URL=/.test(t)) return true;
  const pairs = t.match(/(^|[\s;])[A-Za-z_][\w.-]*=\S*/g) ?? [];
  return pairs.length >= 2 && /(^|[\s;])(host|dbname)=/i.test(t);
}

async function parseAny(text: string): Promise<EnvImportResult> {
  const api = connectionApi as unknown as { ParseConnectionString?: (t: string) => Promise<EnvImportResult> };
  if (api.ParseConnectionString) return api.ParseConnectionString(text);
  const t = text.trim(); // older backend: .env text as is, anything else as DATABASE_URL
  const isEnv = /DB_CONNECTION=|DATABASE_URL=/.test(t) || t.includes("\n");
  return connectionApi.ParseEnv(isEnv ? t : `DATABASE_URL=${t}`);
}

function describeSource(text: string, r: EnvImportResult): string {
  const fmt = r.found?.format;
  const scheme = /^([a-z][a-z0-9+.-]*):\/\//i.exec(text.trim())?.[1];
  switch (fmt) {
    case "url": return scheme ? `${scheme}:// URL` : "URL";
    case "jdbc": return "JDBC URL";
    case "mysql-dsn": return "MySQL DSN";
    case "libpq": return "libpq connection string";
    case "env": return ".env";
  }
  return scheme ? `${scheme}:// URL` : "connection string";
}

function applyEnv(r: EnvImportResult, source = ".env") {
  snapshot.value = { form: clone(form.value), pw: pw.value, pwTouched: pwTouched.value, netMode: netMode.value };
  const c = r.config;
  const f = form.value;
  f.driver = c.driver || f.driver;
  f.host = c.host;
  f.port = c.port;
  f.socket = c.socket;
  f.user = c.user;
  f.database = c.database;
  f.filePath = c.filePath;
  if (c.sslMode) f.sslMode = c.sslMode;
  if (c.color && c.color !== "none") f.color = c.color;
  if (c.readOnly) f.readOnly = true;
  if (!f.name.trim() && c.name) f.name = c.name;
  if (!f.name.trim()) f.name = c.database || c.filePath.split("/").pop() || c.host || "";
  netMode.value = c.socket ? "socket" : "tcp";
  if (c.password) {
    pw.value = c.password;
    pwTouched.value = true;
  }
  envNote.value = { ok: true, text: [`Filled from ${source}.`, ...(r.warnings ?? [])] };
}
function undoEnv() {
  const sn = snapshot.value;
  if (!sn) return;
  form.value = sn.form;
  pw.value = sn.pw;
  pwTouched.value = sn.pwTouched;
  netMode.value = sn.netMode;
  snapshot.value = null;
  envNote.value = null;
}
async function fillFrom(text: string) {
  if (!text.trim()) return;
  envBusy.value = true;
  try {
    const r = await parseAny(text);
    applyEnv(r, describeSource(text, r));
    envText.value = "";
  } catch (e) {
    envNote.value = { ok: false, text: [apiError(e).message] };
  } finally {
    envBusy.value = false;
  }
}
/** Paste into the dedicated box: fill right away. */
function onEnvPaste(e: ClipboardEvent) {
  const t = e.clipboardData?.getData("text") ?? "";
  if (!t.trim()) return;
  e.preventDefault();
  void fillFrom(t);
}
/** Paste into any other field: intercept when it looks like a connection string. */
function onPasteAny(e: ClipboardEvent) {
  const el = e.target as HTMLElement;
  if (el.closest("[data-envbox]") || (el instanceof HTMLInputElement && el.type === "password")) return;
  const t = e.clipboardData?.getData("text") ?? "";
  if (!looksLikeConnString(t)) return;
  e.preventDefault();
  void fillFrom(t);
}
async function pickEnv() {
  envBusy.value = true;
  try {
    const r = await connectionApi.ImportEnvFile("");
    if (!r.cancelled) applyEnv(r, ".env file");
  } catch {
    envNote.value = { ok: false, text: ["The native file picker is not available here. Paste the .env contents into the box."] };
  } finally {
    envBusy.value = false;
  }
}

function onKey(e: KeyboardEvent) {
  if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
    e.preventDefault();
    void save();
  }
}
</script>

<template>
  <Modal :title="isEdit ? 'Edit connection' : 'New connection'" :width="560" :close-on-overlay="false" @close="ui.closeModal()">
    <div v-if="!ready" class="dim py-6 text-center">Loading…</div>
    <form v-else class="cm" @submit.prevent="save" @keydown="onKey" @paste.capture="onPasteAny">
      <!-- 1. what kind of connection: decides which fields appear -->
      <div class="row2 top">
        <div class="f">
          <label>Driver</label>
          <div class="seg">
            <button v-for="d in (['postgres', 'mysql', 'sqlite'] as const)" :key="d" type="button" :class="{ on: driver === d }" @click="setDriver(d)">
              <span class="dbadge">{{ DRIVER_BADGE[d] }}</span>{{ DRIVER_LABEL[d] }}
            </button>
          </div>
        </div>
        <div v-if="!isSqlite" class="f">
          <label>Connect via</label>
          <div class="seg">
            <button type="button" :class="{ on: netMode === 'tcp' }" @click="netMode = 'tcp'">TCP / IP</button>
            <button type="button" :class="{ on: netMode === 'socket' }" @click="netMode = 'socket'">Unix socket</button>
          </div>
        </div>
        <div v-else class="f">
          <label>Location</label>
          <div class="seg"><button type="button" class="on" disabled>Local file</button></div>
        </div>
      </div>

      <!-- 2. paste a connection URL / .env -->
      <div data-envbox class="pastewrap">
        <div class="paste" :class="{ focus: envFocus }">
          <Icon name="link" class="dim" />
          <input
            v-model="envText"
            class="paste-in selectable mono"
            placeholder="Paste connection URL or .env"
            spellcheck="false"
            autocomplete="off"
            @focus="envFocus = true"
            @blur="envFocus = false"
            @paste="onEnvPaste"
            @keydown.enter.prevent="fillFrom(envText)"
          />
          <button v-if="envText.trim()" type="button" class="mini" :disabled="envBusy" title="Fill the form from this text" @click="fillFrom(envText)">Fill</button>
          <button type="button" class="mini icon" title="Choose a .env file" aria-label="Choose a .env file" @click="pickEnv"><Icon name="file" /></button>
        </div>
        <div v-if="envFocus && !envNote" class="hint">e.g. mysql://user:pass@host:3306/db · postgres://… · root@unix(/path/mysql.sock)/db · DATABASE_URL=… · DB_HOST=…</div>
        <div v-if="envNote" class="hint note">
          <span :class="envNote.ok ? 'text-ok' : 'text-danger'">{{ envNote.text[0] }}</span>
          <a v-if="envNote.ok && snapshot" class="link" @click="undoEnv">Undo</a>
          <span v-for="(t, i) in envNote.text.slice(1)" :key="i" class="w-full text-warn">{{ t }}</span>
        </div>
      </div>

      <!-- 3. connection card -->
      <div class="card pad">
        <div class="f"><label>Name</label><input v-model="form.name" class="input" placeholder="my-database" data-autofocus /></div>

        <template v-if="!isSqlite">
          <div v-if="netMode === 'tcp'" class="hostrow">
            <div class="f"><label>Host</label><input v-model="form.host" class="input" placeholder="127.0.0.1" /></div>
            <div class="f"><label>Port</label><input v-model="portStr" class="input" inputmode="numeric" :placeholder="String(defaultPort)" /></div>
          </div>
          <div v-else class="f">
            <label>Socket path</label>
            <input v-model="form.socket" class="input mono" :placeholder="socketPlaceholder" spellcheck="false" />
          </div>

          <div class="row2">
            <div class="f"><label>User</label><input v-model="form.user" class="input" autocomplete="off" /></div>
            <div class="f">
              <label>Password</label>
              <div class="pwbox">
                <input
                  v-model="pw"
                  :type="showPw ? 'text' : 'password'"
                  class="input"
                  autocomplete="new-password"
                  :placeholder="hadPassword && !pwTouched ? '•••••••• (saved)' : 'empty'"
                  @input="pwTouched = true"
                />
                <button type="button" class="eye" :title="showPw ? 'Hide password' : 'Show password'" :aria-label="showPw ? 'Hide password' : 'Show password'" @click="showPw = !showPw">
                  <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M1.5 8s2.3-4.5 6.5-4.5S14.5 8 14.5 8 12.2 12.5 8 12.5 1.5 8 1.5 8z" /><circle cx="8" cy="8" r="2" />
                    <path v-if="showPw" d="M2.5 13.5l11-11" />
                  </svg>
                </button>
              </div>
              <div class="save-pw">
                <label class="check" title="Store the password locally in the app's data folder (readable only by you). Off = ask on every connect.">
                  <input v-model="savePassword" type="checkbox" /> Save password
                </label>
                <a v-if="hadPassword && !pwTouched" class="link" title="Remove the saved password" @click="pw = ''; pwTouched = true">Clear saved</a>
                <span v-if="savePassword" class="dim text-[11px]">Stored locally in the app's data folder</span>
              </div>
            </div>
          </div>

          <div class="f"><label>Database</label><input v-model="form.database" class="input" :placeholder="driver === 'postgres' ? 'postgres' : ''" /></div>
        </template>

        <div v-else class="f">
          <label>Database file</label>
          <div class="fileRow">
            <input v-model="form.filePath" class="input mono" placeholder="/path/to/database.sqlite" spellcheck="false" />
            <button type="button" class="btn" @click="browse">Browse…</button>
          </div>
        </div>
      </div>

      <!-- 4. SSH tunnel -->
      <div v-if="!isSqlite" class="card" :class="{ open: form.ssh.enabled }">
        <div class="cardhead">
          <span class="tg" :class="{ on: form.ssh.enabled }" role="switch" :aria-checked="form.ssh.enabled" aria-label="SSH tunnel" tabindex="0" @click="form.ssh.enabled = !form.ssh.enabled" @keydown.space.prevent="form.ssh.enabled = !form.ssh.enabled" />
          <div class="min-w-0 flex-1">
            <div class="ttl">SSH tunnel</div>
            <div v-if="!form.ssh.enabled" class="sub">Reach the database through a bastion host</div>
          </div>
          <button v-if="form.ssh.enabled" type="button" class="btn" :disabled="sshTest.state === 'running'" @click="runSshTest">
            <span v-if="sshTest.state === 'running'" class="spinner" /> Test SSH
          </button>
        </div>
        <div v-if="form.ssh.enabled" class="cardbody">
          <div class="hostrow">
            <div class="f"><label>SSH host</label><input v-model="form.ssh.host" class="input" placeholder="host or ~/.ssh/config alias" /></div>
            <div class="f"><label>Port</label><input v-model="sshPortStr" class="input" inputmode="numeric" placeholder="22" /></div>
          </div>
          <div class="row2">
            <div class="f"><label>SSH user</label><input v-model="form.ssh.user" class="input" placeholder="from ~/.ssh/config" /></div>
            <div class="f">
              <label>Authentication</label>
              <select v-model="form.ssh.authMethod" class="input">
                <option value="auto">Default (agent + ~/.ssh)</option>
                <option value="key">Key file</option>
                <option value="password">Password</option>
              </select>
            </div>
          </div>
          <div v-if="form.ssh.authMethod === 'auto'" class="sub hintline">Uses ssh-agent, then your default ~/.ssh keys and ~/.ssh/config. User and port may stay empty.</div>
          <div v-if="form.ssh.authMethod === 'key'" class="row2">
            <div class="f"><label>Key file</label><input v-model="form.ssh.keyPath" class="input mono" placeholder="~/.ssh/id_ed25519" spellcheck="false" /></div>
            <div class="f">
              <label>Passphrase</label>
              <input v-model="sshPass" type="password" class="input" autocomplete="new-password" :placeholder="form.ssh.hasKeyPassphrase && !sshPassTouched ? '•••••••• (saved)' : 'none'" @input="sshPassTouched = true" />
            </div>
          </div>
          <div v-if="form.ssh.authMethod === 'password'" class="f">
            <label>SSH password</label>
            <input v-model="sshPw" type="password" class="input" autocomplete="new-password" :placeholder="form.ssh.hasPassword && !sshPwTouched ? '•••••••• (saved)' : ''" @input="sshPwTouched = true" />
          </div>

          <div v-if="sshTest.state === 'ok' || sshTest.state === 'warn' || (sshTest.state === 'fail' && sshTest.text)" data-sshresult class="result" :class="sshTest.state">
            <div class="flex items-start gap-2">
              <div class="selectable min-w-0 flex-1 whitespace-pre-wrap break-words">{{ sshTest.state === "ok" ? "✓ " : "" }}{{ sshTest.text }}</div>
              <button type="button" class="mini" @click="copySsh">Copy</button>
            </div>
            <a v-if="sshTest.tried?.length || sshTest.raw" class="link mt-1 inline-block" @click="showSshRaw = !showSshRaw">{{ showSshRaw ? "Hide details" : "Details" }}</a>
            <div v-if="showSshRaw" class="selectable mt-1 font-mono text-[11px] text-tx">
              <div v-for="(t, i) in sshTest.tried ?? []" :key="i" class="flex gap-2">
                <span class="dim w-16 flex-none">{{ t.method }}</span><span class="min-w-0 flex-1 break-all">{{ t.key }}</span><span class="flex-none">{{ t.result }}</span>
              </div>
              <pre v-if="sshTest.raw" class="m-0 mt-1 whitespace-pre-wrap break-all">{{ sshTest.raw }}</pre>
            </div>
          </div>
        </div>
      </div>

      <!-- 5. environment: color + read-only -->
      <div class="card env">
        <div class="f">
          <label id="env-lbl">Environment</label>
          <div class="seg" role="radiogroup" aria-labelledby="env-lbl">
            <button
              v-for="p in ENV_PRESETS"
              :key="p.id"
              type="button"
              role="radio"
              :aria-checked="env === p.id"
              :class="{ on: env === p.id }"
              :title="p.hint"
              @click="applyPreset(p.id)"
            >
              <span class="edot" :class="{ none: p.color === 'none' }" :style="{ background: p.color === 'none' ? 'transparent' : colorHex[p.color] }" />{{ p.label }}
            </button>
            <span v-if="env === 'custom'" class="custom" title="Color and read-only were set by hand">Custom</span>
          </div>
        </div>
        <div class="safety">
          <div class="swatches" role="radiogroup" aria-label="Color">
            <span class="lbl">Color</span>
            <button
              v-for="c in CONN_COLORS"
              :key="c"
              type="button"
              class="sw"
              role="radio"
              :aria-checked="form.color === c"
              :class="{ on: form.color === c, none: c === 'none' }"
              :title="colorName[c]"
              :aria-label="colorName[c]"
              :style="{ '--c': c === 'none' ? 'var(--dim)' : colorHex[c], background: c === 'none' ? 'transparent' : colorHex[c] }"
              @click="form.color = c"
            />
          </div>
          <div class="roRow">
            <span class="lbl">Read-only</span>
            <span class="tg" :class="{ on: form.readOnly }" role="switch" :aria-checked="form.readOnly" aria-label="Read-only" tabindex="0" title="Blocks all writes at the session level" @click="form.readOnly = !form.readOnly" @keydown.space.prevent="form.readOnly = !form.readOnly" />
          </div>
        </div>
        <div v-if="env === 'production'" class="envnote prod">
          <Icon name="lock" :size="12" />
          <span>
            <b>Read-only is on</b>: edits, deletes and schema changes are blocked for this connection.
            If you turn it off, saving changes on a red connection asks for an extra confirmation with the SQL shown.
          </span>
        </div>
        <div v-else-if="suggestReadOnly" class="envnote warn">
          <Icon name="warn" :size="12" />
          <span>Red means production. Writes are allowed, but each save asks for an extra confirmation.</span>
          <button type="button" class="mini" @click="form.readOnly = true">Enable read-only</button>
        </div>
        <div v-else-if="form.readOnly" class="envnote">
          <Icon name="lock" :size="12" />
          <span>Read-only: edits, deletes and schema changes are blocked for this connection.</span>
        </div>
      </div>

      <!-- 6. advanced (collapsed) -->
      <div v-if="!isSqlite && netMode === 'tcp'" class="adv">
        <button type="button" class="advhead" :aria-expanded="advOpen" @click="advOpen = !advOpen">
          <Icon name="chev" :size="10" :class="{ 'rotate-90': advOpen }" /> Advanced
        </button>
        <div v-if="advOpen" class="advbody">
          <div class="f" style="max-width: 50%">
            <label>SSL mode</label>
            <select v-model="form.sslMode" class="input">
              <option value="">Default</option>
              <option value="disable">Disable</option>
              <option value="prefer">Prefer</option>
              <option value="require">Require</option>
              <option value="verify-ca">Verify CA</option>
              <option value="verify-full">Verify full</option>
            </select>
          </div>
        </div>
      </div>

      <div v-if="test.state === 'ok' || test.state === 'fail'" class="result" :class="test.state === 'ok' ? 'ok' : 'fail'">
        <div class="flex items-start gap-2">
          <div class="selectable min-w-0 flex-1 whitespace-pre-wrap break-words">{{ test.state === "ok" ? "✓ " : "" }}{{ test.text }}</div>
          <button type="button" class="mini" @click="copyResult">Copy</button>
        </div>
        <template v-if="test.raw">
          <a class="link mt-1 inline-block" @click="showRaw = !showRaw">{{ showRaw ? "Hide details" : "Details" }}</a>
          <pre v-if="showRaw" class="selectable m-0 mt-1 whitespace-pre-wrap break-all font-mono text-[11px] text-tx">{{ test.raw }}</pre>
        </template>
      </div>
      <div v-if="error" class="result fail">{{ error }}</div>
      <button type="submit" class="hidden" tabindex="-1" aria-hidden="true" />
    </form>

    <template #footer>
      <div class="ftr" style="--ctl: 32px">
        <div class="flex min-w-0 items-center gap-2">
          <button class="btn" :disabled="!ready || test.state === 'running'" @click="runTest">Test connection</button>
          <span v-if="test.state === 'running'" class="spinner" />
          <span v-else-if="test.state === 'ok'" class="text-ok">✓ Connected</span>
          <span v-else-if="test.state === 'fail'" class="text-danger">✗ Failed</span>
        </div>
        <span class="flex-1" />
        <template v-if="isEdit">
          <button class="btn ghost" :disabled="busy" @click="duplicate">Duplicate</button>
          <button class="btn ghost !text-danger" :disabled="busy" @click="remove">Delete</button>
        </template>
        <button class="btn" @click="ui.closeModal()">Cancel</button>
        <button class="btn primary" :disabled="!ready || busy" @click="save">Save</button>
      </div>
    </template>
  </Modal>
</template>

<style scoped>
.cm { --ctl: 32px; display: flex; flex-direction: column; gap: 12px; }
.f { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.f > label, .lbl { font-size: 11px; color: var(--dim); font-weight: 500; }
.row2 { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.row2.top { grid-template-columns: 1.35fr 1fr; }
.hostrow { display: grid; grid-template-columns: 1fr 90px; gap: 12px; }
.fileRow { display: grid; grid-template-columns: 1fr auto; gap: 8px; }
select.input { padding-right: 6px; appearance: auto; }
.sub { font-size: 11px; color: var(--dim); }
.hintline { margin-top: -4px; }

/* cards: one bordered group on the panel surface */
.card { border: 1px solid var(--bd); border-radius: var(--r); background: var(--bg2); }
.card.pad { display: flex; flex-direction: column; gap: 12px; padding: 14px; }
.cardhead { display: flex; align-items: center; gap: 12px; padding: 10px 12px; }
.ttl { font-weight: 500; line-height: 1.2; }
.cardbody { display: flex; flex-direction: column; gap: 12px; padding: 12px 14px 14px; border-top: 1px solid var(--bd); }
.passrow { gap: 6px; }

/* neutral segmented control: lifted surface for the selected segment */
.seg { display: flex; height: var(--ctl); padding: 2px; gap: 2px; background: var(--bg); border: 1px solid var(--bd); border-radius: var(--r); }
.seg button { flex: 1; display: inline-flex; align-items: center; justify-content: center; gap: 6px; border: 0; background: none; color: var(--dim); border-radius: calc(var(--r) - 3px); padding: 0 8px; white-space: nowrap; }
.seg button:hover:not(.on):not(:disabled) { color: var(--tx); background: var(--hov); }
.seg button.on { background: var(--bg3); color: var(--tx); font-weight: 500; box-shadow: 0 0 0 1px var(--bd2); }
.seg button:focus-visible { outline: none; box-shadow: 0 0 0 3px var(--ring); }
.dbadge { font: 600 9px var(--mono); padding: 1px 5px; border-radius: 99px; background: var(--bg2); border: 1px solid var(--bd); color: var(--dim); letter-spacing: .03em; }
.seg button.on .dbadge { color: var(--tx); border-color: var(--bd2); }

/* paste box */
.pastewrap { display: flex; flex-direction: column; gap: 6px; }
.paste { display: flex; align-items: center; gap: 6px; height: var(--ctl); padding: 0 4px 0 10px; background: var(--bg2); border: 1px dashed var(--bd2); border-radius: var(--r); }
.paste.focus { border-style: solid; border-color: var(--ac); box-shadow: 0 0 0 3px var(--ring); }
.paste-in { flex: 1; min-width: 0; height: 100%; background: none; border: 0; outline: none; font-size: 12px; color: var(--tx); }
.paste-in::placeholder { color: var(--dim); font-family: var(--sans); }
.mini { height: 24px; padding: 0 9px; border: 1px solid var(--bd); border-radius: 7px; background: var(--bg3); color: var(--tx); font-size: 11.5px; display: inline-flex; align-items: center; justify-content: center; gap: 4px; flex: none; }
.mini:hover:not(:disabled) { background: var(--hov); border-color: var(--bd2); }
.mini.icon { width: 24px; padding: 0; background: none; border-color: transparent; color: var(--dim); }
.mini.icon:hover { color: var(--tx); }
.hint { font-size: 11px; color: var(--dim); }
.hint.note { display: flex; flex-wrap: wrap; gap: 2px 8px; align-items: baseline; }
.link { color: var(--ac); text-decoration: underline; cursor: pointer; font-size: 11px; }

/* password */
.pwbox { position: relative; }
.pwbox .input { width: 100%; padding-right: 34px; }
.eye { position: absolute; right: 4px; top: 50%; transform: translateY(-50%); width: 26px; height: 26px; border: 0; background: none; color: var(--dim); border-radius: 7px; display: inline-flex; align-items: center; justify-content: center; }
.eye:hover { color: var(--tx); background: var(--hov); }
.save-pw { display: flex; align-items: center; gap: 12px; min-height: 18px; flex-wrap: wrap; }
.check { display: inline-flex; align-items: center; gap: 7px; font-size: 12px; color: var(--tx); }
.check input { accent-color: var(--ac); width: 14px; height: 14px; margin: 0; }

/* environment card: preset segments, then color + read-only, then an explanation */
.env { display: flex; flex-direction: column; gap: 10px; padding: 12px 14px; }
.edot { width: 8px; height: 8px; border-radius: 50%; flex: none; }
.edot.none { box-shadow: inset 0 0 0 1px var(--dim); }
.seg .custom { display: inline-flex; align-items: center; padding: 0 10px; font-size: 11.5px; color: var(--tx); font-weight: 500; border-radius: calc(var(--r) - 3px); background: var(--bg3); box-shadow: 0 0 0 1px var(--bd2); white-space: nowrap; }
.safety { display: flex; align-items: center; justify-content: space-between; gap: 16px; min-height: 24px; }
.swatches { display: flex; align-items: center; gap: 10px; }
.swatches .lbl { margin-right: 4px; }
.sw { width: 20px; height: 20px; border-radius: 50%; border: 1.5px solid transparent; padding: 0; position: relative; flex: none; }
.sw.none { border-color: var(--dim); background: transparent; }
.sw.none::after { content: ""; position: absolute; left: 2px; right: 2px; top: 50%; border-top: 1.5px solid var(--dim); transform: rotate(-45deg); }
.sw.on { box-shadow: 0 0 0 2px var(--bg2), 0 0 0 4px var(--c, var(--tx)); }
.sw:focus-visible { outline: none; box-shadow: 0 0 0 2px var(--bg2), 0 0 0 4px var(--ring); }
.roRow { display: flex; align-items: center; gap: 10px; }
.envnote { display: flex; align-items: flex-start; gap: 7px; font-size: 11.5px; line-height: 1.45; color: var(--dim); padding: 7px 10px; border-radius: var(--r); background: var(--bg); border: 1px solid var(--bd); }
.envnote > :deep(svg) { flex: none; margin-top: 2px; }
.envnote > span { flex: 1; min-width: 0; }
.envnote b { font-weight: 600; }
.envnote.prod { color: var(--tx); border-color: color-mix(in srgb, var(--conn-red) 45%, transparent); background: color-mix(in srgb, var(--conn-red) 9%, transparent); }
.envnote.prod > :deep(svg) { color: var(--conn-red); }
.envnote.warn { align-items: center; color: var(--warn); border-color: color-mix(in srgb, var(--warn) 40%, transparent); background: color-mix(in srgb, var(--warn) 8%, transparent); }
.envnote.warn > :deep(svg) { margin-top: 0; }

/* advanced disclosure */
.adv { margin-top: -4px; }
.advhead { display: inline-flex; align-items: center; gap: 6px; background: none; border: 0; color: var(--dim); font-size: 11.5px; padding: 2px 0; }
.advhead:hover { color: var(--tx); }
.advbody { padding-top: 8px; }

.tg { width: 34px; height: 20px; border-radius: 99px; background: var(--bd2); position: relative; display: inline-block; flex: none; transition: background .12s; }
.tg::after { content: ""; position: absolute; top: 2px; left: 2px; width: 16px; height: 16px; border-radius: 50%; background: #fff; transition: left .12s; box-shadow: 0 1px 2px #0004; }
.tg.on { background: var(--ac); }
.tg.on::after { left: 16px; }
.tg:focus-visible { outline: none; box-shadow: 0 0 0 3px var(--ring); }

.result { border: 1px solid; border-radius: var(--r); padding: 8px 10px; font-size: 12px; }
.result.ok { border-color: color-mix(in srgb, var(--ok) 40%, transparent); background: color-mix(in srgb, var(--ok) 10%, transparent); color: var(--ok); }
.result.warn { border-color: color-mix(in srgb, var(--warn) 45%, transparent); background: color-mix(in srgb, var(--warn) 10%, transparent); color: var(--warn); }
.result.fail { border-color: color-mix(in srgb, var(--danger) 40%, transparent); background: color-mix(in srgb, var(--danger) 10%, transparent); color: var(--danger); }
.ftr { display: flex; align-items: center; gap: 8px; width: 100%; }
.spinner { width: 12px; height: 12px; border-radius: 50%; border: 2px solid var(--bd); border-top-color: var(--ac); animation: spin .7s linear infinite; flex: none; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
