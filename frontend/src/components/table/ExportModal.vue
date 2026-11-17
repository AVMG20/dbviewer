<script setup lang="ts">
// Registered as the "export" modal. Scope: current page / selection (client side, browser download or
// clipboard) or all rows (backend Export: native save dialog, typed path fallback in the browser).
// ui.openModal("export", { connId, schema, table, page, hasSelection, loadedRows, getText })
// Console results pass `sql` (the result's statement) instead of `page`: "All rows" re-runs the query
// in the backend (read statements only) and streams it to the file; `table` names the INSERT target.
import { onBeforeUnmount, ref } from "vue";
import { apiError, dataApi, type PageRequest } from "@/api";
import Modal from "@/components/common/Modal.vue";
import { useUiStore } from "@/stores/ui";
import { copyText, downloadText } from "@/lib/clipboard";
import { FORMAT_FILE } from "@/lib/copyFormats";
import { fmtNum } from "@/lib/format";

const props = defineProps<{
  connId: string; schema: string; table: string; page?: PageRequest; sql?: string;
  hasSelection?: boolean; loadedRows?: number;
  getText?: (scope: "page" | "selection", format: string, header: boolean) => Promise<string | null> | string | null;
}>();
const ui = useUiStore();

const scope = ref<"page" | "selection" | "all">(props.hasSelection ? "selection" : "all"); // "selection" only for multi-cell selections
const format = ref("csv");
const header = ref(true);
const path = ref("");
const busy = ref(false);
const error = ref("");
const needsPath = ref(false);

let inflight: { cancel: () => void } | null = null;
let closed = false;
onBeforeUnmount(() => {
  closed = true;
  inflight?.cancel();
});
/** Closes only if this export dialog is still the open one (a later dialog must survive a late result). */
function closeSelf() {
  if (!closed && ui.modal?.name === "export") ui.closeModal();
}

async function run() {
  error.value = "";
  if (scope.value !== "all") return clientExport(false);
  busy.value = true;
  try {
    const p = dataApi.Export({
      connId: props.connId,
      ...(props.sql ? { sql: props.sql, schema: props.schema } : { page: { ...props.page!, offset: 0, limit: 0 }, sql: "", schema: "" }),
      format: format.value,
      path: path.value,
      includeHeader: header.value,
      sqlTable: props.table,
      columns: [],
    });
    inflight = p;
    const res = await p;
    if (res.cancelled || closed) return;
    closeSelf();
    ui.toast(`Exported ${fmtNum(res.rows)} rows to ${res.path}`, "success");
  } catch (e) {
    if (closed) return;
    const err = apiError(e);
    if (err.code === "canceled") return;
    error.value = err.code === "not_implemented" ? "Export is not implemented in the backend yet. Use “Current page” instead." : err.message || err.code;
    if (props.sql && err.code === "invalid_argument" && /statement/i.test(err.message)) error.value += "\n\nUse “Loaded rows” to export the rows shown in the console.";
    else if (!path.value && err.code !== "not_implemented" && err.code !== "db_error") needsPath.value = true;
  } finally {
    inflight = null;
    busy.value = false;
  }
}
async function clientExport(copy: boolean) {
  let text: string | null | undefined;
  try {
    text = await props.getText?.(scope.value as "page" | "selection", format.value, header.value);
  } catch (e) {
    return void (error.value = e instanceof Error ? e.message : String(e));
  }
  if (text === null || text === undefined) return void (error.value = "Nothing to export.");
  if (copy) {
    if (await copyText(text)) {
      ui.toast("Copied to clipboard", "success", 1800);
      closeSelf();
    } else error.value = "Could not access the clipboard.";
    return;
  }
  const f = FORMAT_FILE[format.value] ?? FORMAT_FILE.csv;
  downloadText(`${props.table}.${f.ext}`, text, f.mime);
  closeSelf();
}
</script>

<template>
  <Modal title="Export" :width="520" @close="ui.closeModal()">
    <div class="fr"><span class="dim">Scope</span>
      <div class="flex flex-col gap-1">
        <label class="flex items-center gap-2"><input v-model="scope" type="radio" value="page" /> {{ sql ? "Loaded rows" : "Current page" }} <span class="dim">({{ fmtNum(loadedRows ?? 0) }} loaded rows)</span></label>
        <label class="flex items-center gap-2" :class="{ dim: !hasSelection }"><input v-model="scope" type="radio" value="selection" :disabled="!hasSelection" /> Selection</label>
        <label class="flex items-center gap-2"><input v-model="scope" type="radio" value="all" /> All rows <span class="dim">{{ sql ? "(re-runs the query, streamed by the backend)" : "(whole filtered table, streamed by the backend)" }}</span></label>
      </div>
    </div>
    <div class="fr"><span class="dim">Format</span>
      <select v-model="format" class="input" style="height: 24px">
        <option value="csv">CSV</option><option value="tsv">TSV</option><option value="json">JSON</option>
        <option value="sql">SQL INSERT</option><option value="markdown">Markdown</option>
      </select>
    </div>
    <div v-if="format === 'csv' || format === 'tsv'" class="fr"><span class="dim">Header</span>
      <label class="flex items-center gap-2"><input v-model="header" type="checkbox" /> Include column names</label>
    </div>
    <div v-if="scope === 'all'" class="fr"><span class="dim">File path</span>
      <input v-model="path" class="input mono" :placeholder="needsPath ? '/path/to/export.csv' : 'Empty = choose in the save dialog'" spellcheck="false" />
    </div>
    <div v-if="needsPath" class="dim">The native save dialog is not available in the browser. Enter a path above, or export the current page instead.</div>
    <div v-if="error" class="err selectable">{{ error }}</div>
    <template #footer>
      <button v-if="scope !== 'all'" class="btn ghost" @click="clientExport(true)">Copy to clipboard</button>
      <span class="flex-1"></span>
      <button class="btn" @click="ui.closeModal()">Cancel</button>
      <button class="btn primary" :disabled="busy" @click="run">{{ busy ? "Exporting…" : scope === "all" ? "Export…" : "Download" }}</button>
    </template>
  </Modal>
</template>

<style scoped>
.fr { display: grid; grid-template-columns: 70px 1fr; gap: 8px; align-items: start; }
.err { color: var(--danger); border: 1px solid var(--danger); border-radius: 4px; padding: 6px 8px; white-space: pre-wrap; }
</style>
