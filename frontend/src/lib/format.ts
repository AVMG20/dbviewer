// Cell value formatting, parsing and comparison (data agent).
import type { CellValue, DefaultValue, ResultColumn, ValueKind } from "@/api";
import { isBinary, isLongText } from "@/api";

export interface Rendered {
  text: string;
  /** css modifier: "" | null | dim | bool-t | bool-f | json | default */
  cls: "" | "null" | "dim" | "bool-t" | "bool-f" | "json" | "default";
  /** text was cut (long text value) - show an ellipsis marker */
  truncated: boolean;
  num: boolean;
}

const NUMERIC: ValueKind[] = ["int", "float", "decimal"];
export function isNumericKind(k: string): boolean {
  return NUMERIC.includes(k as ValueKind);
}

export function isDefaultValue(v: unknown): v is DefaultValue {
  return typeof v === "object" && v !== null && (v as { $type?: string }).$type === "default";
}

export function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

const ISO_T = /^(\d{4}-\d\d-\d\d)T(\d\d:\d\d:\d\d(?:\.\d+)?)(Z|[+-]\d\d:?\d\d)?$/;

function formatDateTime(s: string): string {
  const m = ISO_T.exec(s);
  if (!m) return s;
  const time = m[2].replace(/\.0+$/, "");
  const tz = m[3] && m[3] !== "Z" ? m[3] : "";
  return `${m[1]} ${time}${tz}`;
}

const MAX_DISPLAY = 400;

/** What a cell shows in the grid. */
export function renderCell(v: CellValue | undefined, col: ResultColumn): Rendered {
  if (v === null || v === undefined) return { text: "NULL", cls: "null", truncated: false, num: false };
  if (isDefaultValue(v)) return { text: "DEFAULT", cls: "default", truncated: false, num: false };
  if (isBinary(v)) {
    return { text: `<blob ${formatSize(v.size)}>`, cls: "dim", truncated: false, num: false };
  }
  if (isLongText(v)) {
    return { text: oneLine(v.text), cls: col.kind === "json" ? "json" : "", truncated: !!v.truncated, num: false };
  }
  if (typeof v === "boolean") return { text: v ? "true" : "false", cls: v ? "bool-t" : "bool-f", truncated: false, num: false };
  if (typeof v === "number") return { text: String(v), cls: "", truncated: false, num: true };
  const kind = col.kind;
  if (kind === "datetime" || kind === "date" || kind === "time") {
    return { text: formatDateTime(v), cls: "", truncated: false, num: false };
  }
  if (kind === "json") return { text: oneLine(v), cls: "json", truncated: false, num: false };
  const num = isNumericKind(kind);
  return { text: num ? v : oneLine(v), cls: "", truncated: false, num };
}

function oneLine(s: string): string {
  if (s.length > MAX_DISPLAY) s = s.slice(0, MAX_DISPLAY);
  if (s.includes("\n") || s.includes("\r")) s = s.replace(/\s*[\r\n]+\s*/g, " ↵ ");
  return s;
}

/** Plain text of a value (editor initial text, search, copy). NULL -> "". */
export function cellText(v: CellValue | undefined): string {
  if (v === null || v === undefined) return "";
  if (isDefaultValue(v)) return "";
  if (isBinary(v)) return "0x" + v.hex;
  if (isLongText(v)) return v.text;
  if (typeof v === "boolean") return v ? "true" : "false";
  return String(v);
}

/** Text used by quick find: full (uncut) text of what the user sees; blobs are skipped. */
export function searchText(v: CellValue | undefined, col: ResultColumn): string {
  if (v === null || v === undefined) return "";
  if (isDefaultValue(v)) return "";
  if (isBinary(v)) return "";
  if (col.kind === "datetime" || col.kind === "date" || col.kind === "time") return renderCell(v, col).text;
  return cellText(v);
}

export function cellEquals(a: CellValue | undefined, b: CellValue | undefined): boolean {
  if (a === undefined) a = null;
  if (b === undefined) b = null;
  if (a === null || b === null) return a === b;
  if (isDefaultValue(a) || isDefaultValue(b)) return isDefaultValue(a) && isDefaultValue(b);
  if (isBinary(a) || isBinary(b)) return isBinary(a) && isBinary(b) && a.hex === b.hex;
  const ta = cellText(a);
  const tb = cellText(b);
  if (typeof a === "boolean" || typeof b === "boolean") return ta === tb;
  return ta === tb;
}

export const NON_TEXT: string[] = ["int", "float", "decimal", "bool", "date", "time", "datetime", "uuid"];

/** True when an empty input is not a valid value for the column (non-text, NOT NULL). */
export function isBlankInvalid(col: ResultColumn, text: string): boolean {
  return NON_TEXT.includes(col.kind) && text.trim() === "" && !col.nullable;
}

/** Converts what the user typed/pasted into the value sent to the backend. */
export function coerceInput(col: ResultColumn, text: string): CellValue {
  const kind = col.kind;
  if (NON_TEXT.includes(kind) && text.trim() === "") return col.nullable ? null : "";
  if (kind === "bool") {
    const t = text.trim().toLowerCase();
    if (["true", "t", "1", "yes", "y"].includes(t)) return true;
    if (["false", "f", "0", "no", "n"].includes(t)) return false;
    return text;
  }
  if (kind === "int" && /^-?\d+$/.test(text.trim())) {
    const n = Number(text.trim());
    if (Number.isSafeInteger(n)) return n;
  }
  return text;
}

/** True when the value is best viewed/edited in a popover instead of inline. */
export function needsPopover(v: CellValue | undefined, col: ResultColumn): boolean {
  if (isLongText(v as CellValue)) return true;
  if (col.kind === "json") return true;
  const t = cellText(v);
  return t.length > 100 || t.includes("\n");
}

/** Pretty-prints JSON text; returns null when it is not valid JSON. */
export function prettyJson(text: string): string | null {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return null;
  }
}
export function minifyJson(text: string): string | null {
  try {
    return JSON.stringify(JSON.parse(text));
  } catch {
    return null;
  }
}

// ---- text measuring (column autofit) --------------------------------------

let ctx: CanvasRenderingContext2D | null = null;
export function measureText(text: string, font = "12px ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"): number {
  if (!ctx) ctx = document.createElement("canvas").getContext("2d");
  if (!ctx) return text.length * 7.2;
  ctx.font = font;
  return ctx.measureText(text).width;
}

/** Display string for generic status text: 2000 -> "2,000". */
export function fmtNum(n: number): string {
  return n.toLocaleString("en-US");
}

// ---- input validation (inline edit, paste, record panel) -------------------------------------

const INT_BITS: [RegExp, number][] = [
  [/^tinyint/i, 8], [/^smallint|^int2/i, 16], [/^mediumint/i, 24], [/^bigint|^int8/i, 64], [/^int4\b|^int(\(|\s|$)/i, 32],
];
const NUM_RE = /^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/;

function validDate(s: string): boolean {
  const m = /^(\d{4})-(\d{1,2})-(\d{1,2})$/.exec(s);
  if (!m) return false;
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  if (mo < 1 || mo > 12 || d < 1) return false;
  return d <= new Date(y, mo, 0).getDate() || y === 0;
}
function validTime(s: string): boolean {
  const m = /^(\d{1,3}):(\d{2})(?::(\d{2})(?:\.\d+)?)?$/.exec(s);
  return !!m && Number(m[2]) < 60 && (m[3] === undefined || Number(m[3]) < 60);
}
const NOW_RE = /^(now\(\)|current_(timestamp|date|time)(\(\d*\))?|localtimestamp)$/i;

/** Enum values of a column type like enum('a','b'); null when not an enum. */
export function enumValues(type: string): string[] | null {
  const m = /^enum\((.*)\)$/i.exec(type.trim());
  if (!m) return null;
  return [...m[1].matchAll(/'((?:[^']|'')*)'/g)].map((x) => x[1].replace(/''/g, "'"));
}

/**
 * Returns an error message when `text` can't be a value of the column (null when valid or blank).
 * Lenient on format, strict on garbage. Blank text is handled separately (NULL / NOT NULL checks).
 */
export function validateInput(col: ResultColumn, text: string): string | null {
  const t = text.trim();
  if (t === "") return null;
  const ev = enumValues(col.type);
  if (ev && !ev.includes(text)) return `Must be one of: ${ev.slice(0, 8).join(", ")}${ev.length > 8 ? ", …" : ""}`;
  switch (col.kind) {
    case "int": {
      if (!/^[+-]?\d+$/.test(t)) return "Not a valid integer";
      const unsigned = /unsigned/i.test(col.type);
      const bits = INT_BITS.find(([re]) => re.test(col.type))?.[1];
      if (!bits) return null;
      const v = BigInt(t);
      const min = unsigned ? 0n : -(1n << BigInt(bits - 1));
      const max = unsigned ? (1n << BigInt(bits)) - 1n : (1n << BigInt(bits - 1)) - 1n;
      if (v < min || v > max) return `Out of range for ${col.type.replace(/\(\d+\)/, "")} (${min} to ${max})`;
      return null;
    }
    case "float":
      return NUM_RE.test(t) || /^[+-]?(inf(inity)?|nan)$/i.test(t) ? null : "Not a valid number";
    case "decimal":
      return NUM_RE.test(t) ? null : "Not a valid number";
    case "bool":
      return /^(0|1|true|false|t|f|yes|no|y|n)$/i.test(t) ? null : "Not a valid boolean (0, 1, true or false)";
    case "date":
      return validDate(t) || NOW_RE.test(t) ? null : "Not a valid date (YYYY-MM-DD)";
    case "time":
      return validTime(t) || NOW_RE.test(t) ? null : "Not a valid time (HH:MM:SS)";
    case "datetime": {
      if (NOW_RE.test(t)) return null;
      const m = /^(\d{4}-\d{1,2}-\d{1,2})(?:[ T](\d{1,2}:\d{2}(?::\d{2}(?:\.\d+)?)?)\s*(Z|[+-]\d{2}(?::?\d{2})?)?)?$/.exec(t);
      if (m && validDate(m[1]) && (m[2] === undefined || validTime(m[2]))) return null;
      return "Not a valid date/time (YYYY-MM-DD HH:MM:SS)";
    }
    case "json":
      try {
        JSON.parse(text);
        return null;
      } catch {
        return "Not valid JSON";
      }
    case "uuid":
      return /^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$/i.test(t) ? null : "Not a valid UUID";
    default:
      return null;
  }
}
