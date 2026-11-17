<script setup lang="ts">
// Record (form) view of the active row, in a side panel. FK values are clickable.
// Truncated long text is read-only until its full value is loaded; blobs are read-only.
// Each field gets an editor for its kind (bool switch, date pickers, JSON editor, numbers, auto-growing
// text); every edit is staged through the same text -> value path the grid uses (validateInput/coerceInput).
import { computed, reactive, ref, watch, type Directive } from "vue";
import { isBinary, isLongText, type CellValue, type ResultColumn } from "@/api";
import {
  cellText, coerceInput, isBlankInvalid, isDefaultValue, isNumericKind, minifyJson, needsPopover, prettyJson, renderCell, validateInput,
} from "@/lib/format";
import { toastOnce } from "@/lib/notify";
import type { GridRow } from "@/lib/gridTypes";
import Icon from "@/components/common/Icon.vue";
import { isMod } from "@/lib/keys";
import { kbd } from "@/lib/kbd";
import type { NavEntry } from "@/lib/fkNav";
import RecordJsonEditor from "./RecordJsonEditor.vue";

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
/** uncommitted text per field (kept so re-renders, e.g. a new validation message, don't reset typing) */
const drafts = reactive<Record<number, string>>({});
/** field whose native date/time input is shown inline (when the webview has no showPicker) */
const pickInline = ref<number | null>(null);
watch(() => props.row?.key, () => {
  for (const k of Object.keys(errors)) delete errors[Number(k)];
  for (const k of Object.keys(fulls)) delete fulls[Number(k)];
  for (const k of Object.keys(drafts)) delete drafts[Number(k)];
  pickInline.value = null;
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

// ---- which editor a field gets ------------------------------------------------------------------
type FieldEditor = "bool" | "json" | "long" | "date" | "time" | "datetime" | "num" | "text";
function editorOf(c: ResultColumn, i: number): FieldEditor {
  const v = value(i);
  if (isBlob(i)) return "text";
  if (c.kind === "bool" && (typeof v === "boolean" || v === null || v === undefined || isDefaultValue(v))) return "bool";
  if (c.kind === "json") return "json";
  if (c.kind === "text" || needsPopover(v, c) || isCut(i)) return "long";
  if (c.kind === "date" || c.kind === "time" || c.kind === "datetime") return c.kind;
  if (isNumericKind(c.kind)) return "num";
  return "text";
}
const editors = computed(() => props.columns.map((c, i) => editorOf(c, i)));

async function loadFullValue(i: number) {
  if (!props.loadFull) return;
  loadingFull[i] = true;
  const v = await props.loadFull(i);
  loadingFull[i] = false;
  if (v === null) toastOnce("Could not load the full value", "warning");
  else fulls[i] = v;
}

/**
 * Validates and stages typed text for field i exactly like the grid's inline editor does.
 * "revert": the field should show its stored value again; "error": keep the text, errors[i] says why.
 */
function stageText(i: number, t: string): "ok" | "revert" | "error" {
  const col = props.columns[i];
  const v = value(i);
  if (readOnlyField(i)) return "revert";
  // an untouched field is never a change (NULL / DEFAULT stay as they are)
  if (isNull(i) && t === "") return "ok";
  if (isDefaultValue(v) && t === "") return "ok";
  if (!isNull(i) && !isDefaultValue(v) && t === text(i)) return "ok";
  if (!props.editable) {
    emit("edit-blocked");
    return "revert";
  }
  const bad = validateInput(col, t);
  if (bad) {
    errors[i] = bad;
    return "error";
  }
  delete errors[i];
  if (isBlankInvalid(col, t)) {
    toastOnce(`"${col.name}" cannot be empty (NOT NULL)`, "warning");
    return "revert";
  }
  emit("edit", i, coerceInput(col, t));
  return "ok";
}
function commit(i: number, e: Event) {
  const el = e.target as HTMLInputElement | HTMLTextAreaElement;
  const r = stageText(i, el.value);
  if (r !== "error") delete drafts[i];
  if (r === "revert") el.value = text(i);
  if (el instanceof HTMLTextAreaElement) grow(el);
}
/** Live feedback while typing for kinds where a message helps right away (numbers). */
function onInput(i: number, e: Event) {
  const el = e.target as HTMLInputElement | HTMLTextAreaElement;
  drafts[i] = el.value;
  if (el instanceof HTMLTextAreaElement) grow(el);
  const col = props.columns[i];
  const bad = isNumericKind(col.kind) && !readOnlyField(i) ? validateInput(col, el.value) : null;
  if (bad) errors[i] = bad;
  else delete errors[i];
}
/** Cmd-click on a key value navigates (same as in the grid). */
function onFieldClick(e: MouseEvent, i: number) {
  if (!isMod(e) || e.shiftKey || isNull(i) || !props.navCols?.has(props.columns[i].name)) return;
  e.preventDefault();
  emit("nav", i, e.clientX, e.clientY);
}
function setNull(i: number) {
  if (!props.editable) return emit("edit-blocked");
  if (props.columns[i].nullable && !readOnlyField(i)) {
    delete drafts[i];
    delete errors[i];
    emit("edit", i, null);
  }
}

// ---- long text: auto-growing textarea ------------------------------------------------------------
const MAX_TA = 320;
function grow(el: HTMLTextAreaElement) {
  el.style.height = "auto";
  el.style.height = `${Math.min(el.scrollHeight + 2, MAX_TA)}px`;
}
const vGrow: Directive<HTMLTextAreaElement> = { mounted: grow, updated: grow };

// ---- boolean ---------------------------------------------------------------------------------------
function boolLabel(i: number): string {
  const v = value(i);
  if (isDefaultValue(v)) return "DEFAULT";
  if (v === null || v === undefined) return "NULL";
  return v ? "true" : "false";
}
function toggleBool(i: number) {
  if (readOnlyField(i)) return;
  if (!props.editable) return emit("edit-blocked");
  // same value the grid stages for "true" / "false" (coerceInput)
  emit("edit", i, coerceInput(props.columns[i], value(i) === true ? "false" : "true"));
}

// ---- JSON ------------------------------------------------------------------------------------------
/** What the JSON editor shows: pretty-printed when valid (like the grid's value popover). */
function jsonText(i: number): string {
  if (isNull(i) || isDefaultValue(value(i))) return "";
  const t = text(i);
  return isCut(i) ? t : (prettyJson(t) ?? t);
}
function onJsonInput(i: number, t: string) {
  drafts[i] = t;
  const bad = t.trim() === "" ? null : validateInput(props.columns[i], t);
  if (bad) errors[i] = bad;
  else delete errors[i];
}
function commitJson(i: number) {
  const t = drafts[i];
  if (t === undefined) return;
  const orig = text(i);
  const same = t === jsonText(i) || (!isNull(i) && minifyJson(t) !== null && minifyJson(t) === minifyJson(orig));
  if (same) {
    delete drafts[i];
    delete errors[i];
    return;
  }
  if (stageText(i, t) !== "error") delete drafts[i];
}
function formatJson(i: number) {
  const p = prettyJson(drafts[i] ?? jsonText(i));
  if (p === null) return void (errors[i] = "Not valid JSON");
  drafts[i] = p;
  delete errors[i];
  commitJson(i);
}
function onJsonBlocked(i: number) {
  if (!props.editable && !readOnlyField(i)) emit("edit-blocked");
}

// ---- date / time / datetime: native picker that writes the driver's text format ---------------------
// Values come back as text ("2024-01-02 13:45:00", "2024-01-02T13:45:00Z" for SQLite as stored,
// "...+02:00" for timestamptz). The picker only replaces the date / time digits: separator, fraction
// and zone suffix of the current value are kept so the staged text round-trips like a typed one.
const DT_RE = /^(\d{4}-\d\d-\d\d)(?:([ T])(\d\d:\d\d(?::\d\d)?)(\.\d+)?)?(.*)$/;
const TM_RE = /^(\d\d:\d\d(?::\d\d)?)(\.\d+)?(.*)$/;
const hms = (t: string) => (t.length === 5 ? `${t}:00` : t.slice(0, 8));
const pickerType = (k: FieldEditor) => (k === "datetime" ? "datetime-local" : k === "date" ? "date" : "time");

function pickerValue(i: number, k: FieldEditor): string {
  const t = text(i).trim();
  if (k === "time") {
    const m = TM_RE.exec(t);
    return m ? hms(m[1]) : "";
  }
  const m = DT_RE.exec(t);
  if (!m) return "";
  return k === "date" ? m[1] : `${m[1]}T${m[3] ? hms(m[3]) : "00:00:00"}`;
}
function fromPicker(i: number, k: FieldEditor, picked: string): string {
  const cur = text(i).trim();
  if (k === "date") return picked;
  if (k === "time") {
    const m = TM_RE.exec(cur);
    const tm = hms(picked);
    return tm + (m && hms(m[1]) === tm ? (m[2] ?? "") : "") + (m?.[3] ?? "");
  }
  const [d, t = "00:00"] = picked.split("T");
  const tm = hms(t);
  const m = DT_RE.exec(cur);
  const sep = m?.[2] ?? " ";
  const frac = m?.[3] && hms(m[3]) === tm ? (m[4] ?? "") : "";
  const zone = m?.[2] ? (m[5] ?? "") : "";
  return `${d}${sep}${tm}${frac}${zone}`;
}
function onPick(i: number, k: FieldEditor, e: Event) {
  const picked = (e.target as HTMLInputElement).value;
  if (!picked) return;
  if (stageText(i, fromPicker(i, k, picked)) !== "error") delete drafts[i];
}
function openPicker(i: number, e: MouseEvent) {
  if (readOnlyField(i)) return;
  if (!props.editable) return emit("edit-blocked");
  const el = (e.currentTarget as HTMLElement).parentElement?.querySelector<HTMLInputElement>("input.rp-pick");
  if (!el) return;
  try {
    if (typeof el.showPicker !== "function") throw new Error("no showPicker");
    el.showPicker();
  } catch {
    pickInline.value = pickInline.value === i ? null : i; // fall back to the visible native control
  }
}
const isDateKind = (k: FieldEditor) => k === "date" || k === "time" || k === "datetime";
const pickTitle = (k: FieldEditor) => (k === "date" ? "Pick a date" : k === "time" ? "Pick a time" : "Pick a date and time");
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
            <!-- boolean: switch (NULL = neither; the ∅ button sets it) -->
            <div v-if="editors[i] === 'bool'" class="rp-bool" :class="{ chg: row.changed?.[i], ro: readOnlyField(i) || !editable }">
              <span
                class="tg" :class="{ on: value(i) === true, nul: isNull(i) || isDefaultValue(value(i)) }" role="switch"
                :aria-checked="value(i) === true" :aria-label="c.name" :tabindex="readOnlyField(i) ? -1 : 0"
                @click="toggleBool(i)" @keydown.space.prevent="toggleBool(i)" @keydown.enter.prevent="toggleBool(i)"
              />
              <span class="mono" :class="{ null: value(i) === null || value(i) === undefined, dim: isDefaultValue(value(i)) }">{{ boolLabel(i) }}</span>
            </div>
            <!-- JSON: CodeMirror, validated as you type, staged on blur -->
            <RecordJsonEditor
              v-else-if="editors[i] === 'json'" :key="row.key + c.name + (i in fulls)"
              :model-value="drafts[i] ?? jsonText(i)" :read-only="readOnlyField(i) || !editable"
              :placeholder="isNull(i) ? 'NULL' : isDefaultValue(value(i)) ? 'DEFAULT' : ''" :invalid="!!errors[i]" :changed="!!row.changed?.[i]"
              @update:model-value="onJsonInput(i, $event)" @commit="commitJson(i)" @blocked="onJsonBlocked(i)"
            />
            <!-- long text: auto-growing textarea -->
            <textarea
              v-else-if="editors[i] === 'long'" :key="row.key + c.name + (i in fulls)" v-grow rows="1" class="input mono selectable rp-ta"
              :class="{ nul: isNull(i), chg: row.changed?.[i], bad: errors[i] }" :value="drafts[i] ?? text(i)" :placeholder="isNull(i) ? 'NULL' : ''" spellcheck="false"
              :readonly="readOnlyField(i)" :title="readOnlyField(i) ? undefined : kbd('⌘↵ to apply')"
              @change="commit(i, $event)" @input="onInput(i, $event)" @click="onFieldClick($event, i)"
              @keydown.enter="isMod($event) && ($event.target as HTMLElement).blur()"
            ></textarea>
            <template v-else>
              <input
                :key="row.key + c.name" class="input mono selectable"
                :class="{ nul: isNull(i), chg: row.changed?.[i], num: editors[i] === 'num', bad: errors[i] }"
                :value="drafts[i] ?? (isDefaultValue(value(i)) ? '' : text(i))" :readonly="readOnlyField(i)"
                :inputmode="editors[i] === 'num' ? (c.kind === 'int' ? 'numeric' : 'decimal') : undefined"
                :title="isBlob(i) ? 'Binary value (not editable as text)' : undefined"
                :placeholder="isNull(i) ? 'NULL' : isDefaultValue(value(i)) ? 'DEFAULT' : ''" spellcheck="false"
                @change="commit(i, $event)" @keydown.enter="($event.target as HTMLInputElement).blur()" @input="onInput(i, $event)" @click="onFieldClick($event, i)"
              />
              <span v-if="isDateKind(editors[i]) && !readOnlyField(i)" class="rp-pk">
                <input
                  class="rp-pick" :type="pickerType(editors[i])" step="1" tabindex="-1" aria-hidden="true"
                  :value="pickerValue(i, editors[i])" @change="onPick(i, editors[i], $event)"
                />
                <button class="btn ghost rp-b" :title="pickTitle(editors[i])" :aria-label="pickTitle(editors[i])" @click="openPicker(i, $event)">
                  <svg v-if="editors[i] === 'time'" width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">
                    <circle cx="8" cy="8" r="6" /><path d="M8 4.5V8l2.5 1.5" />
                  </svg>
                  <svg v-else width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">
                    <rect x="2" y="3" width="12" height="11" rx="1.5" /><path d="M2 6.5h12M5.5 1.5v3M10.5 1.5v3" />
                  </svg>
                </button>
              </span>
            </template>
            <button
              v-if="isCut(i) && editable && loadFull" class="btn ghost rp-b" :disabled="loadingFull[i]" title="Load the full value to edit it"
              @click="loadFullValue(i)"
            >{{ loadingFull[i] ? "…" : "Edit full" }}</button>
            <button v-if="c.nullable && editable && !isNull(i) && !readOnlyField(i)" class="btn ghost rp-b" title="Set NULL" @click="setNull(i)">∅</button>
            <button
              v-if="c.fk && !isNull(i)" class="btn ghost rp-b fk" :title="`Open ${c.fk.table}.${c.fk.column}`" @click="emit('open-fk', i)"
            ><Icon name="link" :size="12" /></button>
          </div>
          <div v-if="pickInline === i && isDateKind(editors[i])" class="rp-inl">
            <input
              class="input mono" :type="pickerType(editors[i])" step="1" :value="pickerValue(i, editors[i])"
              @change="onPick(i, editors[i], $event)"
            />
            <button class="btn ghost rp-b" title="Close" @click="pickInline = null"><Icon name="x" :size="11" /></button>
          </div>
          <div v-if="errors[i]" class="rp-err">{{ errors[i] }}</div>
          <div v-else-if="c.pk && row.changed?.[i]" class="rp-warn">Changing the primary key</div>
          <div v-if="editors[i] === 'json' && editable && !readOnlyField(i)" class="rp-tools">
            <a class="rp-act" title="Pretty-print the JSON" @click="formatJson(i)">Format</a>
            <span class="dim">{{ kbd("⌘↵ applies") }}</span>
          </div>
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
.rp-ta { height: auto; min-height: var(--ctl); max-height: 320px; padding: 5px 6px; resize: vertical; white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.45; }
.num { text-align: right; font-variant-numeric: tabular-nums; }
.input.bad, .input.bad:focus { border-color: var(--danger); }
.input.bad:focus { box-shadow: 0 0 0 3px color-mix(in srgb, var(--danger) 25%, transparent); }
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
/* boolean switch */
.rp-bool { flex: 1; display: flex; align-items: center; gap: 8px; height: var(--ctl); padding: 0 8px; border-radius: var(--r); }
.rp-bool.chg { background: var(--chg-upd-bg); }
.rp-bool > .mono { font-size: 12px; }
.tg { width: 30px; height: 18px; border-radius: 99px; background: var(--bd2); position: relative; display: inline-block; flex: none; transition: background .12s; }
.tg::after { content: ""; position: absolute; top: 2px; left: 2px; width: 14px; height: 14px; border-radius: 50%; background: #fff; transition: left .12s; box-shadow: 0 1px 2px #0004; }
.tg.on { background: var(--ok); }
.tg.on::after { left: 14px; }
.tg.nul { background: none; box-shadow: inset 0 0 0 1px var(--bd2); }
.tg.nul::after { background: var(--bd2); box-shadow: none; }
.tg:focus-visible { outline: none; box-shadow: 0 0 0 3px var(--ring); }
.rp-bool.ro .tg { opacity: .6; }
/* date / time picker: the native input stays invisible under the button; showPicker() opens it */
.rp-pk { position: relative; display: inline-flex; flex: none; }
.rp-pick { position: absolute; left: 0; bottom: 0; width: 1px; height: 1px; opacity: 0; pointer-events: none; border: 0; padding: 0; }
.rp-inl { display: flex; gap: 2px; margin-top: 3px; }
.rp-inl .input { flex: 1; width: 0; color-scheme: inherit; }
.rp-tools { display: flex; align-items: center; gap: 10px; padding-top: 2px; font-size: 11px; }
.rp-act { color: var(--ac); cursor: pointer; }
.rp-act:hover { text-decoration: underline; }
</style>
