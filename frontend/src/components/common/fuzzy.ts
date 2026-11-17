// Small fuzzy matcher (shell agent): subsequence match with boundary / consecutive
// bonuses and gap penalties, returns matched positions for highlighting.
// Smart-case-less (always case-insensitive). Fast enough for ~10k candidates per keystroke.

export interface FuzzyHit {
  score: number;
  positions: number[];
}

const BASE = 16;
const BONUS_CONSEC = 10;
const BONUS_BOUNDARY = 12;
const BONUS_FIRST = 8;
const BONUS_CAMEL = 6;

function isSep(c: number): boolean {
  // _ - . space / : $
  return c === 95 || c === 45 || c === 46 || c === 32 || c === 47 || c === 58 || c === 36;
}

/** Normalizes a query: lowercase, whitespace removed. */
export function normQuery(q: string): string {
  return q.toLowerCase().replace(/\s+/g, "");
}

/** Cheap subsequence test (no scoring). */
export function isSubsequence(q: string, tl: string): boolean {
  let j = 0;
  for (let i = 0; i < tl.length && j < q.length; i++) if (tl.charCodeAt(i) === q.charCodeAt(j)) j++;
  return j === q.length;
}

/**
 * Matches normalized query `q` (use normQuery) against `text`. `tl` is text.toLowerCase()
 * (pass it in when it is precomputed). Returns null when there is no match.
 */
export function fuzzy(q: string, text: string, tl: string = text.toLowerCase()): FuzzyHit | null {
  const m = q.length;
  const n = tl.length;
  if (!m) return { score: 0, positions: [] };
  if (m > n || !isSubsequence(q, tl)) return null;

  // Fast path: contiguous substring match gets a strong score.
  // (DP below would find it anyway, but this skips the matrix for the common case.)
  const NEG = -1e9;
  const bonus = new Int16Array(n);
  for (let j = 0; j < n; j++) {
    const c = text.charCodeAt(j);
    if (j === 0) bonus[j] = BONUS_FIRST + BONUS_BOUNDARY;
    else {
      const p = text.charCodeAt(j - 1);
      if (isSep(p)) bonus[j] = BONUS_BOUNDARY;
      else if (c >= 65 && c <= 90 && p >= 97 && p <= 122) bonus[j] = BONUS_CAMEL;
    }
  }
  const dp: Int32Array[] = [];
  const back: Int16Array[] = [];
  for (let i = 0; i < m; i++) {
    const row = new Int32Array(n).fill(NEG);
    const bk = new Int16Array(n).fill(-1);
    const qc = q.charCodeAt(i);
    if (i === 0) {
      for (let j = 0; j < n; j++) if (tl.charCodeAt(j) === qc) row[j] = BASE + bonus[j] - Math.min(j, 4);
    } else {
      const prev = dp[i - 1];
      let runBest = NEG; // max over k < j-1 of prev[k] + k
      let runIdx = -1;
      for (let j = i; j < n; j++) {
        // include k = j-2 into running max
        const k = j - 2;
        if (k >= 0 && prev[k] > NEG / 2 && prev[k] + k > runBest) {
          runBest = prev[k] + k;
          runIdx = k;
        }
        if (tl.charCodeAt(j) !== qc) continue;
        let best = NEG;
        let from = -1;
        const pc = prev[j - 1];
        if (pc > NEG / 2) {
          best = pc + BONUS_CONSEC;
          from = j - 1;
        }
        if (runIdx >= 0) {
          const v = runBest - (j - 1);
          if (v > best) {
            best = v;
            from = runIdx;
          }
        }
        if (from >= 0) {
          row[j] = best + BASE + bonus[j];
          bk[j] = from;
        }
      }
    }
    dp.push(row);
    back.push(bk);
  }
  const last = dp[m - 1];
  let bj = -1;
  let bs = NEG;
  for (let j = m - 1; j < n; j++) {
    // prefer shorter remaining tails slightly
    const s = last[j] - (n - 1 - j) * 0.05;
    if (last[j] > NEG / 2 && s > bs) {
      bs = s;
      bj = j;
    }
  }
  if (bj < 0) return null;
  const positions = new Array<number>(m);
  let j = bj;
  for (let i = m - 1; i >= 0; i--) {
    positions[i] = j;
    j = back[i][j];
  }
  // Slight preference for shorter strings.
  return { score: Math.round(last[bj] - n * 0.2), positions };
}

/** Splits text into [chunk, matched] segments for rendering highlights. */
export function highlightSegments(text: string, positions: number[]): { t: string; m: boolean }[] {
  if (!positions.length) return [{ t: text, m: false }];
  const out: { t: string; m: boolean }[] = [];
  const set = new Set(positions);
  let cur = "";
  let curM = false;
  for (let i = 0; i < text.length; i++) {
    const m = set.has(i);
    if (i > 0 && m !== curM) {
      out.push({ t: cur, m: curM });
      cur = "";
    }
    cur += text[i];
    curM = m;
  }
  if (cur) out.push({ t: cur, m: curM });
  return out;
}

export interface TableMatch {
  score: number;
  segs: { t: string; m: boolean }[];
  /** best matching column (when it beats the name match) */
  hint?: { t: string; m: boolean }[];
  column?: string;
}

/** Number of gaps between matched runs (0 = one contiguous run). */
export function jumps(pos: number[]): number {
  let j = 0;
  for (let i = 1; i < pos.length; i++) if (pos[i] !== pos[i - 1] + 1) j++;
  return j;
}

/**
 * Scores a table for speed search / palette: name match, or column match shown as a hint.
 * A tight name match wins outright; a scattered name match competes with column matches.
 */
export function matchTable(
  q: string,
  t: { name: string; nameL: string; columns: string[]; columnsL: string[] },
): TableMatch | null {
  const nh = fuzzy(q, t.name, t.nameL);
  if (nh && jumps(nh.positions) <= 1) return { score: nh.score + 15, segs: highlightSegments(t.name, nh.positions) };
  let best: { i: number; score: number; pos: number[] } | null = null;
  for (let i = 0; i < t.columns.length; i++) {
    const h = fuzzy(q, t.columns[i], t.columnsL[i]);
    if (h && (!best || h.score > best.score)) best = { i, score: h.score, pos: h.positions };
  }
  if (nh && (!best || nh.score + 15 >= best.score)) return { score: nh.score + 15, segs: highlightSegments(t.name, nh.positions) };
  if (!best) return null;
  return {
    score: best.score,
    segs: [{ t: t.name, m: false }],
    hint: highlightSegments(t.columns[best.i], best.pos),
    column: t.columns[best.i],
  };
}
