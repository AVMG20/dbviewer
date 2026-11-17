<script setup lang="ts">
// Status bar (shell agent): active connection (color, driver, server version),
// read-only indicator, last query time + rows (ui.setStatus), pending changes.
import { computed } from "vue";
import { useChangesStore } from "@/stores/changes";
import { useProjectsStore } from "@/stores/projects";
import { useTabsStore } from "@/stores/tabs";
import { useUiStore } from "@/stores/ui";
import type { Driver } from "@/api";
import { DRIVER_LABEL } from "@/types";
import { copyText } from "@/lib/clipboard";

const projects = useProjectsStore();
const tabs = useTabsStore();
const ui = useUiStore();
const changes = useChangesStore();

const tab = computed(() => tabs.active);
const conn = computed(() => (tab.value ? projects.getConnection(tab.value.connId) : undefined));
const st = computed(() => (tab.value ? projects.statuses[tab.value.connId] : undefined));
const info = computed(() => (tab.value ? ui.statusByTab[tab.value.id] : undefined));
const pending = computed(() => {
  if (!tab.value) return 0;
  try {
    return changes.count(tab.value.id);
  } catch {
    return 0;
  }
});
const fmt = (n: number) => n.toLocaleString("en-US");
const dur = (ms: number) => (ms >= 1000 ? (ms / 1000).toFixed(2) + " s" : Math.round(ms) + " ms");
const tint = computed(() => projects.connColor(conn.value?.id ?? ""));
/** Without a tab: the open connections of the project (instead of a bare "No connection"). */
const openConns = computed(() => projects.projectConnections.filter((c) => projects.statuses[c.id]?.connected));

// Selection aggregates of the active tab's grid.
const stats = computed(() => (ui.selectionStats && ui.selectionStats.tabId === tab.value?.id ? ui.selectionStats : null));
function num(n: number): string {
  const a = Math.abs(n);
  const digits = a !== 0 && a < 1 ? 6 : a < 1000 ? 4 : 2;
  return n.toLocaleString(undefined, { maximumFractionDigits: digits });
}
const statParts = computed(() => {
  const s = stats.value;
  if (!s) return [];
  const out: { k: string; v: string; copy: string }[] = [{ k: "Count", v: fmt(s.values), copy: String(s.values) }];
  if (s.numbers && s.sum !== undefined) {
    out.push({ k: "Sum", v: num(s.sum), copy: String(s.sum) });
    if (s.numbers > 1) out.push({ k: "Avg", v: num(s.avg!), copy: String(s.avg) }, { k: "Min", v: num(s.min!), copy: String(s.min) }, { k: "Max", v: num(s.max!), copy: String(s.max) });
  }
  return out;
});
async function copyStat(v: string) {
  await copyText(v);
  ui.toast(`Copied ${v}`, "success", 1200);
}
const hasTint = computed(() => !!conn.value && conn.value.color !== "none");
</script>

<template>
  <footer
    class="no-drag flex flex-none items-center gap-3.5 border-t border-bd bg-bg2 px-2.5 text-[11px] text-dim"
    :style="{
      height: 'var(--statusbar-h)',
      '--conn': tint,
      background: hasTint ? 'color-mix(in srgb, var(--conn) 8%, var(--bg2))' : undefined,
    }"
  >
    <template v-if="conn">
      <span class="flex items-center gap-1.5 text-tx">
        <span class="conn-dot" />{{ conn.name }}
        <span v-if="st?.connecting" class="dim">connecting…</span>
        <span v-else-if="!st?.connected" class="dim">(not connected)</span>
      </span>
      <span>{{ st?.serverVersion || (DRIVER_LABEL[conn.driver as Driver] ?? conn.driver) }}</span>
      <span v-if="st?.database" class="mono">{{ st.database }}</span>
      <span v-if="conn.readOnly" class="pill" :class="{ neutral: conn.color !== 'red' }" style="padding-block: 0">{{ conn.color === "red" ? "PRODUCTION · READ-ONLY" : "READ-ONLY" }}</span>
      <span v-else-if="conn.color === 'red'" class="pill" style="padding-block: 0">PRODUCTION</span>
    </template>
    <template v-else-if="openConns.length">
      <span v-for="c in openConns.slice(0, 4)" :key="c.id" class="flex items-center gap-1.5 text-tx" :style="{ '--conn': projects.connColor(c.id) }">
        <span class="conn-dot" />{{ c.name }}
      </span>
      <span v-if="openConns.length > 4">+{{ openConns.length - 4 }} more</span>
    </template>
    <span v-else-if="projects.projectConnections.length">Not connected · click a connection in the sidebar</span>
    <span v-else>No connections yet</span>

    <span v-if="info?.durationMs !== undefined">Query: {{ dur(info.durationMs) }}</span>
    <span v-if="info?.rows !== undefined">
      {{ fmt(info.rows) }} row{{ info.rows === 1 ? "" : "s" }}<template v-if="info.total !== undefined && info.total >= 0 && info.total !== info.rows"> of {{ fmt(info.total) }}</template>
    </span>
    <span v-if="info?.message">{{ info.message }}</span>

    <span class="flex-1" />
    <span v-if="statParts.length" class="flex items-center gap-2.5" aria-label="Selection summary">
      <button v-for="p in statParts" :key="p.k" class="stat" :title="`Copy ${p.k.toLowerCase()}`" @click="copyStat(p.copy)">
        {{ p.k }} <span class="mono text-tx">{{ p.v }}</span>
      </button>
    </span>
    <span v-if="pending" class="text-warn">{{ pending }} pending change{{ pending === 1 ? "" : "s" }}</span>
  </footer>
</template>

<style scoped>
.stat { border: 0; background: none; padding: 0 2px; color: var(--dim); font-size: 11px; border-radius: 3px; }
.stat:hover { background: var(--hov); }
</style>
