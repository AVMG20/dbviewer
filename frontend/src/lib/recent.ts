// Recently opened tables / structures per project (shell), shown first in an empty Cmd+K.
// Fed by App.vue from the active tab; persisted in ui state "recent:<projectId>".
import { ref } from "vue";
import { projectApi } from "@/api";

export interface RecentItem {
  kind: "table" | "structure";
  connId: string;
  schema: string;
  table: string;
}

const MAX = 12;
export const recent = ref<RecentItem[]>([]);
let projectId = "";

const same = (a: RecentItem, b: RecentItem) => a.connId === b.connId && a.schema === b.schema && a.table === b.table;

export async function loadRecent(pid: string) {
  projectId = pid;
  recent.value = [];
  try {
    const raw = await projectApi.GetUIState(`recent:${pid}`);
    const list = raw ? (JSON.parse(raw) as RecentItem[]) : [];
    if (projectId === pid && Array.isArray(list)) recent.value = list.filter((r) => r && r.connId && r.table).slice(0, MAX);
  } catch {
    /* first run */
  }
}

export function pushRecent(item: RecentItem) {
  if (!projectId) return;
  if (recent.value[0] && same(recent.value[0], item) && recent.value[0].kind === item.kind) return;
  recent.value = [item, ...recent.value.filter((r) => !same(r, item))].slice(0, MAX);
  projectApi.SetUIState(`recent:${projectId}`, JSON.stringify(recent.value)).catch(() => {});
}

/** Drops entries of connections that no longer exist. */
export function pruneRecent(exists: (connId: string) => boolean) {
  const next = recent.value.filter((r) => exists(r.connId));
  if (next.length !== recent.value.length) recent.value = next;
}
