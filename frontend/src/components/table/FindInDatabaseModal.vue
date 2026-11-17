<script setup lang="ts">
// Registered as the "findInDatabase" modal (Cmd+Shift+F): search a value across the text columns of
// selected tables. Cancellable; results grouped by table; click a row to open the table filtered on it.
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import {
  apiError, dataApi, newId, onFindProgress, queryApi, schemaApi, type FindProgress, type CellValue, type Driver, type FindResult, type SchemaIndexTable,
} from "@/api";
import Modal from "@/components/common/Modal.vue";
import Icon from "@/components/common/Icon.vue";
import { useProjectsStore } from "@/stores/projects";
import { useTabsStore } from "@/stores/tabs";
import { useUiStore } from "@/stores/ui";
import { quoteIdent, sqlLiteral } from "@/lib/copyFormats";
import { renderCell } from "@/lib/format";

const props = defineProps<{ connId?: string }>();
const ui = useUiStore();
const tabs = useTabsStore();
const projects = useProjectsStore();

const connId = ref(props.connId || projects.projectConnections[0]?.id || projects.connections[0]?.id || "");
const progress = ref<FindProgress | null>(null);
let offProgress: (() => void) | null = null;
const conn = computed(() => projects.getConnection(connId.value));
const term = ref("");
const mode = ref("contains");
const caseSensitive = ref(false);
const limit = ref(50);
const schemaName = ref("");
const allTables = ref<SchemaIndexTable[]>([]);
const picked = ref<Set<string>>(new Set());
const tableFilter = ref("");
const showTables = ref(false);
const running = ref(false);
const error = ref("");
const results = ref<FindResult[] | null>(null);
const durationMs = ref(0);
let searchId = "";
let inflight: { cancel: () => void } | null = null;

async function loadTables() {
  allTables.value = [];
  picked.value = new Set();
  if (!connId.value) return;
  try {
    const idx = await schemaApi.SearchSchema(connId.value, schemaName.value, false);
    allTables.value = idx.tables;
  } catch {
    allTables.value = [];
  }
}
onMounted(() => {
  loadTables();
  offProgress = onFindProgress((p) => {
    if (p.searchId === searchId) progress.value = p;
  });
});
onBeforeUnmount(() => {
  cancel();
  offProgress?.();
});

const visibleTables = computed(() => {
  const f = tableFilter.value.toLowerCase();
  return allTables.value.filter((t) => !f || t.name.toLowerCase().includes(f));
});
function toggle(name: string) {
  const s = new Set(picked.value);
  if (s.has(name)) s.delete(name);
  else s.add(name);
  picked.value = s;
}
const scopeText = computed(() => (picked.value.size ? `${picked.value.size} selected table${picked.value.size > 1 ? "s" : ""}` : `all ${allTables.value.length} tables`));

async function run() {
  if (!term.value.trim() || running.value) return;
  running.value = true;
  error.value = "";
  results.value = null;
  progress.value = null;
  searchId = newId();
  const t0 = performance.now();
  const p = dataApi.FindInDatabase({
    connId: connId.value, searchId, schema: schemaName.value, tables: [...picked.value], term: term.value,
    mode: mode.value, caseSensitive: caseSensitive.value, limitPerTable: limit.value,
  });
  inflight = p;
  try {
    results.value = (await p) as FindResult[];
    durationMs.value = Math.round(performance.now() - t0);
  } catch (e) {
    const err = apiError(e);
    if (err.code === "canceled") error.value = "Search cancelled.";
    else if (err.code === "not_implemented") error.value = "Find in database is not implemented in the backend yet.";
    else error.value = err.message || err.code;
  } finally {
    if (inflight === p) inflight = null;
    running.value = false;
  }
}
function cancel() {
  if (!running.value) return;
  // cancel on the backend only: the pending FindInDatabase call then rejects with "canceled" and is handled in run()
  queryApi.CancelQuery(searchId).catch(() => undefined);
}

function open(fr: FindResult, rowIdx: number) {
  const cols = fr.result.columns;
  const row = fr.result.rows[rowIdx];
  const driver = (conn.value?.driver ?? "") as Driver | "";
  const pk = cols.map((c, i) => ({ c, i })).filter(({ c }) => c.pk);
  const use = pk.length ? pk : cols.map((c, i) => ({ c, i })).filter(({ c }) => fr.matchedColumns.includes(c.name));
  const where = use
    .map(({ c, i }) => (row[i] === null ? `${quoteIdent(driver, c.name)} IS NULL` : `${quoteIdent(driver, c.name)} = ${sqlLiteral(row[i] as CellValue, driver)}`))
    .join(" AND ");
  tabs.openTable(connId.value, fr.schema, fr.table, { where, orderBy: "", focus: true });
  ui.closeModal();
}
function cellText(fr: FindResult, r: number, c: number): string {
  return renderCell(fr.result.rows[r][c], fr.result.columns[c]).text;
}
function shownCols(fr: FindResult): number[] {
  const cols = fr.result.columns;
  const m = cols.map((c, i) => (fr.matchedColumns.includes(c.name) ? i : -1)).filter((i) => i >= 0);
  const rest = cols.map((_, i) => i).filter((i) => !m.includes(i));
  return [...m, ...rest].slice(0, 6);
}
const totalRows = computed(() => (results.value ?? []).reduce((n, r) => n + r.result.rows.length, 0));
</script>

<template>
  <Modal title="Find in database" :width="760" @close="cancel(); ui.closeModal()">
    <form class="flex flex-wrap items-center gap-2" @submit.prevent="run">
      <select v-if="projects.projectConnections.length > 1" v-model="connId" class="input" style="height: 24px; max-width: 180px" @change="loadTables">
        <option v-for="c in projects.projectConnections" :key="c.id" :value="c.id">{{ c.name }}</option>
      </select>
      <input v-model="term" data-autofocus class="input mono" style="flex: 1; min-width: 200px; height: 24px" placeholder="Value to search for" spellcheck="false" />
      <select v-model="mode" class="input" style="height: 24px">
        <option value="contains">contains</option><option value="exact">exact</option><option value="starts_with">starts with</option>
      </select>
      <label class="flex items-center gap-1"><input v-model="caseSensitive" type="checkbox" /> Aa</label>
      <button v-if="!running" class="btn primary" type="submit" :disabled="!term.trim()"><Icon name="search" /> Search</button>
      <button v-else class="btn danger" type="button" @click="cancel"><Icon name="stop" /> Cancel</button>
    </form>
    <div class="flex items-center gap-2 text-[11px]">
      <button class="btn ghost" type="button" @click="showTables = !showTables"><Icon :name="showTables ? 'chevD' : 'chevR'" :size="10" /> Tables: {{ scopeText }}</button>
      <span class="dim">limit per table</span>
      <input v-model.number="limit" type="number" min="1" max="1000" class="input" style="width: 64px; height: 20px" />
    </div>
    <div v-if="showTables" class="tbls">
      <div class="flex gap-2 p-1.5">
        <input v-model="tableFilter" class="input" style="flex: 1; height: 22px" placeholder="Filter tables" />
        <button class="btn ghost" type="button" @click="picked = new Set(visibleTables.map((t) => t.name))">All shown</button>
        <button class="btn ghost" type="button" @click="picked = new Set()">None</button>
      </div>
      <div class="tbl-list">
        <label v-for="t in visibleTables" :key="t.name" class="tbl-it mono"><input type="checkbox" :checked="picked.has(t.name)" @change="toggle(t.name)" /> {{ t.name }}</label>
      </div>
    </div>
    <div v-if="running" class="dim">
      Searching {{ scopeText }}…
      <template v-if="progress"> {{ progress.done }} / {{ progress.total }} tables · {{ progress.hits }} with matches · {{ progress.table }}</template>
    </div>
    <div v-if="error" class="err selectable">{{ error }}</div>
    <template v-if="results">
      <div class="dim">{{ totalRows }} row{{ totalRows === 1 ? "" : "s" }} in {{ results.length }} table{{ results.length === 1 ? "" : "s" }} · {{ durationMs }} ms</div>
      <div v-for="fr in results" :key="fr.schema + '.' + fr.table" class="grp">
        <div class="grp-h">
          <Icon name="table" class="dim" /> <b class="mono">{{ fr.table }}</b>
          <span class="dim">{{ fr.result.rows.length }} row{{ fr.result.rows.length === 1 ? "" : "s" }} · in {{ fr.matchedColumns.join(", ") }}</span>
        </div>
        <table class="rt mono">
          <thead><tr><th v-for="c in shownCols(fr)" :key="c">{{ fr.result.columns[c].name }}</th></tr></thead>
          <tbody>
            <tr v-for="(_, r) in fr.result.rows" :key="r" @click="open(fr, r)">
              <td v-for="c in shownCols(fr)" :key="c" :class="{ hit: fr.matchedColumns.includes(fr.result.columns[c].name) }">{{ cellText(fr, r, c) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="!results.length" class="dim">No matches.</div>
    </template>
  </Modal>
</template>

<style scoped>
.err { color: var(--danger); border: 1px solid var(--danger); border-radius: 4px; padding: 6px 8px; white-space: pre-wrap; }
.tbls { border: 1px solid var(--bd); border-radius: 4px; background: var(--bg); }
.tbl-list { max-height: 160px; overflow: auto; columns: 2; padding: 0 8px 6px; }
.tbl-it { display: flex; gap: 6px; align-items: center; font-size: 11px; break-inside: avoid; }
.grp { flex: none; border: 1px solid var(--bd); border-radius: 4px; overflow: hidden; }
.grp-h { display: flex; gap: 8px; align-items: center; padding: 4px 8px; background: var(--bg); border-bottom: 1px solid var(--bd); }
.rt { width: 100%; border-collapse: collapse; font-size: 11px; }
.rt th { text-align: left; padding: 2px 8px; color: var(--dim); font-weight: 500; border-bottom: 1px solid var(--bd); }
.rt td { padding: 2px 8px; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rt tbody tr:hover { background: var(--hov); cursor: default; }
.rt td.hit { color: var(--warn); }
</style>
