// Shared lazy schema cache (shell agent): schemas, tables and the speed-search index
// per connection. Used by the sidebar tree and the command palette.
import { reactive } from "vue";
import { apiError, schemaApi, type Column, type SchemaInfo, type Table } from "@/api";
import { useProjectsStore } from "@/stores/projects";

export interface IndexedTable {
  name: string;
  nameL: string;
  kind: string;
  columns: string[];
  columnsL: string[];
  /** display types, parallel to columns ("" when the backend sent none) */
  types: string[];
}

export const k = (connId: string, schema: string) => `${connId}\u0000${schema}`;
/** key of one table's column details (cache.columns) */
export const kt = (connId: string, schema: string, table: string) => `${connId}\u0000${schema}\u0000${table}`;

export const cache = reactive({
  schemas: {} as Record<string, SchemaInfo[]>,
  tables: {} as Record<string, Table[]>,
  /** shallow: replaced wholesale, never mutated */
  index: {} as Record<string, readonly IndexedTable[]>,
  /** full column details (pk / fk) per table, loaded when the table is expanded in the tree; shallow like index */
  columns: {} as Record<string, readonly Column[]>,
  loading: {} as Record<string, boolean>,
  errors: {} as Record<string, string>,
});

/** Bumped by invalidate(): loads that started before it must not write their (stale) result. */
const generation = new Map<string, number>();
const gen = (connId: string) => generation.get(connId) ?? 0;

/** "" means the connection's default schema: resolve it so the sidebar, palette and console share one key. */
export function normSchema(connId: string, schema: string): string {
  if (schema) return schema;
  return useProjectsStore().statuses[connId]?.defaultSchema || "";
}

const inflight = new Map<string, Promise<unknown>>();
function once<T>(key: string, fn: () => Promise<T>): Promise<T> {
  let p = inflight.get(key) as Promise<T> | undefined;
  if (!p) {
    cache.loading[key] = true;
    const mine: Promise<T> = fn().finally(() => {
      if (inflight.get(key) === mine) inflight.delete(key);
      cache.loading[key] = false;
    });
    p = mine;
    inflight.set(key, p);
  }
  return p;
}

export async function loadSchemas(connId: string, refresh = false): Promise<SchemaInfo[]> {
  if (!refresh && cache.schemas[connId]) return cache.schemas[connId];
  return once(`s:${connId}`, async () => {
    const g = gen(connId);
    try {
      const list = await schemaApi.Schemas(connId);
      if (g === gen(connId)) cache.schemas[connId] = list;
      delete cache.errors[connId];
      return list;
    } catch (e) {
      cache.errors[connId] = apiError(e).message;
      throw e;
    }
  });
}

export async function loadTables(connId: string, schemaIn: string, refresh = false): Promise<Table[]> {
  const schema = normSchema(connId, schemaIn);
  const key = k(connId, schema);
  if (!refresh && cache.tables[key]) return cache.tables[key];
  return once(`t:${key}`, async () => {
    const g = gen(connId);
    try {
      const list = await schemaApi.Tables(connId, schema, refresh);
      if (g === gen(connId)) cache.tables[key] = list;
      delete cache.errors[key];
      return list;
    } catch (e) {
      cache.errors[key] = apiError(e).message;
      throw e;
    }
  });
}

export async function loadIndex(connId: string, schemaIn: string, refresh = false): Promise<readonly IndexedTable[]> {
  const schema = normSchema(connId, schemaIn);
  const key = k(connId, schema);
  if (!refresh && cache.index[key]) return cache.index[key];
  return once(`i:${key}`, async () => {
    const g = gen(connId);
    try {
      const idx = await schemaApi.SearchSchema(connId, schema, refresh);
      const list: IndexedTable[] = idx.tables.map((t) => ({
        name: t.name,
        nameL: t.name.toLowerCase(),
        kind: t.kind,
        columns: t.columns,
        columnsL: t.columns.map((c) => c.toLowerCase()),
        types: t.columns.map((_, i) => t.types?.[i] ?? ""),
      }));
      const frozen = Object.freeze(list);
      if (g === gen(connId)) cache.index[key] = frozen;
      return frozen;
    } catch (e) {
      cache.errors[`i:${key}`] = apiError(e).message;
      throw e;
    }
  });
}

/** By-name lookup into a schema's index (built once per index array). */
const byName = new WeakMap<readonly IndexedTable[], Map<string, IndexedTable>>();
export function indexedTable(connId: string, schema: string, table: string): IndexedTable | undefined {
  const idx = cache.index[k(connId, schema)];
  if (!idx) return undefined;
  let m = byName.get(idx);
  if (!m) byName.set(idx, (m = new Map(idx.map((t) => [t.name, t]))));
  return m.get(table);
}

/** Column details of one table (types, pk, fk) for the tree's column rows. */
export async function loadColumns(connId: string, schemaIn: string, table: string, refresh = false): Promise<readonly Column[]> {
  const schema = normSchema(connId, schemaIn);
  const key = kt(connId, schema, table);
  if (!refresh && cache.columns[key]) return cache.columns[key];
  return once(`c:${key}`, async () => {
    const g = gen(connId);
    try {
      const list = Object.freeze((await schemaApi.Columns(connId, schema, table)) ?? []);
      if (g === gen(connId)) cache.columns[key] = list;
      delete cache.errors[`c:${key}`];
      return list;
    } catch (e) {
      cache.errors[`c:${key}`] = apiError(e).message;
      throw e;
    }
  });
}

/** Drops everything cached for a connection (disconnect / config change / refresh). */
export function invalidate(connId: string) {
  generation.set(connId, gen(connId) + 1);
  for (const key of [...inflight.keys()]) if (key.includes(connId)) inflight.delete(key);
  delete cache.schemas[connId];
  delete cache.errors[connId];
  for (const key of Object.keys(cache.tables)) if (key.startsWith(connId + "\u0000")) delete cache.tables[key];
  for (const key of Object.keys(cache.index)) if (key.startsWith(connId + "\u0000")) delete cache.index[key];
  for (const key of Object.keys(cache.columns)) if (key.startsWith(connId + "\u0000")) delete cache.columns[key];
  for (const key of Object.keys(cache.errors)) if (key.startsWith(`c:${connId}\u0000`)) delete cache.errors[key];
}

export function quoteIdent(driver: string, name: string): string {
  return driver === "mysql" ? "`" + name.replace(/`/g, "``") + "`" : '"' + name.replace(/"/g, '""') + '"';
}

/** Qualified, quoted table name for generated SQL. */
export function qualifiedName(driver: string, schema: string, table: string): string {
  if (driver === "sqlite" || !schema) return quoteIdent(driver, table);
  return `${quoteIdent(driver, schema)}.${quoteIdent(driver, table)}`;
}

export function isViewKind(kind: string): boolean {
  return kind === "view" || kind === "materialized_view";
}
