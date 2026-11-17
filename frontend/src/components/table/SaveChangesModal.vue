<script setup lang="ts">
// SQL preview + confirm for staged changes (data + structure), built on the shared <Modal>.
// The parent runs the apply and feeds back `error` / `failedIndex`; staged changes stay on failure.
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { kbd } from "@/lib/kbd";
import Modal from "@/components/common/Modal.vue";
import { highlightSql } from "@/lib/sqlHighlight";
import { copyText } from "@/lib/clipboard";
import { isMod } from "@/lib/keys";

const props = defineProps<{
  title?: string;
  statements: string[];
  loading?: boolean;
  applying?: boolean;
  error?: string;
  failedIndex?: number;
  danger?: boolean;
  note?: string;
  confirmText?: string;
}>();
const emit = defineEmits<{ confirm: []; cancel: [] }>();

const LIMIT = 50;
const showAll = ref(false);
/** Long lists (500 UPDATEs) are cut; the failed statement is always visible. */
const shown = computed(() => {
  const n = props.statements.length;
  if (showAll.value || n <= LIMIT) return props.statements.map((s, i) => ({ i, html: highlightSql(s) }));
  const keep = new Set<number>(Array.from({ length: LIMIT }, (_, i) => i));
  if (props.failedIndex !== undefined && props.failedIndex >= 0) keep.add(props.failedIndex);
  return [...keep].sort((a, b) => a - b).map((i) => ({ i, html: highlightSql(props.statements[i]) }));
});
function onKey(e: KeyboardEvent) {
  if (e.key === "Enter" && isMod(e) && props.statements.length && !props.applying && !props.loading) {
    e.preventDefault();
    e.stopPropagation();
    emit("confirm");
  }
}
onMounted(() => window.addEventListener("keydown", onKey, true));
onBeforeUnmount(() => window.removeEventListener("keydown", onKey, true));
</script>

<template>
  <Modal :title="title ?? 'Review changes'" :width="720" :close-on-overlay="!applying" @close="!applying && emit('cancel')">
    <div v-if="statements.length" class="dim mono" style="font-size: 11px">{{ statements.length }} statement{{ statements.length > 1 ? "s" : "" }}, run in one transaction</div>
    <div v-if="note" class="dim">{{ note }}</div>
    <div v-if="loading" class="dim">Generating SQL…</div>
    <div v-else-if="!statements.length && !error" class="dim">No SQL to run.</div>
    <div v-for="h in shown" :key="h.i" class="stmt mono selectable" :class="{ failed: failedIndex === h.i }">
      <span class="dim idx">{{ h.i + 1 }}</span><pre v-html="h.html"></pre>
    </div>
    <button v-if="!showAll && statements.length > LIMIT" class="btn ghost" @click="showAll = true">Show all {{ statements.length }} statements ({{ statements.length - shown.length }} hidden)</button>
    <div v-if="error" class="err mono selectable">
      <b>{{ failedIndex !== undefined && failedIndex >= 0 ? `Statement ${failedIndex + 1} failed. Nothing was saved.` : "Save failed. Nothing was saved." }}</b>
      <div>{{ error }}</div>
      <div class="dim">The staged changes are kept. Fix them and try again.</div>
    </div>
    <template #footer>
      <button class="btn ghost" :disabled="!statements.length" @click="copyText(statements.join(';\n') + ';')">Copy SQL</button>
      <span class="flex-1"></span>
      <button class="btn" :disabled="applying" @click="emit('cancel')">Cancel</button>
      <button class="btn" :class="danger ? 'danger' : 'primary'" :disabled="applying || loading || !statements.length" @click="emit('confirm')">
        {{ applying ? "Saving…" : (confirmText ?? "Save") }} <kbd class="kk">{{ kbd("⌘↵") }}</kbd>
      </button>
    </template>
  </Modal>
</template>

<style scoped>
.stmt { display: flex; gap: 8px; background: var(--bg); border: 1px solid var(--bd); border-radius: 4px; padding: 6px 8px; }
.stmt.failed { border-color: var(--danger); background: color-mix(in srgb, var(--danger) 10%, var(--bg)); }
.stmt pre { margin: 0; font: 12px/1.55 var(--mono); white-space: pre-wrap; word-break: break-word; flex: 1; }
.idx { font-size: 10px; padding-top: 2px; min-width: 14px; text-align: right; }
.err { border: 1px solid var(--danger); background: color-mix(in srgb, var(--danger) 12%, transparent); border-radius: 4px; padding: 8px 10px; color: var(--danger); white-space: pre-wrap; }
.err .dim { color: var(--dim); margin-top: 4px; }
</style>
