<script setup lang="ts">
// Generic modal shell (shell agent; reusable by the data agent):
//   <Modal title="..." :width="520" @close="ui.closeModal()"> body <template #footer>...</template></Modal>
// Esc / overlay click emit "close". Focuses [data-autofocus] or the first field on open,
// traps Tab inside, restores focus on close.
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useProjectsStore } from "@/stores/projects";
import { useTabsStore } from "@/stores/tabs";
import { useUiStore } from "@/stores/ui";

const props = withDefaults(defineProps<{ title?: string; width?: number; closeOnOverlay?: boolean }>(), {
  width: 520,
  closeOnOverlay: true,
});
const emit = defineEmits<{ close: [] }>();
const root = ref<HTMLElement | null>(null);
const ui = useUiStore();
const accent = computed(() => {
  const t = useTabsStore().active;
  const c = t ? useProjectsStore().getConnection(t.connId)?.color : undefined;
  return c && c !== "none" ? c : undefined;
});
let unregister: (() => void) | null = null;
let prevFocus: Element | null = null;

const FOCUSABLE = 'input:not([disabled]):not([type=hidden]), select:not([disabled]), textarea:not([disabled]), button:not([disabled]), [tabindex]:not([tabindex="-1"])';

function onKey(e: KeyboardEvent) {
  if (!root.value) return;
  // Only the topmost modal reacts.
  const all = document.querySelectorAll("[data-modal-root]");
  if (all[all.length - 1] !== root.value) return;
  if (e.key === "Escape") {
    e.preventDefault();
    e.stopPropagation();
    emit("close");
  } else if (e.key === "Tab") {
    const els = [...root.value.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((x) => x.offsetParent !== null);
    if (!els.length) return;
    const first = els[0];
    const last = els[els.length - 1];
    const a = document.activeElement;
    if (e.shiftKey && (a === first || !root.value.contains(a))) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && (a === last || !root.value.contains(a))) {
      e.preventDefault();
      first.focus();
    }
  }
}

onMounted(() => {
  unregister = ui.registerOverlay();
  prevFocus = document.activeElement;
  window.addEventListener("keydown", onKey, true);
  const el = root.value?.querySelector<HTMLElement>("[data-autofocus]") ?? root.value?.querySelector<HTMLElement>("input:not([type=checkbox]), select, textarea");
  (el ?? root.value?.querySelector<HTMLElement>("button.primary, button.danger"))?.focus();
});
onBeforeUnmount(() => {
  unregister?.();
  window.removeEventListener("keydown", onKey, true);
  ui.restoreFocus(prevFocus);
});

const downOnOverlay = ref(false);
function onOverlayUp() {
  if (props.closeOnOverlay && downOnOverlay.value) emit("close");
  downOnOverlay.value = false;
}
</script>

<template>
  <Teleport to="body">
    <div
      class="fixed inset-0 flex items-start justify-center overflow-auto py-[4vh]"
      :style="{ zIndex: 'var(--z-modal)', background: 'var(--scrim)', backdropFilter: 'blur(2px)' }"
      :data-accent="accent"
      @mousedown.self="downOnOverlay = true"
      @mouseup.self="onOverlayUp"
    >
      <div
        ref="root"
        data-modal-root
        role="dialog"
        aria-modal="true"
        class="no-drag flex max-h-[92vh] max-w-[94vw] flex-col rounded-[14px] border border-bd2 bg-pop"
        :style="{ width: width + 'px', boxShadow: '0 20px 60px var(--shadow)' }"
        @mousedown="downOnOverlay = false"
      >
        <header v-if="title || $slots.header" class="flex flex-none items-center gap-2 border-b border-bd px-3.5 py-2.5 font-semibold">
          <slot name="header">{{ title }}</slot>
        </header>
        <div class="flex min-h-0 flex-col gap-2.5 overflow-auto px-3.5 py-3"><slot /></div>
        <footer v-if="$slots.footer" class="flex flex-none items-center justify-end gap-2 border-t border-bd px-3.5 py-2.5">
          <slot name="footer" />
        </footer>
      </div>
    </div>
  </Teleport>
</template>
