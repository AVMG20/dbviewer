<script setup lang="ts">
import { computed, ref } from "vue";
import { useProjectsStore } from "@/stores/projects";
import { useUiStore } from "@/stores/ui";
import Modal from "./Modal.vue";

const ui = useUiStore();
const projects = useProjectsStore();
const props = defineProps<{ connId: string }>();
const conn = computed(() => projects.getConnection(props.connId));
const pw = ref("");
</script>

<template>
  <Modal title="Password required" :width="380" @close="ui.resolvePassword(null)">
    <form class="flex flex-col gap-2" @submit.prevent="ui.resolvePassword(pw)">
      <div class="dim">
        Enter the password for <b class="text-tx">{{ conn?.name }}</b>
        <span v-if="conn?.user"> ({{ conn.user }})</span>. It is not stored.
      </div>
      <input v-model="pw" type="password" class="input !h-6" data-autofocus autocomplete="off" />
    </form>
    <template #footer>
      <button class="btn" @click="ui.resolvePassword(null)">Cancel</button>
      <button class="btn primary" @click="ui.resolvePassword(pw)">Connect</button>
    </template>
  </Modal>
</template>
