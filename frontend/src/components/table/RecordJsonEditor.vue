<script setup lang="ts">
// Compact CodeMirror JSON editor for one record-panel field: grows with its content (up to a
// max height), wraps long lines, commits on blur / ⌘↵. The parent owns validation and staging.
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Compartment, EditorState, Prec } from "@codemirror/state";
import { EditorView, drawSelection, keymap, placeholder as cmPlaceholder } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { bracketMatching, syntaxHighlighting } from "@codemirror/language";
import { json } from "@codemirror/lang-json";
import { appHighlight, appTheme } from "@/components/console/cmTheme";

const props = defineProps<{ modelValue: string; readOnly?: boolean; placeholder?: string; invalid?: boolean; changed?: boolean }>();
const emit = defineEmits<{
  "update:modelValue": [v: string];
  commit: [];
  /** a key was typed while read-only */
  blocked: [];
}>();

const host = ref<HTMLElement | null>(null);
let view: EditorView | null = null;
const roConf = new Compartment();
const phConf = new Compartment();
const ro = (r?: boolean) => [EditorState.readOnly.of(!!r), EditorView.editable.of(true)];

const fieldTheme = EditorView.theme({
  "&": { height: "auto", fontSize: "11.5px", background: "transparent" },
  ".cm-scroller": { lineHeight: "1.45", maxHeight: "260px" },
  ".cm-content": { padding: "4px 0" },
  ".cm-line": { padding: "0 6px" },
});

onMounted(() => {
  if (!host.value) return;
  view = new EditorView({
    parent: host.value,
    state: EditorState.create({
      doc: props.modelValue,
      extensions: [
        history(),
        drawSelection(),
        bracketMatching(),
        EditorView.lineWrapping,
        Prec.high(keymap.of([{ key: "Mod-Enter", run: (v) => (v.contentDOM.blur(), true) }])),
        keymap.of([...historyKeymap, indentWithTab, ...defaultKeymap]),
        syntaxHighlighting(appHighlight),
        appTheme,
        fieldTheme,
        json(),
        roConf.of(ro(props.readOnly)),
        phConf.of(props.placeholder ? cmPlaceholder(props.placeholder) : []),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) emit("update:modelValue", u.state.doc.toString());
          if (u.focusChanged && !u.view.hasFocus) emit("commit");
        }),
        EditorView.domEventHandlers({
          keydown: (e) => {
            if (props.readOnly && e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) emit("blocked");
            return false;
          },
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
watch(() => props.readOnly, (r) => view?.dispatch({ effects: roConf.reconfigure(ro(r)) }));
watch(() => props.placeholder, (p) => view?.dispatch({ effects: phConf.reconfigure(p ? cmPlaceholder(p) : []) }));
</script>

<template>
  <div ref="host" class="rje selectable" :class="{ bad: invalid, chg: changed, ro: readOnly }"></div>
</template>

<style scoped>
.rje { flex: 1; width: 0; min-width: 0; border: 1px solid var(--bd); border-radius: var(--r); background: var(--bg); overflow: hidden; }
.rje:focus-within { border-color: var(--ac); box-shadow: 0 0 0 3px var(--ring); }
.rje.chg { background: var(--chg-upd-bg); }
.rje.bad, .rje.bad:focus-within { border-color: var(--danger); }
.rje :deep(.cm-editor) { background: transparent; }
.rje :deep(.cm-placeholder) { font-style: italic; }
.rje:not(:focus-within) :deep(.cm-matchingBracket) { background: none; outline: none; }
</style>
