<script setup lang="ts">
// Settings (shell), opened with Cmd+, or from the palette. Changes apply immediately and
// are persisted with the other ui prefs (stores/ui.ts).
import { computed } from "vue";
import { useUiStore } from "@/stores/ui";
import { DEFAULT_SETTINGS, PAGE_SIZES, settings, type DateStyle, type Density, type NullStyle } from "@/lib/settings";
import type { Theme } from "@/types";
import { kbd } from "@/lib/kbd";
import Modal from "./Modal.vue";

const ui = useUiStore();

const THEMES: { v: Theme; l: string }[] = [
  { v: "system", l: "System" },
  { v: "light", l: "Light" },
  { v: "dark", l: "Dark" },
];
const DENSITIES: { v: Density; l: string }[] = [
  { v: "compact", l: "Compact" },
  { v: "comfortable", l: "Comfortable" },
];
const NULLS: { v: NullStyle; l: string }[] = [
  { v: "NULL", l: "NULL" },
  { v: "null", l: "null" },
  { v: "∅", l: "∅" },
  { v: "empty", l: "Empty" },
];
const sampleDate = new Date(2026, 2, 14, 9, 30, 0);
const DATES = computed<{ v: DateStyle; l: string }[]>(() => [
  { v: "iso", l: "2026-03-14 09:30:00" },
  { v: "local", l: sampleDate.toLocaleString() },
]);
const zoomPct = computed(() => Math.round(settings.zoom * 100) + "%");

function reset() {
  Object.assign(settings, DEFAULT_SETTINGS);
  ui.setTheme("system");
}
</script>

<template>
  <Modal title="Settings" :width="520" @close="ui.closeModal()">
    <div class="grid">
      <label>Theme</label>
      <div class="seg" role="radiogroup" aria-label="Theme">
        <button v-for="t in THEMES" :key="t.v" :class="{ on: ui.theme === t.v }" role="radio" :aria-checked="ui.theme === t.v" @click="ui.setTheme(t.v)">{{ t.l }}</button>
      </div>

      <label>Zoom</label>
      <div class="flex items-center gap-1.5">
        <button class="btn" :title="kbd('Zoom out (⌘-)')" aria-label="Zoom out" @click="ui.zoomOut()">−</button>
        <span class="mono w-12 text-center">{{ zoomPct }}</span>
        <button class="btn" :title="kbd('Zoom in (⌘=)')" aria-label="Zoom in" @click="ui.zoomIn()">+</button>
        <button class="btn ghost" :disabled="settings.zoom === 1" :title="kbd('Reset (⌘0)')" @click="ui.zoomReset()">Reset</button>
      </div>

      <label>Row height</label>
      <div class="seg" role="radiogroup" aria-label="Row height">
        <button v-for="d in DENSITIES" :key="d.v" :class="{ on: settings.density === d.v }" role="radio" :aria-checked="settings.density === d.v" @click="settings.density = d.v">{{ d.l }}</button>
      </div>

      <label for="set-page">Rows per page</label>
      <div>
        <select id="set-page" v-model.number="settings.pageSize" class="input sel">
          <option v-for="n in PAGE_SIZES" :key="n" :value="n">{{ n.toLocaleString() }} rows</option>
        </select>
        <div class="help">For tables you open from now on.</div>
      </div>

      <label>Show NULL as</label>
      <div class="seg mono" role="radiogroup" aria-label="Show NULL as">
        <button v-for="n in NULLS" :key="n.v" :class="{ on: settings.nullStyle === n.v }" role="radio" :aria-checked="settings.nullStyle === n.v" @click="settings.nullStyle = n.v">{{ n.l }}</button>
      </div>

      <label>Dates</label>
      <div>
        <div class="seg mono" role="radiogroup" aria-label="Dates">
          <button v-for="d in DATES" :key="d.v" :class="{ on: settings.dateStyle === d.v }" role="radio" :aria-checked="settings.dateStyle === d.v" @click="settings.dateStyle = d.v">{{ d.l }}</button>
        </div>
        <div class="help">Display only. Editing, copying and filters always use the stored value.</div>
      </div>
    </div>
    <template #footer>
      <button class="btn ghost mr-auto" @click="reset">Reset to defaults</button>
      <button class="btn primary" @click="ui.closeModal()">Done</button>
    </template>
  </Modal>
</template>

<style scoped>
.grid { display: grid; grid-template-columns: 130px 1fr; gap: 14px 12px; align-items: center; }
label { color: var(--dim); }
.help { margin-top: 3px; font-size: 11px; color: var(--dim); }
.seg { display: inline-flex; padding: 2px; gap: 2px; border: 1px solid var(--bd); border-radius: var(--r); background: var(--bg2); flex-wrap: wrap; }
.seg button { border: 0; background: none; padding: 3px 10px; border-radius: calc(var(--r) - 2px); color: var(--dim); }
.seg button:hover { color: var(--tx); }
.seg button.on { background: var(--bg); color: var(--tx); box-shadow: 0 1px 2px var(--shadow); }
</style>
