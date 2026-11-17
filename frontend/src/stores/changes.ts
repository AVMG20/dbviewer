// Staged grid changes per table tab (owned by the "data" agent).
// Nothing is written until preview()/apply(). Per-tab undo/redo (Cmd+Z / Cmd+Shift+Z) via state snapshots.
import { defineStore } from "pinia";
import { ref } from "vue";
import { apiError, dataApi, newId, type ApplyResult, type CellValue, type ChangeSet } from "@/api";
import { useTabsStore } from "./tabs";

/** Stable key of a loaded row: JSON of its PK values in PK column order. */
export type RowKey = string;

export interface StagedUpdate {
  pk: Record<string, CellValue>;
  /** column -> new value (CellValue or {$type:"default"}) */
  set: Record<string, CellValue>;
}

export interface StagedInsert {
  tempId: string; // newId(); shown as a green row
  values: Record<string, CellValue>;
}

interface Snapshot {
  updates: Record<RowKey, StagedUpdate>;
  inserts: StagedInsert[];
  deletes: Record<RowKey, Record<string, CellValue>>;
}

export interface TableChanges extends Snapshot {
  connId: string;
  schema: string;
  table: string;
  history: Snapshot[]; // undo stack (state before each user action)
  redo: Snapshot[];
}

const MAX_HISTORY = 100;
const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v));

export function makeRowKey(pkValues: CellValue[]): RowKey {
  return JSON.stringify(pkValues);
}

export const useChangesStore = defineStore("changes", () => {
  /** keyed by tab id */
  const byTab = ref<Record<string, TableChanges>>({});
  /** Bumped after a successful save; views reload when their tab's counter changes. */
  const savedSeq = ref<Record<string, number>>({});
  const batchDepth = new Map<string, number>();

  function notifySaved(tabId: string) {
    savedSeq.value = { ...savedSeq.value, [tabId]: (savedSeq.value[tabId] ?? 0) + 1 };
  }

  /** Number of pending changes: updated rows (not deleted ones) + inserts + deletes. */
  function count(tabId: string): number {
    const c = byTab.value[tabId];
    if (!c) return 0;
    const updates = Object.keys(c.updates).filter((k) => !(k in c.deletes)).length;
    return updates + c.inserts.length + Object.keys(c.deletes).length;
  }
  function hasChanges(tabId: string): boolean {
    return count(tabId) > 0;
  }
  function canUndo(tabId: string): boolean {
    return (byTab.value[tabId]?.history.length ?? 0) > 0;
  }
  function canRedo(tabId: string): boolean {
    return (byTab.value[tabId]?.redo.length ?? 0) > 0;
  }

  function syncDirty(tabId: string) {
    try {
      useTabsStore().setDirty(tabId, hasChanges(tabId));
    } catch {
      /* tabs store not ready */
    }
  }

  function snapshot(c: TableChanges): Snapshot {
    return clone({ updates: c.updates, inserts: c.inserts, deletes: c.deletes });
  }
  function restore(c: TableChanges, s: Snapshot) {
    c.updates = s.updates;
    c.inserts = s.inserts;
    c.deletes = s.deletes;
  }
  /** Records the state before a user action (once per batch). */
  function mark(tabId: string) {
    const c = byTab.value[tabId];
    if (!c) return;
    if ((batchDepth.get(tabId) ?? 0) > 0) return;
    pushHistory(c);
  }
  function pushHistory(c: TableChanges) {
    c.history.push(snapshot(c));
    if (c.history.length > MAX_HISTORY) c.history.shift();
    c.redo = [];
  }

  /** Ensures the container exists. Call when a table view mounts / the table changes. */
  function ensure(tabId: string, meta: { connId: string; schema: string; table: string }): TableChanges {
    let c = byTab.value[tabId];
    if (!c) {
      c = byTab.value[tabId] = { ...meta, updates: {}, inserts: [], deletes: {}, history: [], redo: [] };
    } else {
      c.connId = meta.connId;
      c.schema = meta.schema;
      c.table = meta.table;
    }
    return byTab.value[tabId];
  }

  /** Runs fn so that all its mutations undo together (one Cmd+Z). */
  function batch(tabId: string, fn: () => void) {
    const c = byTab.value[tabId];
    if (!c) return;
    const depth = batchDepth.get(tabId) ?? 0;
    if (depth === 0) pushHistory(c);
    batchDepth.set(tabId, depth + 1);
    try {
      fn();
    } finally {
      batchDepth.set(tabId, depth);
      // drop the history entry again if the batch changed nothing
      if (depth === 0) {
        const last = c.history[c.history.length - 1];
        if (last && JSON.stringify(last) === JSON.stringify(snapshot(c))) c.history.pop();
      }
      syncDirty(tabId);
    }
  }

  // ---- mutations ------------------------------------------------------------------
  /**
   * Stages one cell update on a loaded row. `original` (the value as loaded) lets the
   * store drop the change when the value is set back to the original.
   */
  function setCell(
    tabId: string, rowKey: RowKey, pk: Record<string, CellValue>, column: string, value: CellValue,
    original?: CellValue, hasOriginal = false,
  ): void {
    const c = byTab.value[tabId];
    if (!c) return;
    mark(tabId);
    const upd = c.updates[rowKey];
    if (hasOriginal && JSON.stringify(value) === JSON.stringify(original ?? null)) {
      if (upd) {
        delete upd.set[column];
        if (!Object.keys(upd.set).length) delete c.updates[rowKey];
      }
    } else {
      (c.updates[rowKey] ??= { pk, set: {} }).set[column] = value;
    }
    syncDirty(tabId);
  }

  /** Stages an edit of a cell in a not-yet-saved new row. */
  function setInsertCell(tabId: string, tempId: string, column: string, value: CellValue): void {
    const c = byTab.value[tabId];
    const ins = c?.inserts.find((i) => i.tempId === tempId);
    if (!c || !ins) return;
    mark(tabId);
    ins.values[column] = value;
    syncDirty(tabId);
  }

  function insertRow(tabId: string, values: Record<string, CellValue> = {}): string {
    const c = byTab.value[tabId];
    if (!c) throw new Error("changes.insertRow: unknown tab");
    mark(tabId);
    const tempId = newId();
    c.inserts.push({ tempId, values: { ...values } });
    syncDirty(tabId);
    return tempId;
  }

  /** Removes a new (unsaved) row. */
  function removeInsert(tabId: string, tempId: string): void {
    const c = byTab.value[tabId];
    if (!c) return;
    const at = c.inserts.findIndex((i) => i.tempId === tempId);
    if (at < 0) return;
    mark(tabId);
    c.inserts.splice(at, 1);
    syncDirty(tabId);
  }

  /** Marks loaded rows deleted; rows already marked are un-marked (toggle). */
  function deleteRows(tabId: string, rows: { rowKey: RowKey; pk: Record<string, CellValue> }[]): void {
    const c = byTab.value[tabId];
    if (!c) return;
    const allDeleted = rows.length > 0 && rows.every((r) => r.rowKey in c.deletes);
    mark(tabId);
    for (const r of rows) {
      if (allDeleted) delete c.deletes[r.rowKey];
      else if (!(r.rowKey in c.deletes)) c.deletes[r.rowKey] = r.pk;
    }
    syncDirty(tabId);
  }

  function undo(tabId: string): boolean {
    const c = byTab.value[tabId];
    const prev = c?.history.pop();
    if (!c || !prev) return false;
    c.redo.push(snapshot(c));
    restore(c, prev);
    syncDirty(tabId);
    return true;
  }
  function redo(tabId: string): boolean {
    const c = byTab.value[tabId];
    const next = c?.redo.pop();
    if (!c || !next) return false;
    c.history.push(snapshot(c));
    restore(c, next);
    syncDirty(tabId);
    return true;
  }

  function revertAll(tabId: string): void {
    const c = byTab.value[tabId];
    if (!c) return;
    c.updates = {};
    c.inserts = [];
    c.deletes = {};
    c.history = [];
    c.redo = [];
    syncDirty(tabId);
  }

  /** Drops everything for a tab (view unmounted). */
  function dispose(tabId: string): void {
    delete byTab.value[tabId];
    batchDepth.delete(tabId);
    syncDirty(tabId);
  }

  // ---- backend --------------------------------------------------------------------
  /** Builds the api ChangeSet for preview/apply. Updates of rows that are deleted are dropped. */
  function toChangeSet(tabId: string): ChangeSet | null {
    const c = byTab.value[tabId];
    if (!c || !hasChanges(tabId)) return null;
    return {
      connId: c.connId,
      schema: c.schema,
      table: c.table,
      updates: Object.entries(c.updates).filter(([k]) => !(k in c.deletes)).map(([, u]) => ({ pk: u.pk, set: u.set })),
      inserts: c.inserts.map((i) => ({ values: i.values })),
      deletes: Object.values(c.deletes).map((pk) => ({ pk })),
    } as ChangeSet;
  }

  /** PreviewChanges: the SQL statements that Save would run. Throws the api error. */
  async function preview(tabId: string): Promise<string[]> {
    const cs = toChangeSet(tabId);
    if (!cs) return [];
    return await dataApi.PreviewChanges(cs);
  }

  /** ApplyChanges in one transaction. On success the staged changes are cleared. */
  async function apply(tabId: string): Promise<ApplyResult> {
    const cs = toChangeSet(tabId);
    const fail = (error: string) => ({ ok: false, statements: [], executed: 0, failedIndex: -1, error, rowsAffected: 0, durationMs: 0, insertedPks: [] }) as ApplyResult;
    if (!cs) return { ...fail(""), ok: true };
    try {
      const res = await dataApi.ApplyChanges(cs);
      if (res.ok) revertAll(tabId);
      return res;
    } catch (e) {
      const err = apiError(e);
      return fail(err.message || err.code);
    }
  }

  return {
    byTab, savedSeq, notifySaved, count, hasChanges, canUndo, canRedo, ensure, batch, setCell, setInsertCell, insertRow,
    removeInsert, deleteRows, undo, redo, revertAll, dispose, toChangeSet, preview, apply,
  };
});
