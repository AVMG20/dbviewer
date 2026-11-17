// Foreign-key navigation ("go to definition / find usages" for cells): link lookups per table (cached)
// and the target entries for a clicked cell.
import { schemaApi, type CellValue, type Driver, type ForeignKey, type ResultColumn } from "@/api";
import { quoteIdent, sqlLiteral } from "./copyFormats";
import { isDefaultValue } from "./format";

export interface Links {
  out: ForeignKey[];
  inn: ForeignKey[];
}

export interface NavEntry {
  heading: "References" | "Referenced by";
  /** e.g. sales_order_item.order_id */
  label: string;
  schema: string;
  table: string;
  where: string;
  /** columns of the target table used in `where` (for counting / display) */
  columns: string[];
}

const cache = new Map<string, Promise<Links>>();
const key = (c: string, s: string, t: string) => `${c}\u0000${s}\u0000${t}`;

/** Outgoing and incoming foreign keys of a table, cached per table (refresh = reload). */
export function loadLinks(connId: string, schema: string, table: string, refresh = false): Promise<Links> {
  const k = key(connId, schema, table);
  if (!refresh && cache.has(k)) return cache.get(k)!;
  const p = schemaApi
    .TableStructure(connId, schema, table)
    .then((st) => ({ out: st.foreignKeys ?? [], inn: st.referencedBy ?? [] }) as Links)
    .catch(() => {
      cache.delete(k);
      return { out: [], inn: [] } as Links;
    });
  cache.set(k, p);
  return p;
}
export function invalidateLinks(connId: string) {
  for (const k of cache.keys()) if (k.startsWith(connId + "\u0000")) cache.delete(k);
}

function usable(v: CellValue | undefined): v is CellValue {
  return v !== null && v !== undefined && !isDefaultValue(v);
}

function whereOf(driver: Driver | "", pairs: { col: string; v: CellValue }[]): string {
  return pairs.map(({ col, v }) => `${quoteIdent(driver, col)} = ${sqlLiteral(v, driver)}`).join(" AND ");
}

/** Navigation targets for column `ci` of a row: outgoing FK first, then incoming references. */
export function navEntries(
  links: Links | null, columns: ResultColumn[], values: CellValue[], ci: number, driver: Driver | "", schema: string,
): NavEntry[] {
  const col = columns[ci];
  if (!col || !usable(values[ci])) return [];
  const idx = (name: string) => columns.findIndex((c) => c.name === name);
  const out: NavEntry[] = [];
  const outFks = links?.out.filter((f) => f.columns.includes(col.name)) ?? [];
  if (outFks.length) {
    for (const fk of outFks) {
      const pairs = fk.columns.map((c, i) => ({ col: fk.refColumns[i], v: values[idx(c)] }));
      if (!pairs.every((p) => usable(p.v))) continue;
      out.push({
        heading: "References", label: `${fk.refTable}.${fk.refColumns.join(", ")}`, schema: fk.refSchema || schema, table: fk.refTable,
        where: whereOf(driver, pairs as { col: string; v: CellValue }[]), columns: fk.refColumns,
      });
    }
  } else if (col.fk) {
    out.push({
      heading: "References", label: `${col.fk.table}.${col.fk.column}`, schema: col.fk.schema || schema, table: col.fk.table,
      where: whereOf(driver, [{ col: col.fk.column, v: values[ci] }]), columns: [col.fk.column],
    });
  }
  for (const fk of links?.inn.filter((f) => f.refColumns.includes(col.name)) ?? []) {
    const pairs = fk.refColumns.map((c, i) => ({ col: fk.columns[i], v: values[idx(c)] }));
    if (!pairs.every((p) => usable(p.v))) continue;
    out.push({
      heading: "Referenced by", label: `${fk.table}.${fk.columns.join(", ")}`, schema: fk.schema || schema, table: fk.table,
      where: whereOf(driver, pairs as { col: string; v: CellValue }[]), columns: fk.columns,
    });
  }
  return out;
}

/** All incoming references for a whole row (record panel "Referenced by" list). */
export function incomingForRow(links: Links | null, columns: ResultColumn[], values: CellValue[], driver: Driver | "", schema: string): NavEntry[] {
  if (!links) return [];
  const idx = (name: string) => columns.findIndex((c) => c.name === name);
  const out: NavEntry[] = [];
  for (const fk of links.inn) {
    const pairs = fk.refColumns.map((c, i) => ({ col: fk.columns[i], v: values[idx(c)] }));
    if (!pairs.every((p) => usable(p.v))) continue;
    out.push({
      heading: "Referenced by", label: `${fk.table}.${fk.columns.join(", ")}`, schema: fk.schema || schema, table: fk.table,
      where: whereOf(driver, pairs as { col: string; v: CellValue }[]), columns: fk.columns,
    });
  }
  return out;
}

/** Names of columns that can navigate (outgoing FK or referenced by another table). */
export function navigableColumns(links: Links | null, columns: ResultColumn[]): Set<string> {
  const s = new Set<string>();
  for (const c of columns) if (c.fk) s.add(c.name);
  for (const f of links?.out ?? []) f.columns.forEach((c) => s.add(c));
  for (const f of links?.inn ?? []) f.refColumns.forEach((c) => s.add(c));
  return s;
}
