// Sidebar find matcher: fast contiguous substring match with word-boundary / acronym
// fallback (e.g. "so" -> "sales_order", "salord" -> "sales_order") so highlight-and-jump
// search never floods the tree with noisy mid-word subsequence hits.
import { highlightSegments, isSubsequence } from "@/components/common/fuzzy";

export type Seg = { t: string; m: boolean };

export interface NameMatch {
  segs: Seg[];
  /** true for a contiguous substring match */
  exact: boolean;
}

function isSep(c: number): boolean {
  // _ - . space / : $
  return c === 95 || c === 45 || c === 46 || c === 32 || c === 47 || c === 58 || c === 36;
}

function isWordStart(name: string, idx: number): boolean {
  if (idx === 0) return true;
  const p = name.charCodeAt(idx - 1);
  if (isSep(p)) return true;
  const c = name.charCodeAt(idx);
  return c >= 65 && c <= 90 && p >= 97 && p <= 122; // camelCase boundary
}

/**
 * Matches `nq` against `nameL` where the first char starts a word and every subsequent
 * char either continues the current run (pos[i] = pos[i-1] + 1) or jumps to a later word start.
 * Memoized on (qi, ni, canContinue) so repeated-prefix patterns stay polynomial (O(m * n²)) and fast in practice.
 */
function matchWordStarts(nq: string, name: string, nameL: string): number[] | null {
  const m = nq.length;
  const n = nameL.length;
  if (m < 2 || m > n || !isSubsequence(nq, nameL)) return null;
  const pos = new Array<number>(m);
  const dead = new Uint8Array((m + 1) * (n + 1) * 2);

  function dfs(qi: number, ni: number, canCont: boolean): boolean {
    if (qi === m) return true;
    if (ni >= n) return false;
    const state = ((qi * (n + 1) + ni) << 1) | (canCont ? 1 : 0);
    if (dead[state]) return false;

    const qc = nq.charCodeAt(qi);
    if (canCont && nameL.charCodeAt(ni) === qc) {
      pos[qi] = ni;
      if (dfs(qi + 1, ni + 1, true)) return true;
    }
    const start = canCont ? ni + 1 : ni;
    for (let j = start; j < n; j++) {
      if (nameL.charCodeAt(j) === qc && isWordStart(name, j)) {
        pos[qi] = j;
        if (dfs(qi + 1, j + 1, true)) return true;
      }
    }
    dead[state] = 1;
    return false;
  }

  return dfs(0, 0, false) ? pos : null;
}

/**
 * Matches normalized query `nq` (use normQuery) against a sidebar object name.
 * Prefers contiguous substring matches; falls back to word-boundary / acronym matches.
 */
export function matchSidebarName(nq: string, name: string, nameL: string = name.toLowerCase()): NameMatch | null {
  if (!nq) return null;
  const idx = nameL.indexOf(nq);
  if (idx >= 0) {
    const segs: Seg[] = [];
    if (idx > 0) segs.push({ t: name.slice(0, idx), m: false });
    segs.push({ t: name.slice(idx, idx + nq.length), m: true });
    if (idx + nq.length < name.length) segs.push({ t: name.slice(idx + nq.length), m: false });
    return { segs, exact: true };
  }
  const pos = matchWordStarts(nq, name, nameL);
  if (!pos) return null;
  return { segs: highlightSegments(name, pos), exact: false };
}
