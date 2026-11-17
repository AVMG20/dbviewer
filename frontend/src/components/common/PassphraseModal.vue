<script setup lang="ts">
// SSH key passphrase prompt (shell agent): shows the key, calls ProvideKeyPassphrase, errors inline.
import { ref } from "vue";
import { apiError, connectionApi } from "@/api";
import { useUiStore } from "@/stores/ui";
import Modal from "./Modal.vue";

const props = defineProps<{ keyPath: string; keyType?: string; fingerprint?: string }>();
const ui = useUiStore();
const pass = ref("");
const remember = ref(false);
const error = ref("");
const busy = ref(false);

async function submit() {
  if (busy.value || !pass.value) return;
  busy.value = true;
  error.value = "";
  try {
    await connectionApi.ProvideKeyPassphrase(props.keyPath, pass.value, remember.value);
    ui.resolvePassphrase(true);
  } catch (e) {
    const err = apiError(e);
    error.value = err.code === "invalid_argument" ? "Wrong passphrase, try again." : err.message;
    busy.value = false;
  }
}
</script>

<template>
  <Modal title="SSH key passphrase" :width="440" @close="ui.resolvePassphrase(false)">
    <form class="flex flex-col gap-2.5" @submit.prevent="submit">
      <div class="dim">The key below is protected by a passphrase.</div>
      <div class="rounded border border-bd bg-bg2 px-2.5 py-1.5 font-mono text-[11.5px]">
        <div class="selectable break-all">{{ keyPath }}</div>
        <div v-if="keyType || fingerprint" class="dim selectable break-all">{{ [keyType, fingerprint].filter(Boolean).join(" ") }}</div>
      </div>
      <input v-model="pass" type="password" class="input" placeholder="Passphrase" autocomplete="off" data-autofocus />
      <label class="flex items-center gap-2 text-[12px]">
        <input v-model="remember" type="checkbox" style="accent-color: var(--ac)" /> Remember passphrase
        <span class="dim text-[11px]">stored locally in the app's data folder</span>
      </label>
      <div v-if="error" class="text-danger">{{ error }}</div>
    </form>
    <template #footer>
      <button class="btn" @click="ui.resolvePassphrase(false)">Cancel</button>
      <button class="btn primary" :disabled="busy || !pass" @click="submit">Unlock</button>
    </template>
  </Modal>
</template>
