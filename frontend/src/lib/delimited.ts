// Parse pasted TSV / CSV text into a matrix of strings.

/** Sentinels returned for unquoted `\\N` (NULL) and `\\D` (DEFAULT) fields. */
export const PASTE_NULL = "\u0000NULL";
export const PASTE_DEFAULT = "\u0000DEFAULT";

/** Detects the delimiter (tab wins, then comma) and parses with quote support. */
export function parseDelimited(text: string): string[][] {
  text = text.replace(/\r\n?/g, "\n");
  if (text.endsWith("\n")) text = text.slice(0, -1);
  if (text === "") return [[""]];
  const firstLine = text.split("\n", 1)[0];
  const delim = text.includes("\t") ? "\t" : firstLine.includes(",") ? "," : "\n-none-";
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let i = 0;
  let quoted = false;
  let wasQuoted = false;
  const n = text.length;
  const single = delim === "\n-none-";
  const fin = () => {
    const f = !wasQuoted && field === "\\N" ? PASTE_NULL : !wasQuoted && field === "\\D" ? PASTE_DEFAULT : field;
    field = "";
    wasQuoted = false;
    return f;
  };
  while (i < n) {
    const ch = text[i];
    if (quoted) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          field += '"';
          i += 2;
          continue;
        }
        quoted = false;
        i++;
        continue;
      }
      field += ch;
      i++;
      continue;
    }
    if (ch === '"' && field === "" && !single) {
      quoted = true;
      wasQuoted = true;
      i++;
    } else if (!single && ch === delim) {
      row.push(fin());
      i++;
    } else if (ch === "\n") {
      row.push(fin());
      rows.push(row);
      row = [];
      i++;
    } else {
      field += ch;
      i++;
    }
  }
  row.push(fin());
  rows.push(row);
  return rows;
}
