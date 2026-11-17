<script setup lang="ts">
// Registered as the "saveChanges" modal:
//   data:      ui.openModal("saveChanges", { tabId, statements?, error?, failedIndex?, title? })
//   structure: ui.openModal("saveChanges", { statements, apply, onDone, title?, note?, confirmText?, error? })
// Previews (unless statements are given), then applies in one go; failures keep the dialog (and the staged changes) open.
import { onMounted, ref } from "vue";
import { apiError } from "@/api";
import { useChangesStore } from "@/stores/changes";
import { useUiStore } from "@/stores/ui";
import SaveChangesModal from "./SaveChangesModal.vue";

type ApplyFn = () => Promise<{ ok: boolean; error: string; failedIndex: number }>;
const props = defineProps<{
  tabId?: string; statements?: string[]; error?: string; failedIndex?: number; title?: string; note?: string;
  confirmText?: string; apply?: ApplyFn; onDone?: () => void | Promise<void>;
}>();
const ui = useUiStore();
const changes = useChangesStore();

const statements = ref<string[]>(props.statements ?? []);
const loading = ref(!props.statements && !!props.tabId);
const applying = ref(false);
const error = ref(props.error ?? "");
const failedIndex = ref(props.failedIndex ?? -1);

onMounted(async () => {
  if (props.statements || !props.tabId) return;
  try {
    statements.value = await changes.preview(props.tabId);
  } catch (e) {
    const err = apiError(e);
    error.value = err.message || err.code;
  }
  loading.value = false;
});

async function confirm() {
  applying.value = true;
  error.value = "";
  failedIndex.value = -1;
  if (props.apply) {
    const res = await props.apply();
    applying.value = false;
    if (res.ok) {
      ui.closeModal();
      await props.onDone?.();
    } else {
      error.value = res.error || "Unknown error";
      failedIndex.value = res.failedIndex ?? -1;
    }
    return;
  }
  const tabId = props.tabId!;
  const n = changes.count(tabId);
  const res = await changes.apply(tabId);
  applying.value = false;
  if (res.ok) {
    ui.closeModal();
    ui.toast(`Saved ${n} change${n === 1 ? "" : "s"}${res.durationMs ? ` in ${Math.round(res.durationMs)} ms` : ""}`, "success");
    changes.notifySaved(tabId);
  } else {
    error.value = res.error || "Unknown error";
    failedIndex.value = res.failedIndex ?? -1;
    if (res.statements?.length) statements.value = res.statements;
  }
}
</script>

<template>
  <SaveChangesModal
    :title="title" :statements="statements" :loading="loading" :applying="applying" :error="error" :note="note"
    :confirm-text="confirmText" :failed-index="failedIndex" @confirm="confirm" @cancel="ui.closeModal()"
  />
</template>
