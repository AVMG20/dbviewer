// SQL formatting for the console (sql-formatter). Imported lazily by SqlConsole (own chunk, only
// the three dialects we need). Keyword case and identifiers are kept as written.
import { formatDialect, mysql, postgresql, sqlite } from "sql-formatter";

const DIALECTS = { postgres: postgresql, mysql, sqlite } as const;

/** Formats SQL in the dialect of a driver (postgres / mysql / sqlite). Throws on unparsable input. */
export function formatSql(text: string, driver: string): string {
  const dialect = DIALECTS[driver as keyof typeof DIALECTS] ?? postgresql;
  return formatDialect(text, { dialect, tabWidth: 2, keywordCase: "preserve", linesBetweenQueries: 1 });
}

/**
 * Formats `text` that sits at column `indent.length` of a line: keeps its leading / trailing
 * whitespace and indents continuation lines by `indent`.
 */
export function formatFragment(text: string, driver: string, indent = ""): string {
  const lead = /^\s*/.exec(text)![0];
  const trail = /\s*$/.exec(text.slice(lead.length))![0];
  const body = text.slice(lead.length, text.length - trail.length);
  if (!body) return text;
  const out = formatSql(body, driver).replace(/\n(?=.)/g, "\n" + indent);
  return lead + out + trail;
}

/**
 * Maps an offset in `before` to the offset in `after` that has the same number of non-whitespace
 * characters before it (formatting only changes whitespace), so the cursor stays in its statement.
 */
export function mapOffset(before: string, after: string, pos: number): number {
  let n = 0;
  for (let i = 0; i < pos && i < before.length; i++) if (!/\s/.test(before[i])) n++;
  let i = 0;
  for (; i < after.length && n > 0; i++) if (!/\s/.test(after[i])) n--;
  // a cursor after whitespace (e.g. at the start of the next statement) stays before the next token
  if (pos > 0 && /\s/.test(before[pos - 1])) while (i < after.length && /\s/.test(after[i])) i++;
  return i;
}
