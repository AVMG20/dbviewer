// Tiny SQL -> HTML highlighter for read-only SQL blocks (previews). Uses the --syn-* tokens.
const KW = new Set(
  ("select from where and or not insert into values update set delete create alter drop table index add column rename to " +
    "primary key foreign references constraint unique default null is in between like order by group having limit offset " +
    "join left right inner outer on as distinct union all begin commit rollback if exists cascade restrict returning with " +
    "true false case when then else end asc desc explain analyze truncate view").split(" "),
);

function esc(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

export function highlightSql(sql: string): string {
  const re = /(--[^\n]*|\/\*[\s\S]*?\*\/)|('(?:[^']|'')*'?)|((?:"[^"]*"|`[^`]*`))|(\b\d+(?:\.\d+)?\b)|([A-Za-z_][A-Za-z_0-9]*)/g;
  let out = "";
  let last = 0;
  for (let m = re.exec(sql); m; m = re.exec(sql)) {
    out += esc(sql.slice(last, m.index));
    last = m.index + m[0].length;
    const t = esc(m[0]);
    if (m[1]) out += `<span style="color:var(--syn-comment)">${t}</span>`;
    else if (m[2]) out += `<span style="color:var(--syn-string)">${t}</span>`;
    else if (m[3]) out += `<span style="color:var(--syn-func)">${t}</span>`;
    else if (m[4]) out += `<span style="color:var(--syn-number)">${t}</span>`;
    else if (m[5] && KW.has(m[5].toLowerCase())) out += `<span style="color:var(--syn-keyword)">${t}</span>`;
    else out += t;
  }
  return out + esc(sql.slice(last));
}
