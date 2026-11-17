// Speed-search matcher for the sidebar tree (shell agent): ranks tables by name and by column
// names (from the schema index) and returns the matched columns of tables that only match
// through them, so the tree can list those as nested rows.
import type { Table } from "@/api";
import { fuzzy, highlightSegments, jumps } from "@/components/common/fuzzy";
import type { IndexedTable } from "./schemaCache";

export type Seg = { t: string; m: boolean };

export interface ColHit {
  name: string;
  /** index into IndexedTable.columns / types */
  i: number;
  rank: number;
  score: number;
  segs: Seg[];
}

export interface TableHit {
  table: Table;
  rank: number;
  score: number;
  /** name highlight (undefined when the table matched through its columns only) */
  segs?: Seg[];
  /** matched columns, best first; only filled for column-only matches */
  cols: ColHit[];
}

// Rank tiers: exact / prefix / substring name matches first, then strong column matches,
// then scattered name matches, then scattered column matches. Score breaks ties inside a tier.
const R_NAME_EXACT = 50;
const R_NAME_PREFIX = 40;
const R_NAME_SUB = 30;
const R_COL_EXACT = 25;
const R_COL_SUB = 20;
const R_NAME_FUZZY = 15;
const R_COL_FUZZY = 10;

/** Columns only match from 2 chars on and only fairly tight: one-letter / scattered hits would flood the tree. */
export function columnMatch(q: string, name: string, nameL: string, i = 0): ColHit | null {
  if (q.length < 2) return null;
  // short queries only match as substrings: scattered 2-3 letter hits are noise (and the DP is the cost)
  if (q.length < 4 && !nameL.includes(q)) return null;
  const h = fuzzy(q, name, nameL);
  if (!h || jumps(h.positions) > (q.length >= 6 ? 2 : 1)) return null;
  const rank = nameL === q ? R_COL_EXACT : nameL.includes(q) ? R_COL_SUB : R_COL_FUZZY;
  return { name, i, rank, score: h.score, segs: highlightSegments(name, h.positions) };
}

/** `q` must be normalized (normQuery). `lookup` returns the index entry of a table (undefined while loading). */
export function searchTables(q: string, list: readonly Table[], lookup: (name: string) => IndexedTable | undefined): TableHit[] {
  const hits: TableHit[] = [];
  // the same column names (id, created_at, store_id, …) recur in most tables: match each name once
  const memo = new Map<string, ColHit | null>();
  for (const t of list) {
    const nl = t.name.toLowerCase();
    const nh = fuzzy(q, t.name, nl);
    const nameRank = !nh ? 0 : nl === q ? R_NAME_EXACT : nl.startsWith(q) ? R_NAME_PREFIX : nl.includes(q) ? R_NAME_SUB : R_NAME_FUZZY;
    const cols: ColHit[] = [];
    // A clear name match needs no column scan (also keeps 1k-table schemas cheap per keystroke).
    if (nameRank < R_NAME_SUB) {
      const it = lookup(t.name);
      if (it) {
        for (let i = 0; i < it.columns.length; i++) {
          const name = it.columns[i];
          let c = memo.get(name);
          if (c === undefined) memo.set(name, (c = columnMatch(q, name, it.columnsL[i])));
          if (c) cols.push({ ...c, i });
        }
      }
    }
    if (cols.length) cols.sort((a, b) => b.rank - a.rank || b.score - a.score || a.i - b.i);
    const best = cols[0];
    if (nh && (!best || nameRank >= best.rank)) {
      hits.push({ table: t, rank: nameRank, score: nh.score, segs: highlightSegments(t.name, nh.positions), cols: [] });
    } else if (best) {
      hits.push({ table: t, rank: best.rank, score: best.score, cols });
    }
  }
  hits.sort((a, b) => b.rank - a.rank || b.score - a.score || a.table.name.localeCompare(b.table.name));
  return hits;
}
