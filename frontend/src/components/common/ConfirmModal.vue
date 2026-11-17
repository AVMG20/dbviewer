<script setup lang="ts">
import { useUiStore } from "@/stores/ui";
import type { ConfirmOptions } from "@/types";
import Modal from "./Modal.vue";

const o = defineProps<ConfirmOptions>();
const ui = useUiStore();
</script>

<template>
  <Modal :title="o.title" :width="o.sql ? 620 : 420" @close="ui.resolveConfirm(false)">
    <div class="whitespace-pre-line text-tx">{{ o.message }}</div>
    <pre v-if="o.sql" class="selectable m-0 max-h-60 overflow-auto whitespace-pre-wrap rounded border border-bd bg-bg p-2 font-mono text-[12px] leading-relaxed">{{ o.sql }}</pre>
    <template #footer>
      <!-- Safe default: Cancel has focus on destructive confirms; the red button needs Tab + Enter or a click. -->
      <button class="btn" :data-autofocus="o.danger ? '' : undefined" @click="ui.resolveConfirm(false)">Cancel</button>
      <button class="btn" :class="o.danger ? 'danger' : 'primary'" :data-autofocus="o.danger ? undefined : ''" @click="ui.resolveConfirm(true)">
        {{ o.confirmText ?? "OK" }}
      </button>
    </template>
  </Modal>
</template>
