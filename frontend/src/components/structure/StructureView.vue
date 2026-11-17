<script setup lang="ts">
// Structure tab (data agent): columns, indexes, foreign keys (out + referenced by), triggers and DDL.
// Structure edits (drop / rename / add column, drop index) are staged and need Save with an SQL preview,
// the same flow as data edits.
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { kbd } from "@/lib/kbd";
import {
  apiError, schemaApi, newId,
  type ApplyResult, type Column, type Driver, type ForeignKey, type Index, type Table, type TableStructure, type Trigger,
} from "@/api";
import { useProjectsStore } from "@/stores/projects";
import { useTabsStore } from "@/stores/tabs";
import { useUiStore } from "@/stores/ui";
import type { ModalName, Tab } from "@/types";
import Icon from "@/components/common/Icon.vue";
import SqlEditor from "@/components/console/SqlEditor.vue";
import { copyText } from "@/lib/clipboard";
import { isMod } from "@/lib/keys";
import { fmtNum } from "@/lib/format";

const props = defineProps<{ tab: Tab; active: boolean }>();
const projects = useProjectsStore();
const tabs = useTabsStore();
const ui = useUiStore();

const connId = computed(() => props.tab.connId);
const schema = computed(() => props.tab.schema ?? "");
const table = computed(() => props.tab.table ?? "");
const conn = computed(() => projects.getConnection(connId.value));
const driver = computed(() => (conn.value?.driver ?? "") as Driver | "");
const readOnly = computed(() => projects.isReadOnly(connId.value));
const tint = computed(() => projects.connColor(connId.value));
const hasTint = computed(() => !!conn.value && conn.value.color !== "none");

// ---- loading ----------------------------------------------------------------------------------
const loading = ref(false);
const error = ref("");
const meta = ref<Table | null>(null);
const columns = ref<Column[]>([]);
const indexes = ref<Index[]>([]);
const fks = ref<ForeignKey[]>([]);
const refBy = ref<ForeignKey[]>([]);
const triggers = ref<Trigger[]>([]);
const ddl = ref("");

async function load() {
  loading.value = true;
  error.value = "";
  try {
    if (!(await projects.ensureConnected(connId.value))) throw new Error("Not connected");
    const st: TableStructure = await schemaApi.TableStructure(connId.value, schema.value, table.value);
    meta.value = st.table;
    columns.value = st.columns;
    indexes.value = st.indexes;
    fks.value = st.foreignKeys;
    refBy.value = st.referencedBy;
    triggers.value = st.triggers;
    ddl.value = st.ddl;
  } catch (e) {
    error.value = apiError(e).message || String(e);
  } finally {
    loading.value = false;
  }
}
onMounted(() => {
  load();
  window.addEventListener("keydown", onWinKey);
});
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onWinKey);
  tabs.setDirty(props.tab.id, false);
});

// ---- staged structure edits -----------------------------------------------------------------------
interface NewCol { id: string; name: string; type: string; nullable: boolean; def: string }
const dropCols = reactive(new Set<string>());
const renames = reactive(new Map<string, string>());
const dropIdx = reactive(new Set<string>());
const adds = ref<NewCol[]>([]);
const renaming = ref<string | null>(null);
const renameText = ref("");

/** any staged change or open draft row (Revert is available for all of them) */
const anyStaged = computed(() => dropCols.size + renames.size + dropIdx.size + adds.value.length > 0);
/** a named new column without a type can't be saved */
const invalidDraft = computed(() => adds.value.some((a) => a.name.trim() && !a.type.trim()));
const pending = computed(() => dropCols.size + renames.size + dropIdx.size + adds.value.filter((a) => a.name.trim()).length);
watch(pending, (n) => tabs.setDirty(props.tab.id, n > 0), { immediate: true });
watch(() => [props.tab.table, props.tab.schema], () => revertAll());

function guard(): boolean {
  if (readOnly.value) {
    ui.toast("Connection is read-only", "warning");
    return false;
  }
  return true;
}
function toggleDrop(name: string) {
  if (!guard()) return;
  if (dropCols.has(name)) dropCols.delete(name);
  else {
    dropCols.add(name);
    renames.delete(name);
  }
}
let renameEl: HTMLInputElement | null = null;
const setRenameEl = (el: unknown) => (renameEl = el as HTMLInputElement | null);
async function startRename(name: string) {
  if (!guard() || dropCols.has(name)) return;
  renaming.value = name;
  renameText.value = renames.get(name) ?? name;
  await nextTick();
  renameEl?.focus();
  renameEl?.select();
}
function commitRename() {
  const old = renaming.value;
  if (!old) return;
  const nn = renameText.value.trim();
  renaming.value = null;
  if (!nn || nn === old) renames.delete(old);
  else if (columns.value.some((c) => c.name === nn && c.name !== old)) ui.toast(`Column "${nn}" already exists`, "warning");
  else renames.set(old, nn);
}
function toggleDropIndex(name: string) {
  if (!guard()) return;
  if (dropIdx.has(name)) dropIdx.delete(name);
  else dropIdx.add(name);
}
function addColumn() {
  if (!guard()) return;
  adds.value.push({ id: newId(), name: "", type: driver.value === "postgres" ? "text" : driver.value === "sqlite" ? "TEXT" : "varchar(255)", nullable: true, def: "" });
}
function revertAll() {
  dropCols.clear();
  renames.clear();
  dropIdx.clear();
  adds.value = [];
  renaming.value = null;
}
async function revertConfirm() {
  if (!anyStaged.value) return;
  const ok = await ui.confirm({ title: "Revert all changes?", message: "Discard the staged structure changes?", confirmText: "Revert all", danger: true });
  if (ok) revertAll();
}

function changeRequest() {
  return {
    connId: connId.value, schema: schema.value, table: table.value,
    dropColumns: [...dropCols], renameColumns: Object.fromEntries(renames), dropIndexes: [...dropIdx],
    addColumns: adds.value.filter((a) => a.name.trim()).map((a) => ({ name: a.name.trim(), type: a.type.trim(), nullable: a.nullable, default: a.def.trim() || null, after: "" })),
  };
}

// ---- save --------------------------------------------------------------------------------------------------------
async function runApply(): Promise<{ ok: boolean; error: string; failedIndex: number }> {
  try {
    const res: ApplyResult = await schemaApi.ApplyStructureChanges(changeRequest());
    return { ok: res.ok, error: res.error, failedIndex: res.failedIndex };
  } catch (e) {
    const err = apiError(e);
    return { ok: false, error: err.message || err.code, failedIndex: -1 };
  }
}

async function save() {
  if (!guard() || !pending.value || invalidDraft.value) return;
  let statements: string[];
  try {
    statements = await schemaApi.PreviewStructureChanges(changeRequest());
  } catch (e) {
    // validation errors (invalid type, unknown column...) are shown as they are: nothing is generated client-side
    const err = apiError(e);
    return void ui.openModal("saveChanges" as ModalName, {
      title: `Alter ${table.value}`, statements: [], error: err.message || err.code, failedIndex: -1,
    });
  }
  if (!statements.length) return void ui.toast("Nothing to save", "info", 1500);
  const sql = statements.map((s) => s + ";");
  if (conn.value?.color === "red" && !conn.value.readOnly) {
    // Production connection: the confirm dialog doubles as the SQL preview.
    const ok = await ui.confirm({
      title: "Alter a production table?",
      message: `"${conn.value.name}" is marked red. These statements will run:`,
      sql: sql.join("\n"),
      confirmText: "Run",
      danger: true,
    });
    if (!ok) return;
    const res = await runApply();
    if (res.ok) return void (await done());
    return void ui.openModal("saveChanges" as ModalName, {
      title: `Alter ${table.value}`, statements: sql, error: res.error, failedIndex: res.failedIndex, apply: runApply, onDone: done, confirmText: "Run",
    });
  }
  ui.openModal("saveChanges" as ModalName, {
    title: `Alter ${table.value}`, statements: sql, apply: runApply, onDone: done, confirmText: "Run",
    note: "Structure changes can lock the table and are not always transactional (MySQL commits DDL immediately).",
  });
}
async function done() {
  ui.toast("Structure changes applied", "success");
  revertAll();
  await load();
  ui.refreshConnection?.(connId.value);
}

function onWinKey(e: KeyboardEvent) {
  if (!props.active || ui.overlayOpen) return;
  const mod = isMod(e);
  const k = e.key.toLowerCase();
  if (mod && k === "s") {
    e.preventDefault();
    e.stopPropagation();
    save();
  } else if ((mod && k === "r") || e.key === "F5") {
    e.preventDefault();
    e.stopPropagation();
    load();
  } else if (mod && k === "z" && !(e.target as HTMLElement | null)?.closest("input,textarea,.cm-editor") && pending.value) {
    e.preventDefault();
    undo();
  }
}
/** Undo for structure edits: drops the most recently staged item (adds, then index drops, renames, drops). */
function undo() {
  if (adds.value.length) adds.value.pop();
  else if (dropIdx.size) dropIdx.delete([...dropIdx].pop()!);
  else if (renames.size) renames.delete([...renames.keys()].pop()!);
  else if (dropCols.size) dropCols.delete([...dropCols].pop()!);
}

// ---- display helpers ----------------------------------------------------------------------------------------------
function openTable(sch: string, name: string, where = "") {
  tabs.openTable(connId.value, sch || schema.value, name, where ? { where, focus: true } : { focus: true });
}
function keyFlags(c: Column): string[] {
  const f: string[] = [];
  if (c.pk) f.push("PK");
  if (c.fk) f.push("FK");
  if (c.autoIncrement) f.push("AI");
  if (c.generated) f.push("GEN");
  return f;
}
const kind = computed(() => (meta.value?.kind ?? "table").replace("_", " "));
const copied = ref(false);
async function copyDdl() {
  if (await copyText(ddl.value)) {
    copied.value = true;
    setTimeout(() => (copied.value = false), 1200);
  }
}
const colCount = computed(() => columns.value.length);
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col" :style="{ '--conn': tint }">
    <div class="tb">
      <Icon name="columns" class="dim" />
      <b class="mono">{{ schema ? schema + "." : "" }}{{ table }}</b>
      <span class="badge">{{ kind }}</span>
      <span v-if="meta && meta.estRows >= 0" class="dim mono">~{{ fmtNum(meta.estRows) }} rows</span>
      <span class="flex-1"></span>
      <span v-if="readOnly" class="pill ro" title="Read-only connection: structure edits are disabled"><Icon name="lock" :size="10" /> READ-ONLY</span>
      <button class="btn ghost" :title="kbd('Refresh (⌘R)')" @click="load"><Icon name="refresh" /></button>
      <button class="btn ghost" @click="openTable(schema, table)"><Icon name="table" /> Open data</button>
      <span class="sep"></span>
      <button class="btn ghost" title="Add column" @click="addColumn"><Icon name="plus" /> Add column</button>
      <button class="btn ghost" :disabled="!anyStaged" @click="revertConfirm"><Icon name="undo" /> Revert</button>
      <button class="btn primary" :disabled="!pending || readOnly || invalidDraft" :title="kbd(invalidDraft ? 'Give every new column a type' : 'Preview and save (⌘S)')" @click="save">
        <Icon name="save" /> Save <span v-if="pending" class="bdg">{{ pending }}</span>
      </button>
    </div>

    <div v-if="error" class="p-4" style="color: var(--danger)">
      <b>Could not load the structure</b>
      <div class="mono" style="color: var(--tx); white-space: pre-wrap">{{ error }}</div>
      <button class="btn mt-2" @click="load">Retry</button>
    </div>
    <div v-else class="body selectable" tabindex="0" data-focus-target>
      <div v-if="loading && !columns.length" class="dim p-4">Loading…</div>

      <!-- columns -->
      <section>
        <h4>Columns <span class="dim">{{ colCount }}</span></h4>
        <table class="st">
          <thead><tr><th class="n">#</th><th>Name</th><th>Type</th><th>Null</th><th>Default</th><th>Key</th><th>Comment</th><th class="act"></th></tr></thead>
          <tbody>
            <tr v-for="c in columns" :key="c.name" :class="{ del: dropCols.has(c.name), chg: renames.has(c.name) }">
              <td class="n dim">{{ c.ordinal }}</td>
              <td class="mono">
                <Icon v-if="c.pk" name="key" :size="11" class="pk" /><Icon v-else-if="c.fk" name="link" :size="11" class="fk" />
                <input
                  v-if="renaming === c.name" :ref="setRenameEl" v-model="renameText" class="input mono inl"
                  @keydown.enter.prevent="commitRename" @keydown.esc.stop.prevent="renaming = null" @blur="commitRename"
                />
                <span v-else class="nm" title="Double-click to rename" @dblclick="startRename(c.name)">
                  <template v-if="renames.has(c.name)"><s class="dim">{{ c.name }}</s> → {{ renames.get(c.name) }}</template><template v-else>{{ c.name }}</template>
                </span>
              </td>
              <td class="mono">{{ c.type }}</td>
              <td>{{ c.nullable ? "YES" : "NO" }}</td>
              <td class="mono"><span v-if="c.default !== null && c.default !== undefined">{{ c.default }}</span><span v-else class="null">{{ c.nullable ? "NULL" : "" }}</span></td>
              <td><span v-for="f in keyFlags(c)" :key="f" class="badge mr">{{ f }}</span></td>
              <td class="dim">{{ c.comment }}</td>
              <td class="act">
                <button class="btn ghost" title="Rename" @click="startRename(c.name)"><Icon name="edit" :size="12" /></button>
                <button class="btn ghost" :title="dropCols.has(c.name) ? 'Keep column' : 'Drop column'" @click="toggleDrop(c.name)"><Icon :name="dropCols.has(c.name) ? 'undo' : 'trash'" :size="12" /></button>
              </td>
            </tr>
            <tr v-for="a in adds" :key="a.id" class="new">
              <td class="n"><span class="plus">+</span></td>
              <td><input v-model="a.name" class="input mono inl" placeholder="column_name" spellcheck="false" /></td>
              <td><input v-model="a.type" class="input mono inl" placeholder="type" spellcheck="false" /></td>
              <td><label class="flex items-center gap-1"><input v-model="a.nullable" type="checkbox" /> {{ a.nullable ? "YES" : "NO" }}</label></td>
              <td><input v-model="a.def" class="input mono inl" placeholder="SQL default expr" spellcheck="false" /></td>
              <td></td><td></td>
              <td class="act"><button class="btn ghost" title="Remove" @click="adds = adds.filter((x) => x.id !== a.id)"><Icon name="x" :size="12" /></button></td>
            </tr>
          </tbody>
        </table>
      </section>

      <!-- indexes -->
      <section>
        <h4>Indexes <span class="dim">{{ indexes.length }}</span></h4>
        <table v-if="indexes.length" class="st">
          <thead><tr><th>Name</th><th>Columns</th><th>Unique</th><th>Method</th><th>Condition</th><th class="act"></th></tr></thead>
          <tbody>
            <tr v-for="i in indexes" :key="i.name" :class="{ del: dropIdx.has(i.name) }">
              <td class="mono">{{ i.name }} <span v-if="i.primary" class="badge">PRIMARY</span></td>
              <td class="mono">{{ i.columns.join(", ") }}</td>
              <td>{{ i.unique ? "YES" : "" }}</td>
              <td class="dim mono">{{ i.method }}</td>
              <td class="dim mono">{{ i.where }}</td>
              <td class="act"><button v-if="!i.primary" class="btn ghost" :title="dropIdx.has(i.name) ? 'Keep index' : 'Drop index'" @click="toggleDropIndex(i.name)"><Icon :name="dropIdx.has(i.name) ? 'undo' : 'trash'" :size="12" /></button></td>
            </tr>
          </tbody>
        </table>
        <div v-else class="dim empty">No indexes</div>
      </section>

      <!-- foreign keys -->
      <section>
        <h4>Foreign keys <span class="dim">{{ fks.length }}</span></h4>
        <table v-if="fks.length" class="st">
          <thead><tr><th>Name</th><th>Columns</th><th>References</th><th>On update</th><th>On delete</th></tr></thead>
          <tbody>
            <tr v-for="f in fks" :key="f.name + f.columns.join()">
              <td class="mono">{{ f.name }}</td>
              <td class="mono">{{ f.columns.join(", ") }}</td>
              <td class="mono"><a class="lnk" @click="openTable(f.refSchema, f.refTable)">{{ f.refTable }}</a> ({{ f.refColumns.join(", ") }})</td>
              <td class="dim">{{ f.onUpdate }}</td><td class="dim">{{ f.onDelete }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="dim empty">No outgoing foreign keys</div>
      </section>

      <section>
        <h4>Referenced by <span class="dim">{{ refBy.length }}</span></h4>
        <table v-if="refBy.length" class="st">
          <thead><tr><th>Table</th><th>Columns</th><th>References</th><th>On update</th><th>On delete</th></tr></thead>
          <tbody>
            <tr v-for="f in refBy" :key="f.table + f.name">
              <td class="mono"><a class="lnk" @click="openTable(f.schema, f.table)">{{ f.table }}</a></td>
              <td class="mono">{{ f.columns.join(", ") }}</td>
              <td class="mono">{{ f.refColumns.join(", ") }}</td>
              <td class="dim">{{ f.onUpdate }}</td><td class="dim">{{ f.onDelete }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="dim empty">No incoming foreign keys</div>
      </section>

      <section v-if="triggers.length">
        <h4>Triggers <span class="dim">{{ triggers.length }}</span></h4>
        <table class="st">
          <thead><tr><th>Name</th><th>Timing</th><th>Event</th><th>Definition</th></tr></thead>
          <tbody>
            <tr v-for="t in triggers" :key="t.name">
              <td class="mono">{{ t.name }}</td><td>{{ t.timing }}</td><td>{{ t.event }}</td>
              <td class="mono dim trunc" :title="t.definition">{{ t.definition }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <section>
        <h4>
          DDL
          <button class="btn ghost" :disabled="!ddl" @click="copyDdl"><Icon :name="copied ? 'check' : 'copy'" /> {{ copied ? "Copied" : "Copy" }}</button>
        </h4>
        <div v-if="ddl" class="ddl"><SqlEditor :model-value="ddl" :driver="driver" :tables="[]" read-only :line-numbers="false" /></div>
        <div v-else class="dim empty">No DDL available</div>
      </section>
    </div>

  </div>
</template>

<style scoped>
.tb { display: flex; align-items: center; gap: 8px; padding: 4px 10px; border-bottom: 1px solid var(--bd); flex: none; flex-wrap: wrap; }
.sep { width: 1px; height: 16px; background: var(--bd); }
.bdg { background: color-mix(in srgb, var(--ac-fg, #fff) 22%, transparent); color: var(--ac-fg, #fff); border-radius: 8px; padding: 0 5px; font-size: 10px; line-height: 14px; }
.pill.ro { display: inline-flex; align-items: center; gap: 4px; }
.body { flex: 1; overflow: auto; padding: 4px 14px 24px; }
section { margin-top: 14px; }
h4 { margin: 0 0 4px; display: flex; align-items: center; gap: 8px; font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--dim); font-weight: 600; }
.st { width: 100%; border-collapse: collapse; font-size: 12px; }
.st th { text-align: left; font: 600 11px var(--sans); color: var(--dim); padding: 3px 8px; border-bottom: 1px solid var(--bd); background: var(--bg2); position: sticky; top: 0; }
.st td { padding: 0 8px; height: 24px; border-bottom: 1px solid var(--bd); white-space: nowrap; }
.st tbody tr:hover { background: var(--hov); }
.st .n { width: 34px; text-align: right; }
.st .act { width: 64px; text-align: right; white-space: nowrap; }
.st .act .btn { padding: 0 4px; height: 20px; opacity: .6; }
.st tr:hover .act .btn { opacity: 1; }
.st tr.del td { background: var(--chg-del-bg); text-decoration: line-through; color: var(--dim); }
.st tr.chg td { background: var(--chg-upd-bg); }
.st tr.new td { background: var(--chg-new-bg); }
.st .pk { color: var(--pk); margin-right: 4px; vertical-align: -1px; }
.st .fk { color: var(--fk); margin-right: 4px; vertical-align: -1px; }
.st .nm { cursor: default; }
.st .inl { height: 20px; width: 100%; min-width: 90px; }
.st .trunc { max-width: 520px; overflow: hidden; text-overflow: ellipsis; }
.plus { color: var(--chg-new); }
.mr { margin-right: 4px; }
.lnk { color: var(--ac); cursor: pointer; }
.lnk:hover { text-decoration: underline; }
.empty { padding: 4px 8px; }
.ddl { height: 280px; border: 1px solid var(--bd); border-radius: 4px; overflow: hidden; }
</style>
