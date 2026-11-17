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
/** the stored list of projectId is still being read: pushes are merged into it, not saved over it */
let loading = false;

const same = (a: RecentItem, b: RecentItem) => a.connId === b.connId && a.schema === b.schema && a.table === b.table;

function valid(r: unknown): r is RecentItem {
  const x = r as RecentItem | null;
  return !!x && (x.kind === "table" || x.kind === "structure") && typeof x.connId === "string" && !!x.connId &&
    typeof x.schema === "string" && typeof x.table === "string" && !!x.table;
}

function save() {
  projectApi.SetUIState(`recent:${projectId}`, JSON.stringify(recent.value)).catch(() => {});
}

export async function loadRecent(pid: string) {
  projectId = pid;
  loading = true;
  recent.value = [];
  let list: RecentItem[] = [];
  try {
    const raw = await projectApi.GetUIState(`recent:${pid}`);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    if (Array.isArray(parsed)) list = parsed.filter(valid);
  } catch {
    /* first run / corrupt state */
  }
  if (projectId !== pid) return;
  loading = false;
  // tabs restored while reading (the active one is pushed) come first
  const pushed = recent.value;
  recent.value = [...pushed, ...list.filter((r) => !pushed.some((p) => same(p, r)))].slice(0, MAX);
  if (pushed.length) save();
}

export function pushRecent(item: RecentItem) {
  if (!projectId) return;
  if (recent.value[0] && same(recent.value[0], item) && recent.value[0].kind === item.kind) return;
  recent.value = [item, ...recent.value.filter((r) => !same(r, item))].slice(0, MAX);
  if (!loading) save();
}

/** Drops entries of connections that no longer exist. */
export function pruneRecent(exists: (connId: string) => boolean) {
  const next = recent.value.filter((r) => exists(r.connId));
  if (next.length !== recent.value.length) recent.value = next;
}
