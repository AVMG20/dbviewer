<script setup lang="ts">
// Small CodeMirror 6 editor for the value popover: line numbers, no wrapping (horizontal scroll),
// optional JSON / XML highlighting. The theme comes from the app's CSS variables (dark + light).
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Compartment, EditorState } from "@codemirror/state";
import { EditorView, drawSelection, keymap, lineNumbers } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { syntaxHighlighting, bracketMatching } from "@codemirror/language";
import { json } from "@codemirror/lang-json";
import { xml } from "@codemirror/lang-xml";
import { appHighlight, appTheme } from "@/components/console/cmTheme";

export type ValueLang = "text" | "json" | "xml";

const props = defineProps<{ modelValue: string; lang: ValueLang; readOnly?: boolean }>();
const emit = defineEmits<{ "update:modelValue": [v: string] }>();

const host = ref<HTMLElement | null>(null);
let view: EditorView | null = null;
const langConf = new Compartment();
const roConf = new Compartment();

const ext = (l: ValueLang) => (l === "json" ? json() : l === "xml" ? xml() : []);
const ro = (r?: boolean) => [EditorState.readOnly.of(!!r), EditorView.editable.of(!r)];

onMounted(() => {
  if (!host.value) return;
  view = new EditorView({
    parent: host.value,
    state: EditorState.create({
      doc: props.modelValue,
      extensions: [
        lineNumbers(),
        history(),
        drawSelection(),
        bracketMatching(),
        keymap.of([...historyKeymap, indentWithTab, ...defaultKeymap]),
        syntaxHighlighting(appHighlight),
        appTheme,
        langConf.of(ext(props.lang)),
        roConf.of(ro(props.readOnly)),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) emit("update:modelValue", u.state.doc.toString());
        }),
      ],
    }),
  });
});
// the popover is torn down with its view: nothing may leak
onBeforeUnmount(() => {
  view?.destroy();
  view = null;
});

watch(() => props.modelValue, (v) => {
  if (view && v !== view.state.doc.toString()) view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v } });
});
watch(() => props.lang, (l) => view?.dispatch({ effects: langConf.reconfigure(ext(l)) }));
watch(() => props.readOnly, (r) => view?.dispatch({ effects: roConf.reconfigure(ro(r)) }));

defineExpose({ focus: () => view?.focus() });
</script>

<template>
  <div ref="host" class="valed selectable"></div>
</template>

<style scoped>
.valed { flex: 1; min-height: 0; overflow: hidden; background: var(--bg); }
.valed :deep(.cm-editor) { height: 100%; }
</style>
