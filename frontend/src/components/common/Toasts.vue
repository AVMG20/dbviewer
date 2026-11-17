<script setup lang="ts">
import { ref } from "vue";
import { useUiStore } from "@/stores/ui";
import type { Toast, ToastAction } from "@/types";
import Icon from "./Icon.vue";

const ui = useUiStore();
/** Toasts whose technical details are expanded (they stay until dismissed). */
const open = ref(new Set<number>());
function toggleDetail(t: Toast) {
  const s = new Set(open.value);
  if (s.has(t.id)) s.delete(t.id);
  else {
    s.add(t.id);
    ui.pinToast(t.id); // reading the details: keep it until dismissed
  }
  open.value = s;
}
function act(t: Toast, a: ToastAction) {
  ui.dismissToast(t.id);
  a.run();
}
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
          <div class="min-w-0">
            <div class="selectable break-words">
              {{ t.message }}<span v-if="(ui.toastCounts[t.id] ?? 1) > 1" class="dim ml-1 font-mono">×{{ ui.toastCounts[t.id] }}</span>
            </div>
            <pre v-if="t.detail && open.has(t.id)" class="detail selectable mono">{{ t.detail }}</pre>
            <div v-if="t.actions?.length || t.detail" class="mt-1 flex flex-wrap items-center gap-1.5">
              <button v-for="a in t.actions" :key="a.label" class="btn act" @click="act(t, a)">{{ a.label }}</button>
              <button v-if="t.detail" class="link" @click="toggleDetail(t)">{{ open.has(t.id) ? "Hide details" : "Details" }}</button>
            </div>
          </div>
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
.act { padding: 1px 8px; font-size: 11px; }
.link { border: 0; background: none; padding: 0 2px; color: var(--dim); font-size: 11px; text-decoration: underline; text-underline-offset: 2px; }
.link:hover { color: var(--tx); }
.detail { margin: 4px 0 0; padding: 4px 6px; max-height: 120px; overflow: auto; white-space: pre-wrap; word-break: break-word; font-size: 10.5px; color: var(--dim); background: var(--bg2); border-radius: 5px; }
</style>
