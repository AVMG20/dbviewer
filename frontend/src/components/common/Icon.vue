<script setup lang="ts">
// Tiny inline SVG icon set (16x16 grid, stroke based), shared by shell and data views.
import { computed } from "vue";

const P: Record<string, string> = {
  lock: '<rect x="3" y="7" width="10" height="7" rx="1.5"/><path d="M5 7V5a3 3 0 016 0v2"/>',
  key: '<circle cx="5.5" cy="10.5" r="2.5"/><path d="M7.5 8.5L14 2M11 5l2 2"/>',
  refresh: '<path d="M13 8a5 5 0 11-1.5-3.5M13 2v3h-3"/>',
  plus: '<path d="M8 3v10M3 8h10"/>',
  trash: '<path d="M3 4h10M6 4V2h4v2M5 4l.5 9h5L11 4"/>',
  copy: '<rect x="5" y="5" width="8" height="8" rx="1"/><path d="M3 10V3h7"/>',
  chev: '<path d="M6 4l4 4-4 4"/>',
  table: '<rect x="2" y="3" width="12" height="10" rx="1"/><path d="M2 6.5h12M6 6.5V13"/>',
  view: '<rect x="2" y="3" width="12" height="10" rx="1" stroke-dasharray="2 1.5"/><path d="M2 6.5h12M6 6.5V13"/>',
  sql: '<path d="M5 4L2 8l3 4M11 4l3 4-3 4"/>',
  structure: '<path d="M3 3h10M3 8h6M3 13h10M11 6l2 2-2 2"/>',
  x: '<path d="M4 4l8 8M12 4l-8 8"/>',
  search: '<circle cx="7" cy="7" r="4"/><path d="M10 10l4 4"/>',
  db: '<ellipse cx="8" cy="4" rx="5" ry="2"/><path d="M3 4v8c0 1.1 2.2 2 5 2s5-.9 5-2V4M3 8c0 1.1 2.2 2 5 2s5-.9 5-2"/>',
  schema: '<rect x="2" y="2" width="12" height="12" rx="2"/><path d="M2 6h12"/>',
  folder: '<path d="M2 4.5A1.5 1.5 0 013.5 3H6l1.5 1.5h5A1.5 1.5 0 0114 6v5.5a1.5 1.5 0 01-1.5 1.5h-9A1.5 1.5 0 012 11.5z"/>',
  sun: '<circle cx="8" cy="8" r="3"/><path d="M8 1.5v1.5M8 13v1.5M1.5 8H3M13 8h1.5M3.4 3.4l1 1M11.6 11.6l1 1M3.4 12.6l1-1M11.6 4.4l1-1"/>',
  moon: '<path d="M13 9.5A5.5 5.5 0 116.5 3a4.5 4.5 0 006.5 6.5z"/>',
  monitor: '<rect x="2" y="3" width="12" height="8" rx="1"/><path d="M6 14h4M8 11v3"/>',
  plug: '<path d="M6 2v3M10 2v3M4 5h8v2a4 4 0 01-8 0zM8 11v3"/>',
  unplug: '<path d="M6 2v3M10 2v3M4 5h8v2a4 4 0 01-8 0zM8 11v3M2 2l12 12"/>',
  edit: '<path d="M10.5 2.5l3 3L6 13H3v-3z"/>',
  warn: '<path d="M8 2l6.5 11.5h-13zM8 6.5v3M8 11.5v.5"/>',
  check: '<path d="M3 8.5l3.5 3.5L13 4.5"/>',
  info: '<circle cx="8" cy="8" r="6"/><path d="M8 7.5v4M8 5v.5"/>',
  cmd: '<path d="M5.5 5.5h5v5h-5zM5.5 5.5a2 2 0 10-2-2 2 2 0 012 2zM10.5 5.5a2 2 0 112-2 2 2 0 01-2 2zM5.5 10.5a2 2 0 11-2 2 2 2 0 012-2zM10.5 10.5a2 2 0 102 2 2 2 0 00-2-2z"/>',
  query: '<path d="M3 2h7l3 3v9H3zM10 2v3h3M5.5 8h5M5.5 11h5"/>',
  sidebar: '<rect x="2" y="3" width="12" height="10" rx="1"/><path d="M6 3v10"/>',
  keyboard: '<rect x="1.5" y="4" width="13" height="8" rx="1.5"/><path d="M4 6.5h.01M6.5 6.5h.01M9 6.5h.01M11.5 6.5h.01M4.5 9.5h7"/>',
  gear: '<circle cx="8" cy="8" r="2"/><path d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.4 3.4l1.4 1.4M11.2 11.2l1.4 1.4M3.4 12.6l1.4-1.4M11.2 4.8l1.4-1.4"/>',
  collapse: '<path d="M4 6l4-3 4 3M4 10l4 3 4-3"/>',
  upload: '<path d="M8 10V2M5 5l3-3 3 3M3 13h10"/>',
  file: '<path d="M3 2h7l3 3v9H3zM10 2v3h3"/>',
  undo: '<path d="M6 3L2.5 6.5 6 10M3 6.5h6.5a3.5 3.5 0 010 7H7"/>',
  redo: '<path d="M10 3l3.5 3.5L10 10M13 6.5H6.5a3.5 3.5 0 000 7H9"/>',
  save: '<path d="M3 2.5h8l2.5 2.5v8.5h-10.5zM5 2.5v3.5h5V2.5M5 13.5V9.5h6v4"/>',
  export: '<path d="M8 10V2M5 5l3-3 3 3M3 10v3.5h10V10"/>',
  panel: '<rect x="2" y="3" width="12" height="10" rx="1"/><path d="M10 3v10"/>',
  play: '<path d="M4.5 2.5l9 5.5-9 5.5z"/>',
  stop: '<rect x="3.5" y="3.5" width="9" height="9" rx="1"/>',
  link: '<path d="M7 9a3 3 0 004.2 0l2-2a3 3 0 00-4.2-4.2l-.7.7M9 7a3 3 0 00-4.2 0l-2 2A3 3 0 007 13.2l.7-.7"/>',
  filter: '<path d="M2.5 3h11L9.5 8.5V13l-3-1.5V8.5z"/>',
  chevR: '<path d="M6 4l4 4-4 4"/>',
  chevL: '<path d="M10 4L6 8l4 4"/>',
  chevD: '<path d="M4 6l4 4 4-4"/>',
  chevU: '<path d="M4 10l4-4 4 4"/>',
  sortUp: '<path d="M8 13V3M4 7l4-4 4 4"/>',
  sortDown: '<path d="M8 3v10M4 9l4 4 4-4"/>',
  history: '<path d="M2.5 8a5.5 5.5 0 101.8-4.1M2.5 2.5v3h3M8 5v3.2l2 1.3"/>',
  bookmark: '<path d="M4 2.5h8v11L8 10.5 4 13.5z"/>',
  expand: '<path d="M2.5 6.5v-4h4M13.5 9.5v4h-4M2.5 2.5L7 7M13.5 13.5L9 9"/>',
  explain: '<path d="M2.5 4h11M2.5 8h7M2.5 12h4M11 10l2.5 2.5L11 15"/>',
  columns: '<rect x="2.5" y="2.5" width="11" height="11" rx="1"/><path d="M6.2 2.5v11M9.8 2.5v11"/>',
  dup: '<rect x="5.5" y="5.5" width="8" height="8" rx="1"/><path d="M10.5 3.5v-1h-8v8h1"/>',
  sparkle: '<path d="M7 2l1.2 3.3L11.5 6.5 8.2 7.7 7 11 5.8 7.7 2.5 6.5l3.3-1.2zM12.5 9.5l.6 1.4 1.4.6-1.4.6-.6 1.4-.6-1.4-1.4-.6 1.4-.6z"/>',
  dots: '<circle cx="3.5" cy="8" r="1"/><circle cx="8" cy="8" r="1"/><circle cx="12.5" cy="8" r="1"/>',
};

const props = defineProps<{ name: string; size?: number }>();
const html = computed(() => P[props.name] ?? "");
</script>

<template>
  <svg
    :width="size ?? 13"
    :height="size ?? 13"
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    stroke-width="1.4"
    stroke-linecap="round"
    stroke-linejoin="round"
    style="flex: none; display: inline-block; vertical-align: middle"
    aria-hidden="true"
    v-html="html"
  />
</template>
