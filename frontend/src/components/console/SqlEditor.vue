<script setup lang="ts">
// CodeMirror 6 SQL editor: dialect per driver, schema-aware autocomplete (tables, columns after
// `table.` / alias.), theme driven by the app's CSS variables (dark + light).
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Compartment, EditorState, Prec } from "@codemirror/state";
import { EditorView, drawSelection, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers, placeholder } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { HighlightStyle, bracketMatching, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import { acceptCompletion, autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap, type Completion, type CompletionContext } from "@codemirror/autocomplete";
import { MariaSQL, MySQL, PostgreSQL, SQLite, sql } from "@codemirror/lang-sql";
import { highlightSelectionMatches, searchKeymap } from "@codemirror/search";
import { appHighlight, appTheme } from "./cmTheme";

export interface SchemaTable { name: string; columns: string[]; kind?: string }

const props = defineProps<{
  modelValue: string;
  driver: string;
  tables: SchemaTable[];
  readOnly?: boolean;
  lineNumbers?: boolean;
  placeholderText?: string;
}>();
const emit = defineEmits<{
  "update:modelValue": [v: string];
  run: [];
  "run-all": [];
  "save-query": [];
}>();

const host = ref<HTMLElement | null>(null);
let view: EditorView | null = null;
const langConf = new Compartment();

function dialect() {
  switch (props.driver) {
    case "postgres": return PostgreSQL;
    case "mysql": return MySQL;
    case "sqlite": return SQLite;
    default: return props.driver === "mariadb" ? MariaSQL : PostgreSQL;
  }
}

const clean = (s: string) => s.replace(/^[`"]|[`"]$/g, "");
const STOP = new Set("where on join left right inner outer cross full natural group order limit set using union having window returning values select from offset fetch".split(" "));

/** alias / table name -> table, from FROM / JOIN / UPDATE / INTO clauses of the whole document. */
function aliasMap(doc: string): Map<string, string> {
  const m = new Map<string, string>();
  const re = /\b(?:from|join|update|into)\s+((?:[`"\w$]+\.)?[`"\w$]+)(?:\s+(?:as\s+)?([`"\w$]+))?/gi;
  for (let x = re.exec(doc); x; x = re.exec(doc)) {
    const full = clean(x[1].split(".").pop()!);
    m.set(full.toLowerCase(), full);
    const a = x[2] ? clean(x[2]) : "";
    if (a && !STOP.has(a.toLowerCase())) m.set(a.toLowerCase(), full);
  }
  return m;
}

function schemaSource(ctx: CompletionContext) {
  const word = ctx.matchBefore(/[`"\w$.]*/);
  if (!word || (word.from === word.to && !ctx.explicit)) return null;
  const text = word.text;
  const doc = ctx.state.doc.toString();
  const tables = props.tables;
  const byName = (n: string) => tables.find((x) => x.name.toLowerCase() === n.toLowerCase());
  const dot = text.lastIndexOf(".");
  if (dot >= 0) {
    const qual = clean(text.slice(0, dot).split(".").pop()!);
    const aliases = aliasMap(doc);
    const tn = aliases.get(qual.toLowerCase()) ?? qual;
    const tb = byName(tn);
    if (tb) {
      return {
        from: word.from + dot + 1,
        options: tb.columns.map((c): Completion => ({ label: c, type: "property", detail: tb.name })),
        validFor: /^[\w$]*$/,
      };
    }
    // schema-qualified table: schema.<table>
    return {
      from: word.from + dot + 1,
      options: tables.map((x): Completion => ({ label: x.name, type: "class", detail: x.kind === "view" ? "view" : "table" })),
      validFor: /^[\w$]*$/,
    };
  }
  const opts: Completion[] = tables.map((x) => ({ label: x.name, type: "class", detail: x.kind === "view" ? "view" : "table", boost: 1 }));
  const used = new Set<string>();
  for (const tn of aliasMap(doc).values()) used.add(tn);
  for (const tn of used) {
    const tb = byName(tn);
    if (tb) for (const c of tb.columns) opts.push({ label: c, type: "property", detail: tb.name, boost: 2 });
  }
  return { from: word.from, options: opts, validFor: /^[`"\w$]*$/ };
}

function langExtensions() {
  const d = dialect();
  const lang = sql({ dialect: d, upperCaseKeywords: true });
  return [lang, lang.language.data.of({ autocomplete: schemaSource })];
}

onMounted(() => {
  if (!host.value) return;
  view = new EditorView({
    parent: host.value,
    state: EditorState.create({
      doc: props.modelValue,
      extensions: [
        Prec.highest(
          keymap.of([
            // Tab accepts an open completion; otherwise falls through to indentWithTab
            { key: "Tab", run: acceptCompletion },
            { key: "Mod-Enter", run: () => (emit("run"), true) },
            { key: "Mod-Shift-Enter", run: () => (emit("run-all"), true) },
            { key: "Mod-s", run: () => (emit("save-query"), true) },
          ]),
        ),
        props.lineNumbers === false ? [] : [lineNumbers(), highlightActiveLineGutter()],
        history(),
        drawSelection(),
        indentOnInput(),
        bracketMatching(),
        closeBrackets(),
        highlightActiveLine(),
        highlightSelectionMatches(),
        autocompletion({ activateOnTyping: true, icons: false }),
        keymap.of([...closeBracketsKeymap, ...completionKeymap, ...searchKeymap, ...historyKeymap, indentWithTab, ...defaultKeymap]),
        syntaxHighlighting(appHighlight),
        appTheme,
        langConf.of(langExtensions()),
        EditorState.readOnly.of(!!props.readOnly),
        EditorView.editable.of(!props.readOnly),
        props.placeholderText ? placeholder(props.placeholderText) : [],
        EditorView.updateListener.of((u) => {
          if (u.docChanged) emit("update:modelValue", u.state.doc.toString());
        }),
      ],
    }),
  });
});
onBeforeUnmount(() => view?.destroy());

watch(() => props.modelValue, (v) => {
  if (view && v !== view.state.doc.toString()) view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v } });
});
watch(() => props.driver, () => view?.dispatch({ effects: langConf.reconfigure(langExtensions()) }));

defineExpose({
  focus: () => view?.focus(),
  /** Current selection (text may be empty) and cursor offset. */
  getState: () => {
    const s = view!.state;
    const r = s.selection.main;
    return { doc: s.doc.toString(), from: r.from, to: r.to, selected: s.sliceDoc(r.from, r.to), cursor: r.head };
  },
  setDoc: (text: string) => view?.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } }),
  insertAtCursor: (text: string) => {
    if (!view) return;
    const r = view.state.selection.main;
    view.dispatch({ changes: { from: r.from, to: r.to, insert: text }, selection: { anchor: r.from + text.length } });
    view.focus();
  },
  goToOffset: (pos: number) => {
    if (!view) return;
    pos = Math.max(0, Math.min(pos, view.state.doc.length));
    view.dispatch({ selection: { anchor: pos }, effects: EditorView.scrollIntoView(pos, { y: "center" }) });
    view.focus();
  },
});
</script>

<template>
  <div ref="host" class="sqled selectable"></div>
</template>

<style scoped>
.sqled { height: 100%; min-height: 0; overflow: hidden; }
.sqled :deep(.cm-editor) { height: 100%; }
</style>
