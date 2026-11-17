<script setup lang="ts">
// DataGrip-style raw SQL clause input (WHERE / ORDER BY): a single-line CodeMirror 6 editor with SQL
// highlighting (dialect per driver), column + keyword/operator autocomplete, Enter applies.
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Compartment, EditorState } from "@codemirror/state";
import { EditorView, keymap, placeholder as cmPlaceholder } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { syntaxHighlighting, bracketMatching } from "@codemirror/language";
import {
  acceptCompletion, autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap, type Completion, type CompletionContext,
} from "@codemirror/autocomplete";
import { MariaSQL, MySQL, PostgreSQL, SQLite, sql } from "@codemirror/lang-sql";
import { appHighlight, appTheme } from "@/components/console/cmTheme";

const props = defineProps<{
  modelValue: string; label: string; placeholder?: string; columns: string[]; error?: string; driver?: string;
}>();
const emit = defineEmits<{ "update:modelValue": [v: string]; apply: [] }>();

const host = ref<HTMLElement | null>(null);
let view: EditorView | null = null;
const langConf = new Compartment();

const WORDS = [
  "AND", "OR", "NOT", "IN", "NOT IN", "IS NULL", "IS NOT NULL", "LIKE", "NOT LIKE", "ILIKE", "BETWEEN", "EXISTS",
  "ASC", "DESC", "NULLS FIRST", "NULLS LAST", "TRUE", "FALSE", "NULL",
];

function dialect() {
  switch (props.driver) {
    case "mysql": return MySQL;
    case "sqlite": return SQLite;
    case "mariadb": return MariaSQL;
    default: return PostgreSQL;
  }
}

function source(ctx: CompletionContext) {
  const w = ctx.matchBefore(/[A-Za-z_][A-Za-z_0-9]*/);
  if (!w || (w.from === w.to && !ctx.explicit)) return null;
  const options: Completion[] = [
    ...props.columns.map((c): Completion => ({ label: c, type: "property", boost: 2 })),
    ...WORDS.map((k): Completion => ({ label: k, type: "keyword" })),
  ];
  return { from: w.from, options, validFor: /^[A-Za-z_0-9]*$/ };
}

/** pasted / typed newlines become spaces: the clause stays on one line */
const singleLine = EditorState.transactionFilter.of((tr) => {
  if (!tr.docChanged) return tr;
  let multi = false;
  tr.changes.iterChanges((_f, _t, _a, _b, ins) => {
    if (ins.lines > 1) multi = true;
  });
  if (!multi) return tr;
  const specs: { from: number; to: number; insert: string }[] = [];
  tr.changes.iterChanges((f, t, _a, _b, ins) => specs.push({ from: f, to: t, insert: ins.toString().replace(/\r?\n/g, " ") }));
  return { changes: specs };
});

const compact = EditorView.theme({
  "&": { fontSize: "12px", background: "transparent" },
  ".cm-scroller": { overflowY: "hidden", lineHeight: "20px", fontFamily: "var(--mono)" },
  ".cm-content": { padding: "0 6px", minHeight: "20px" },
  ".cm-line": { padding: "0" },
  ".cm-placeholder": { color: "var(--dim)", opacity: ".55" },
  "&.cm-focused": { outline: "none" },
});

onMounted(() => {
  if (!host.value) return;
  view = new EditorView({
    parent: host.value,
    state: EditorState.create({
      doc: props.modelValue,
      extensions: [
        singleLine,
        keymap.of([
          { key: "Tab", run: acceptCompletion },
          { key: "Enter", run: () => (emit("apply"), true) },
          { key: "Mod-Enter", run: () => (emit("apply"), true) },
          ...closeBracketsKeymap, ...completionKeymap, ...historyKeymap, ...defaultKeymap.filter((b) => b.key !== "Enter" && b.key !== "Mod-Enter"),
        ]),
        history(),
        bracketMatching(),
        closeBrackets(),
        autocompletion({ override: [source], icons: false, activateOnTyping: true }),
        syntaxHighlighting(appHighlight),
        appTheme,
        compact,
        langConf.of(sql({ dialect: dialect() })),
        props.placeholder ? cmPlaceholder(props.placeholder) : [],
        EditorView.updateListener.of((u) => {
          if (u.docChanged) emit("update:modelValue", u.state.doc.toString());
        }),
      ],
    }),
  });
});
onBeforeUnmount(() => {
  view?.destroy();
  view = null;
});

watch(() => props.modelValue, (v) => {
  if (view && v !== view.state.doc.toString()) view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v } });
});
watch(() => props.driver, () => view?.dispatch({ effects: langConf.reconfigure(sql({ dialect: dialect() })) }));

defineExpose({ focus: () => view?.focus() });
</script>

<template>
  <div class="ci" :class="{ err: !!error }" :title="error">
    <span class="dim mono ci-l">{{ label }}</span>
    <div ref="host" class="ci-ed selectable"></div>
  </div>
</template>

<style scoped>
.ci { flex: 1 1 220px; display: flex; align-items: center; gap: 6px; min-width: 160px; }
.ci-l { font-size: 11px; }
.ci-ed {
  flex: 1; min-width: 0; height: 22px; overflow: hidden; background: var(--bg); border: 1px solid var(--bd); border-radius: 4px;
}
.ci-ed:focus-within { border-color: var(--ac); }
.ci.err .ci-ed { border-color: var(--danger); box-shadow: 0 1px 0 var(--danger); }
.ci-ed :deep(.cm-editor) { height: 100%; }
/* single-line field: never scroll vertically, hide the horizontal scrollbar (it still scrolls with the caret) */
.ci-ed :deep(.cm-scroller) {
  height: 100%; align-items: center; line-height: 20px !important;
  overflow-y: hidden !important; overflow-x: auto; scrollbar-width: none;
}
.ci-ed :deep(.cm-scroller::-webkit-scrollbar) { display: none; }
.ci-ed :deep(.cm-content) { padding: 0 6px !important; min-height: 0 !important; }
.ci-ed :deep(.cm-line) { padding: 0 !important; line-height: 20px; }
</style>
