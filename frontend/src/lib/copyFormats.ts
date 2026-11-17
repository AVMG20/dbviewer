// Copy / export text formats (TSV, CSV, JSON, SQL INSERT/UPDATE, Markdown, IN list, column names)
// built from a selection. Pure functions, no DOM.
import type { CellValue, Driver, ResultColumn } from "@/api";
import { isBinary, isLongText } from "@/api";
import type { CopyFormat } from "@/types";
import { cellText, isDefaultValue, isNumericKind } from "./format";

export type CopyFormatId = CopyFormat | "column_names" | "csv_body";

export const COPY_FORMATS: { id: CopyFormatId; label: string }[] = [
  { id: "csv", label: "CSV" },
  { id: "tsv", label: "TSV" },
  { id: "json", label: "JSON" },
  { id: "sql_insert", label: "SQL INSERT" },
  { id: "sql_update", label: "SQL UPDATE" },
  { id: "markdown", label: "Markdown table" },
  { id: "in_list", label: "IN (…) list" },
  { id: "column_names", label: "Column names" },
];

export interface CopyInput {
  /** All columns of the result (needed for PKs in UPDATE). */
  columns: ResultColumn[];
  /** Selected column indexes (ascending). */
  cols: number[];
  /** Selected rows, FULL rows (all columns). */
  rows: CellValue[][];
  /** (rowIdx within `rows`, col) -> selected? Unselected cells come out empty/NULL. */
  mask?: (r: number, c: number) => boolean;
  driver: Driver | "";
  schema: string;
  table: string;
}

export function quoteIdent(driver: Driver | "", name: string): string {
  if (driver === "mysql") return "`" + name.replace(/`/g, "``") + "`";
  return '"' + name.replace(/"/g, '""') + '"';
}

export function qualifiedTable(driver: Driver | "", schema: string, table: string): string {
  const t = quoteIdent(driver, table || "table_name");
  if (!schema || (driver === "sqlite" && schema === "main")) return t;
  return quoteIdent(driver, schema) + "." + t;
}

function hexOf(v: CellValue): string {
  return isBinary(v) ? v.hex : "";
}

const NUM_RE = /^-?\d+(\.\d+)?([eE][+-]?\d+)?$/;

export function sqlLiteral(v: CellValue | undefined, driver: Driver | "", kind = ""): string {
  if (v === null || v === undefined) return "NULL";
  if (isDefaultValue(v)) return "DEFAULT";
  if (isBinary(v)) return driver === "postgres" ? `'\\x${hexOf(v)}'::bytea` : `X'${hexOf(v)}'`;
  if (typeof v === "boolean") return driver === "postgres" ? (v ? "TRUE" : "FALSE") : v ? "1" : "0";
  if (typeof v === "number") return Number.isFinite(v) ? String(v) : "NULL";
  let s = isLongText(v) ? v.text : v;
  if (isNumericKind(kind) && NUM_RE.test(s.trim())) return s.trim();
  if (driver === "mysql") s = s.replace(/\\/g, "\\\\");
  return "'" + s.replace(/'/g, "''") + "'";
}

function cell(inp: CopyInput, r: number, c: number): CellValue | undefined {
  if (inp.mask && !inp.mask(r, c)) return undefined;
  return inp.rows[r][c];
}

function delimField(s: string, delim: string): string {
  if (s === "") return s;
  // a real string that looks like a NULL/DEFAULT token must be quoted so it round-trips as text
  const tokenLike = delim === "\t" && (s === "\\N" || s === "\\D");
  if (tokenLike || s.includes(delim) || s.includes('"') || s.includes("\n") || s.includes("\r")) return '"' + s.replace(/"/g, '""') + '"';
  return s;
}

/**
 * TSV (the default copy format) writes NULL as `\N` and DEFAULT as `\D`; Paste parses them back.
 * CSV writes NULL as an empty field (spreadsheet friendly).
 */
function field(v: CellValue | undefined, delim: string): string {
  if (v === null || v === undefined) return delim === "\t" ? "\\N" : "";
  if (isDefaultValue(v)) return delim === "\t" ? "\\D" : "";
  return delimField(cellText(v), delim);
}

function delimited(inp: CopyInput, delim: string, header: boolean): string {
  const lines: string[] = [];
  if (header) lines.push(inp.cols.map((c) => delimField(inp.columns[c].name, delim)).join(delim));
  for (let r = 0; r < inp.rows.length; r++) {
    lines.push(inp.cols.map((c) => (inp.mask && !inp.mask(r, c) ? "" : field(inp.rows[r][c], delim))).join(delim));
  }
  return lines.join("\n");
}

/** Serializes a value as JSON text: json columns are embedded, numeric columns stay numbers. */
function jsonText(v: CellValue | undefined, col: ResultColumn, indent: string): string {
  if (v === undefined || v === null || isDefaultValue(v)) return "null";
  if (isBinary(v)) return JSON.stringify("0x" + v.hex);
  const raw = isLongText(v) ? v.text : v;
  if (typeof raw === "string") {
    if (col.kind === "json") {
      try {
        return JSON.stringify(JSON.parse(raw), null, 2).replace(/\n/g, "\n" + indent);
      } catch {
        /* not valid JSON: keep as string */
      }
    }
    if (isNumericKind(col.kind) && NUM_RE.test(raw.trim())) return raw.trim();
  }
  return JSON.stringify(raw);
}

function jsonArray(inp: CopyInput): string {
  if (!inp.rows.length) return "[]";
  const rows = inp.rows.map((_, r) => {
    const parts = inp.cols
      .filter((c) => !inp.mask || inp.mask(r, c))
      .map((c) => `    ${JSON.stringify(inp.columns[c].name)}: ${jsonText(inp.rows[r][c], inp.columns[c], "    ")}`);
    return `  {\n${parts.join(",\n")}\n  }`;
  });
  return `[\n${rows.join(",\n")}\n]`;
}

function markdown(inp: CopyInput): string {
  const esc = (s: string) => s.replace(/\|/g, "\\|").replace(/\r?\n/g, "<br>");
  const head = "| " + inp.cols.map((c) => esc(inp.columns[c].name)).join(" | ") + " |";
  const sep = "| " + inp.cols.map(() => "---").join(" | ") + " |";
  const body = inp.rows.map((_, r) => {
    return "| " + inp.cols.map((c) => {
      const v = cell(inp, r, c);
      return v === null || v === undefined ? "NULL" : esc(cellText(v));
    }).join(" | ") + " |";
  });
  return [head, sep, ...body].join("\n");
}

function sqlInsert(inp: CopyInput): string {
  const tbl = qualifiedTable(inp.driver, inp.schema, inp.table);
  const out: string[] = [];
  inp.rows.forEach((_, r) => {
    const cols = inp.cols.filter((c) => !inp.mask || inp.mask(r, c));
    if (!cols.length) return;
    const names = cols.map((c) => quoteIdent(inp.driver, inp.columns[c].name)).join(", ");
    const vals = cols.map((c) => sqlLiteral(inp.rows[r][c], inp.driver, inp.columns[c].kind)).join(", ");
    out.push(`INSERT INTO ${tbl} (${names}) VALUES (${vals});`);
  });
  return out.join("\n");
}

function sqlUpdate(inp: CopyInput): string {
  const tbl = qualifiedTable(inp.driver, inp.schema, inp.table);
  const pkIdx = inp.columns.map((c, i) => (c.pk ? i : -1)).filter((i) => i >= 0);
  const keyIdx = pkIdx.length ? pkIdx : inp.columns.map((_, i) => i);
  let setCols = inp.cols.filter((c) => !inp.columns[c].pk);
  if (!setCols.length) setCols = inp.cols;
  return inp.rows
    .map((row, r) => {
      const sc = setCols.filter((c) => !inp.mask || inp.mask(r, c));
      if (!sc.length) return "";
      const set = sc.map((c) => `${quoteIdent(inp.driver, inp.columns[c].name)} = ${sqlLiteral(row[c], inp.driver, inp.columns[c].kind)}`).join(", ");
      const where = keyIdx
        .map((c) => {
          const v = row[c];
          const q = quoteIdent(inp.driver, inp.columns[c].name);
          return v === null ? `${q} IS NULL` : `${q} = ${sqlLiteral(v, inp.driver, inp.columns[c].kind)}`;
        })
        .join(" AND ");
      return `UPDATE ${tbl} SET ${set} WHERE ${where};`;
    })
    .filter(Boolean)
    .join("\n");
}

function inList(inp: CopyInput): string {
  const items: string[] = [];
  for (let r = 0; r < inp.rows.length; r++) {
    for (const c of inp.cols) {
      const v = cell(inp, r, c);
      if (v === null || v === undefined) continue;
      items.push(sqlLiteral(v, inp.driver, inp.columns[c].kind));
    }
  }
  return `(${items.join(", ")})`;
}

export function formatCopy(format: CopyFormatId, inp: CopyInput): string {
  switch (format) {
    case "tsv":
      return delimited(inp, "\t", false);
    case "tsv_header":
      return delimited(inp, "\t", true);
    case "csv":
      return delimited(inp, ",", true);
    case "csv_body":
      return delimited(inp, ",", false);
    case "json":
      return jsonArray(inp);
    case "sql_insert":
      return sqlInsert(inp);
    case "sql_update":
      return sqlUpdate(inp);
    case "markdown":
      return markdown(inp);
    case "in_list":
      return inList(inp);
    case "column_names":
      return inp.cols.map((c) => inp.columns[c].name).join(", ");
  }
}

export const FORMAT_FILE: Record<string, { ext: string; mime: string }> = {
  csv: { ext: "csv", mime: "text/csv" },
  tsv: { ext: "tsv", mime: "text/tab-separated-values" },
  json: { ext: "json", mime: "application/json" },
  sql: { ext: "sql", mime: "application/sql" },
  markdown: { ext: "md", mime: "text/markdown" },
};
