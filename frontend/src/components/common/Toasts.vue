<script setup lang="ts">
import { useUiStore } from "@/stores/ui";
import Icon from "./Icon.vue";

const ui = useUiStore();
const icon = { info: "info", success: "check", warning: "warn", error: "warn" } as const;
const color = { info: "var(--ac)", success: "var(--ok)", warning: "var(--warn)", error: "var(--danger)" } as const;
</script>

<template>
  <Teleport to="body">
    <div class="pointer-events-none fixed right-3 flex flex-col items-end gap-1.5" :style="{ bottom: 'calc(var(--statusbar-h) + 10px)', zIndex: 'var(--z-toast)' }">
      <TransitionGroup name="toast">
        <div
          v-for="t in ui.toasts"
          :key="t.id"
          class="pointer-events-auto flex max-w-[420px] items-start gap-2 rounded-[10px] border border-bd2 bg-pop py-1.5 pl-3 pr-1.5 text-tx"
          :style="{ boxShadow: '0 6px 22px var(--shadow)', borderLeft: `3px solid ${color[t.kind]}` }"
          :role="t.kind === 'error' ? 'alert' : 'status'"
        >
          <span class="mt-px" :style="{ color: color[t.kind] }"><Icon :name="icon[t.kind]" /></span>
          <span class="selectable min-w-0 break-words">
            {{ t.message }}<span v-if="(ui.toastCounts[t.id] ?? 1) > 1" class="dim ml-1 font-mono">×{{ ui.toastCounts[t.id] }}</span>
          </span>
          <button class="close" title="Dismiss" aria-label="Dismiss" @click="ui.dismissToast(t.id)"><Icon name="x" :size="10" /></button>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<style scoped>
.toast-enter-active, .toast-leave-active { transition: all .15s ease; }
.toast-enter-from, .toast-leave-to { opacity: 0; transform: translateX(8px); }
.close { display: inline-flex; align-items: center; justify-content: center; width: 16px; height: 16px; border: 0; border-radius: 3px; background: none; color: var(--dim); padding: 0; flex: none; }
.close:hover { background: var(--hov); color: var(--tx); }
</style>
