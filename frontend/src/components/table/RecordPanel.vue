<script setup lang="ts">
// Record (form) view of the active row, in a side panel. FK values are clickable.
// Truncated long text is read-only until its full value is loaded; blobs are read-only.
import { computed, reactive, ref, watch } from "vue";
import { isBinary, isLongText, type CellValue, type ResultColumn } from "@/api";
import { cellText, coerceInput, isBlankInvalid, isDefaultValue, needsPopover, renderCell, validateInput } from "@/lib/format";
import { toastOnce } from "@/lib/notify";
import type { GridRow } from "@/lib/gridTypes";
import Icon from "@/components/common/Icon.vue";
import { isMod } from "@/lib/keys";
import type { NavEntry } from "@/lib/fkNav";

const props = defineProps<{
  columns: ResultColumn[]; row: GridRow | null; rowNumber: number; editable: boolean;
  loadFull?: (col: number) => Promise<CellValue | null>;
  incoming?: NavEntry[];
  navCols?: Set<string>;
}>();
const emit = defineEmits<{
  edit: [col: number, value: CellValue];
  "open-fk": [col: number];
  nav: [col: number, x: number, y: number];
  "open-entry": [e: NavEntry];
  "edit-blocked": [];
  close: [];
}>();

const filter = ref("");
const visible = computed(() =>
  props.columns.map((c, i) => ({ c, i })).filter(({ c }) => !filter.value || c.name.toLowerCase().includes(filter.value.toLowerCase())),
);
/** full values loaded for truncated cells of the current row */
const fulls = reactive<Record<number, CellValue>>({});
const loadingFull = reactive<Record<number, boolean>>({});
/** inline validation errors per field; a field with an error is not staged */
const errors = reactive<Record<number, string>>({});
watch(() => props.row?.key, () => {
  for (const k of Object.keys(errors)) delete errors[Number(k)];
  for (const k of Object.keys(fulls)) delete fulls[Number(k)];
});

function value(i: number): CellValue | undefined {
  return fulls[i] ?? props.row?.values[i];
}
function isBlob(i: number): boolean {
  return props.columns[i].kind === "binary" || isBinary(value(i) as CellValue);
}
function isCut(i: number): boolean {
  const v = value(i);
  return i in fulls ? false : isLongText(v as CellValue) && !!(v as { truncated: boolean }).truncated;
}
function text(i: number): string {
  if (isBlob(i)) return renderCell(value(i), props.columns[i]).text;
  return cellText(value(i));
}
function isNull(i: number) {
  const v = value(i);
  return v === null || v === undefined;
}
function readOnlyField(i: number): boolean {
  return isBlob(i) || isCut(i) || props.row?.state === "deleted";
}
async function loadFullValue(i: number) {
  if (!props.loadFull) return;
  loadingFull[i] = true;
  const v = await props.loadFull(i);
  loadingFull[i] = false;
  if (v === null) toastOnce("Could not load the full value", "warning");
  else fulls[i] = v;
}
function commit(i: number, e: Event) {
  const el = e.target as HTMLInputElement;
  const t = el.value;
  const col = props.columns[i];
  const v = value(i);
  if (readOnlyField(i)) return void (el.value = text(i));
  // an untouched field is never a change (NULL / DEFAULT stay as they are)
  if (isNull(i) && t === "") return;
  if (isDefaultValue(v) && t === "") return;
  if (!isNull(i) && !isDefaultValue(v) && t === text(i)) return;
  if (!props.editable) {
    el.value = text(i);
    return emit("edit-blocked");
  }
  const bad = validateInput(col, t);
  if (bad) {
    errors[i] = bad;
    return;
  }
  delete errors[i];
  if (isBlankInvalid(col, t)) {
    toastOnce(`"${col.name}" cannot be empty (NOT NULL)`, "warning");
    el.value = text(i);
    return;
  }
  emit("edit", i, coerceInput(col, t));
}
/** Cmd-click on a key value navigates (same as in the grid). */
function onFieldClick(e: MouseEvent, i: number) {
  if (!isMod(e) || e.shiftKey || isNull(i) || !props.navCols?.has(props.columns[i].name)) return;
  e.preventDefault();
  emit("nav", i, e.clientX, e.clientY);
}
function setNull(i: number) {
  if (!props.editable) return emit("edit-blocked");
  if (props.columns[i].nullable && !readOnlyField(i)) emit("edit", i, null);
}
</script>

<template>
  <aside class="rp">
    <div class="rp-h">
      <span>Record <span v-if="row" class="mono">#{{ rowNumber }}</span></span>
      <span class="flex-1"></span>
      <button class="btn ghost" title="Close" @click="emit('close')"><Icon name="x" /></button>
    </div>
    <div v-if="!row" class="dim rp-empty">Select a row to see it here.</div>
    <template v-else>
      <div class="rp-s"><input v-model="filter" class="input" placeholder="Filter fields" style="width: 100%" /></div>
      <div class="rp-body">
        <div v-for="{ c, i } in visible" :key="c.name" class="rp-f">
          <label>
            <Icon v-if="c.pk" name="key" :size="10" class="pk" />
            <Icon v-else-if="c.fk" name="link" :size="10" class="fk" />
            {{ c.name }}<b class="mono">{{ c.type }}</b>
          </label>
          <div class="rp-in">
            <textarea
              v-if="!isBlob(i) && (needsPopover(value(i), c) || isCut(i))" :key="row.key + c.name + (i in fulls)" class="input mono selectable rp-ta"
              :class="{ nul: isNull(i), chg: row.changed?.[i] }" :value="text(i)" :placeholder="isNull(i) ? 'NULL' : ''" spellcheck="false"
              :readonly="readOnlyField(i)" @change="commit(i, $event)" @input="delete errors[i]" @click="onFieldClick($event, i)"
            ></textarea>
            <input
              v-else :key="row.key + c.name" class="input mono selectable" :class="{ nul: isNull(i), chg: row.changed?.[i] }"
              :value="isDefaultValue(value(i)) ? '' : text(i)" :readonly="readOnlyField(i)"
              :title="isBlob(i) ? 'Binary value (not editable as text)' : undefined"
              :placeholder="isNull(i) ? 'NULL' : isDefaultValue(value(i)) ? 'DEFAULT' : ''" spellcheck="false"
              @change="commit(i, $event)" @keydown.enter="($event.target as HTMLInputElement).blur()" @input="delete errors[i]" @click="onFieldClick($event, i)"
            />
            <button
              v-if="isCut(i) && editable && loadFull" class="btn ghost rp-b" :disabled="loadingFull[i]" title="Load the full value to edit it"
              @click="loadFullValue(i)"
            >{{ loadingFull[i] ? "…" : "Edit full" }}</button>
            <button v-if="c.nullable && editable && !isNull(i) && !readOnlyField(i)" class="btn ghost rp-b" title="Set NULL" @click="setNull(i)">∅</button>
            <button
              v-if="c.fk && !isNull(i)" class="btn ghost rp-b fk" :title="`Open ${c.fk.table}.${c.fk.column}`" @click="emit('open-fk', i)"
            ><Icon name="link" :size="12" /></button>
          </div>
          <div v-if="errors[i]" class="rp-err">{{ errors[i] }}</div>
          <div v-else-if="c.pk && row.changed?.[i]" class="rp-warn">Changing the primary key</div>
        </div>
        <div v-if="incoming?.length" class="rp-ref">
          <div class="rp-rh">Referenced by</div>
          <a v-for="en in incoming" :key="en.label" class="rp-rl mono" @click="emit('open-entry', en)">{{ en.label }}</a>
        </div>
      </div>
    </template>
  </aside>
</template>

<style scoped>
.rp { width: 300px; flex: none; border-left: 1px solid var(--bd); background: var(--bg2); display: flex; flex-direction: column; min-height: 0; }
.rp-h { display: flex; align-items: center; padding: 4px 6px 4px 10px; border-bottom: 1px solid var(--bd); font-size: 11px; color: var(--dim); text-transform: uppercase; letter-spacing: .05em; height: 30px; }
.rp-s { padding: 6px 10px 2px; }
.rp-empty { padding: 14px 10px; }
.rp-body { overflow: auto; padding-bottom: 12px; flex: 1; }
.rp-f { padding: 5px 10px 0; }
.rp-f label { display: flex; align-items: center; gap: 4px; color: var(--dim); font-size: 11px; }
.rp-f label b { font: 10px var(--mono); font-weight: 400; margin-left: 2px; opacity: .8; }
.rp-f :deep(.pk) { color: var(--pk); }
.rp-f :deep(.fk) { color: var(--fk); }
.rp-in { display: flex; gap: 2px; align-items: flex-start; margin-top: 2px; }
.rp-in .input { flex: 1; width: 0; }
.rp-ta { height: 64px; padding: 4px 6px; resize: vertical; white-space: pre; }
.nul::placeholder { font-style: italic; color: var(--dim); }
.chg { background: var(--chg-upd-bg); }
.rp-err { color: var(--danger); font-size: 11px; padding-top: 1px; }
.rp-warn { color: var(--warn); font-size: 11px; padding-top: 1px; }
.rp-ref { padding: 10px 10px 0; }
.rp-rh { font-size: 10px; text-transform: uppercase; letter-spacing: .05em; color: var(--dim); margin-bottom: 2px; }
.rp-rl { display: block; padding: 1px 0; color: var(--ac); cursor: pointer; font-size: 11px; }
.rp-rl:hover { text-decoration: underline; }
.rp-b { padding: 0 5px; height: 22px; }
.rp-b.fk { color: var(--fk); }
</style>
