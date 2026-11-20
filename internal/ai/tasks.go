package ai

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"dbviewer/internal/model"
	"dbviewer/internal/sqltok"
)

// Prompt builders and reply extractors for the smaller AI tasks (fix, convert,
// explain, plan review, naming, table filters). Small local models follow short,
// strict instructions best, so every prompt asks for one thing in a fixed format.

const sqlBlockRule = "Reply with exactly one SQL statement in a single ```sql code block and nothing else: no explanation."

func schemaSection(schemaText string) string {
	if strings.TrimSpace(schemaText) == "" {
		return ""
	}
	return "Schema:\n" + schemaText + "\n"
}

// FixMessages asks for a corrected version of a statement that failed with dbError.
func FixMessages(driver, sql, dbError, schemaText string, now time.Time) []Message {
	sys := fmt.Sprintf(`You fix %s queries that failed. Rules:
- %s
- Fix the cause of the error and change nothing else: keep the rest of the query, its formatting and its intent.
- Use only the tables and columns listed in the schema. If a name is misspelled, use the closest listed name.
- %s`, dialectName(driver), sqlBlockRule, dialectHints(driver))
	var u strings.Builder
	u.WriteString(schemaSection(schemaText))
	fmt.Fprintf(&u, "Today is %s.\n\n", now.Format("2006-01-02 (Monday)"))
	fmt.Fprintf(&u, "This query failed:\n```sql\n%s\n```\n\nError: %s\n\nReturn the corrected query.", strings.TrimSpace(sql), truncateText(strings.TrimSpace(dbError), 800))
	return []Message{{Role: "system", Content: sys}, {Role: "user", Content: u.String()}}
}

// ConvertMessages asks to rewrite SQL written for driver `from` in the dialect `to`.
func ConvertMessages(from, to, sql string) []Message {
	sys := fmt.Sprintf(`You convert SQL from %s to %s. Rules:
- Reply with the converted SQL in a single `+"```sql"+` code block and nothing else: no explanation.
- Keep the same statements, tables, columns, aliases, logic and line layout. Change only syntax, functions, types and identifier quoting that differ in %s.
- %s`, dialectName(from), dialectName(to), dialectName(to), convertHints(to))
	u := fmt.Sprintf("Convert this %s SQL to %s:\n```sql\n%s\n```", dialectName(from), dialectName(to), strings.TrimSpace(sql))
	return []Message{{Role: "system", Content: sys}, {Role: "user", Content: u}}
}

func convertHints(to string) string {
	switch to {
	case model.DriverMySQL:
		return "MySQL: quote names with backticks only where the original quoted them; NOW(), CURDATE(), DATE_SUB(x, INTERVAL n DAY), DATE_FORMAT, IFNULL, CONCAT(), LIMIT n OFFSET m, AUTO_INCREMENT, no RETURNING, no ILIKE (use LIKE), no ::casts (use CAST)."
	case model.DriverPostgres:
		return "PostgreSQL: quote names with double quotes only where the original quoted them (never backticks); now(), current_date, x - interval '7 days', to_char, date_trunc, COALESCE, || for concatenation, ILIKE, LIMIT n OFFSET m, GENERATED ALWAYS AS IDENTITY or serial, true/false for booleans."
	case model.DriverSQLite:
		return "SQLite: quote names with double quotes only where the original quoted them (never backticks); date('now'), datetime('now', '-7 days'), strftime('%Y-%m', x), IFNULL, || for concatenation, LIMIT n OFFSET m, INTEGER PRIMARY KEY AUTOINCREMENT, no ILIKE (LIKE is case-insensitive), no YEAR() or NOW()."
	}
	return ""
}

// ExplainMessages asks for a short plain-language explanation of a query.
func ExplainMessages(driver, sql, schemaText string) []Message {
	sys := `You explain SQL queries to a developer in plain English. Rules:
- Reply with 3 to 5 short sentences in one paragraph.
- Say what the query returns (or changes), from which tables, how they are joined, which rows it filters, and how it groups and sorts.
- Be concrete: name the tables and columns, and give values and limits as written in the query.
- No SQL, no lists, no headings, no introduction like "This query".`
	var u strings.Builder
	u.WriteString(schemaSection(schemaText))
	fmt.Fprintf(&u, "Explain this %s query:\n```sql\n%s\n```", dialectName(driver), strings.TrimSpace(sql))
	return []Message{{Role: "system", Content: sys}, {Role: "user", Content: u.String()}}
}

// PlanTable is a table of an explained query with its size and indexes.
type PlanTable struct {
	Name    string
	Rows    int64 // estimated, -1 unknown
	Indexes []model.Index
	Columns []string
}

// PlanTablesText renders the tables for PlanMessages, one line each:
//
//	sales_order (~2,000 rows): indexes PRIMARY(entity_id), ix_status(status); columns entity_id, status, …
func PlanTablesText(tables []PlanTable) string {
	var b strings.Builder
	for _, t := range tables {
		b.WriteString("- " + t.Name)
		if t.Rows >= 0 {
			fmt.Fprintf(&b, " (~%s rows)", groupDigits(t.Rows))
		}
		b.WriteString(": indexes ")
		if len(t.Indexes) == 0 {
			b.WriteString("none")
		}
		for i, ix := range t.Indexes {
			if i > 0 {
				b.WriteString(", ")
			}
			name := ix.Name
			if ix.Primary {
				name = "PRIMARY"
			} else if ix.Unique {
				name += " UNIQUE"
			}
			fmt.Fprintf(&b, "%s(%s)", name, strings.Join(ix.Columns, ", "))
		}
		if len(t.Columns) > 0 {
			cols := t.Columns
			more := ""
			if len(cols) > 60 {
				more = fmt.Sprintf(", … %d more", len(cols)-60)
				cols = cols[:60]
			}
			b.WriteString("; columns used by the query: " + strings.Join(cols, ", ") + more)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 && s[i-1] != '-' {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

// Words returns the lowercase identifier-like words of a text (quotes stripped).
func Words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		out[w] = true
	}
	return out
}

// MaxPlanChars caps the EXPLAIN output sent to the model.
const MaxPlanChars = 6000

// PlanMessages asks for a short review of an EXPLAIN result with index suggestions.
func PlanMessages(driver, sql, plan, tablesText string) []Message {
	d := dialectName(driver)
	sys := fmt.Sprintf(`You review %s query plans for a developer. Be short and concrete. Reply in exactly this format:

Problems:
- one line per problem: the table and what is slow (full table scan, many rows read, filesort, temporary table, no usable index, a function on an indexed column). At most 4 lines. If the plan is fine, write one line saying so.

Indexes:
`+"```sql"+`
CREATE INDEX idx_<table>_<column> ON <table> (<column>, …);
`+"```"+`
(one statement per line, or the single line "None needed." instead of the code block)

Rules:
- Only suggest an index on columns the query filters (WHERE), joins on (the ON columns of the scanned table, e.g. orders.customer_id) or sorts on, and never when an existing index already starts with those columns. Equality columns first, then range / sort columns.
- Do not mention plan ids or node numbers.
- Every CREATE INDEX needs ON <table>. Use the exact table and column names given.
- Small tables (under ~1,000 rows) do not need indexes.
- Nothing else: no introduction, no summary.`, d)
	var u strings.Builder
	fmt.Fprintf(&u, "Query:\n```sql\n%s\n```\n\nEXPLAIN output:\n%s\n", strings.TrimSpace(sql), truncateText(strings.TrimSpace(plan), MaxPlanChars))
	if tablesText != "" {
		u.WriteString("\nTables (estimated rows, existing indexes):\n" + tablesText)
	}
	return []Message{{Role: "system", Content: sys}, {Role: "user", Content: u.String()}}
}

// NameMessages asks for a short title for a query (saved query name, tab title).
func NameMessages(sql string) []Message {
	sys := `You name SQL queries. Reply with only a short title of 2 to 5 words in sentence case that says what the query returns or does, like "Paid orders per month", "Customers without orders" or "Delete old sessions". No quotes, no SQL, no period.`
	u := "```sql\n" + truncateText(strings.TrimSpace(sql), 3000) + "\n```"
	return []Message{{Role: "system", Content: sys}, {Role: "user", Content: u}}
}

var titlePrefixRe = regexp.MustCompile(`(?i)^(title|name|query name)\s*:\s*`)

// ExtractTitle cleans a NameMessages reply: first line, no quotes / markdown /
// "Title:" prefix / trailing period, first letter upper case, at most 60 characters.
func ExtractTitle(reply string) string {
	s := StripThinking(reply)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, "*#`>-_ ")
		line = titlePrefixRe.ReplaceAllString(line, "")
		line = strings.Trim(line, "\"'“”‘’`* ")
		line = strings.TrimRight(line, ".!")
		if line == "" || strings.HasPrefix(strings.ToLower(line), "sql") && len(line) <= 4 {
			continue
		}
		if utf8.RuneCountInString(line) > 60 {
			r := []rune(line)[:60]
			line = strings.TrimSpace(string(r))
			if i := strings.LastIndexByte(line, ' '); i > 30 {
				line = line[:i]
			}
		}
		r, n := utf8.DecodeRuneInString(line)
		return string(unicode.ToUpper(r)) + line[n:]
	}
	return ""
}

// StripThinking removes <think> blocks (and a leading half one) from a reply.
func StripThinking(reply string) string {
	s := thinkRe.ReplaceAllString(reply, "")
	if i := strings.Index(s, "<think>"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	return strings.TrimSpace(s)
}

// ---- table filters (WHERE from plain language) --------------------------------------------------

// MaxDistinct: columns with more distinct values than this are not categorical.
const MaxDistinct = 30

var categoricalWords = toSet(`status state type kind category categories method mode level stage role gender
country currency channel source origin priority group tier plan segment carrier provider gateway locale language lang
visibility condition reason action event outcome result payment shipping`)

var varcharLenRe = regexp.MustCompile(`(?i)^(?:var)?char(?:acter)?(?: varying)?\s*\((\d+)\)`)

// FilterCandidates returns the text columns (at most max) whose distinct values
// the model should see: columns named in the prompt first, then status-like
// columns (status, state, type, …). Long text, JSON and enum columns (their type
// lists the values) are skipped.
func FilterCandidates(cols []model.Column, prompt string, max int) []model.Column {
	words, _ := promptWords(prompt)
	var named, typical []model.Column
	for _, c := range cols {
		if c.Kind != model.KindString || c.PK || c.FK != nil {
			continue
		}
		lt := strings.ToLower(c.Type)
		if strings.HasPrefix(lt, "enum") || strings.HasPrefix(lt, "set(") {
			continue
		}
		if m := varcharLenRe.FindStringSubmatch(lt); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > 100 {
				continue
			}
		}
		parts := nameParts(c.Name)
		hitPrompt, hitTypical := false, false
		for _, p := range parts {
			if categoricalWords[p] {
				hitTypical = true
			}
			for _, w := range words {
				if partMatch(w, p) {
					hitPrompt = true
				}
			}
		}
		// "email", "name", "sku" columns named in the prompt are not categorical
		if hitPrompt && !hitTypical && isIdentityColumn(parts) {
			hitPrompt = false
		}
		switch {
		case hitPrompt:
			named = append(named, c)
		case hitTypical:
			typical = append(typical, c)
		}
	}
	out := append(named, typical...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

var identityParts = toSet("email mail name firstname lastname sku title description desc comment note url path ip phone telephone street address token hash uuid guid password")

func isIdentityColumn(parts []string) bool {
	for _, p := range parts {
		if identityParts[p] {
			return true
		}
	}
	return false
}

// ValuesText renders the distinct values per column for FilterMessages:
//
//	status: 'canceled', 'closed', 'complete'
func ValuesText(cols []string, values map[string][]string) string {
	var b strings.Builder
	for _, c := range cols {
		vs := values[c]
		if len(vs) == 0 {
			continue
		}
		q := make([]string, len(vs))
		for i, v := range vs {
			q[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
		}
		b.WriteString(c + ": " + strings.Join(q, ", ") + "\n")
	}
	return b.String()
}

// FilterMessages asks for SELECT * FROM table WHERE … [ORDER BY …] for one table;
// ExtractFilter takes the clauses out of the reply.
func FilterMessages(driver, table, tableText, valuesText, prompt string, now time.Time) []Message {
	sys := fmt.Sprintf(`You write %s filters for one table. Rules:
- Reply with exactly one statement "SELECT * FROM %s WHERE …" in a single `+"```sql"+` code block and nothing else.
- Filter on the columns of %s only. If the request needs another table, use a subquery (column IN (SELECT …)); never JOIN.
- Write only the conditions the request asks for. Test each requested thing on ONE column: if a word matches a value listed under "Values", use that column only.
- The columns under "Values" contain only the values listed: compare with those exact values.
- Put conditions joined with OR in parentheses.
- Relative periods (last week, last 30 days): use date functions relative to now. A calendar year or month: a range like created_at >= '2024-01-01' AND created_at < '2025-01-01'.
- Add ORDER BY only when the request asks for a sort order or for the newest / largest / … rows. No LIMIT.
- %s`, dialectName(driver), table, table, dialectHints(driver))
	var u strings.Builder
	u.WriteString("Table:\n" + tableText)
	if valuesText != "" {
		u.WriteString("\nValues:\n" + valuesText)
	}
	fmt.Fprintf(&u, "\nToday is %s.\n\nRequest: %s", now.Format("2006-01-02 (Monday)"), strings.TrimSpace(prompt))
	return []Message{{Role: "system", Content: sys}, {Role: "user", Content: u.String()}}
}

// ExtractFilter takes the WHERE condition and the ORDER BY list out of a
// FilterMessages reply ("SELECT * FROM t [alias] WHERE … ORDER BY … LIMIT …").
// A bare condition (with or without the WHERE keyword) is accepted too. An alias
// of the table is removed from column references. A JOIN in the main query is an
// error: the filter can only use the table's own columns.
func ExtractFilter(dialect, reply string) (where, orderBy string, err error) {
	sql, _ := ExtractSQL(reply)
	if sql == "" {
		sql = StripThinking(reply)
		if i := strings.Index(sql, "```"); i >= 0 { // code block without SQL keywords at line start
			sql = strings.Trim(sql[i:], "`")
			sql = strings.TrimPrefix(strings.TrimPrefix(sql, "sql"), "SQL")
		}
	}
	sql = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";"))
	if sql == "" {
		return "", "", fmt.Errorf("empty reply")
	}
	toks := sqltok.Tokenize(dialect, sql).Tokens
	if len(toks) == 0 {
		return "", "", fmt.Errorf("empty reply")
	}
	// top-level keyword positions
	depth := 0
	idx := map[string]int{}
	alias := ""
	for i, t := range toks {
		if t.Kind == sqltok.Punct {
			switch t.Text {
			case "(":
				depth++
			case ")":
				depth--
			case ";":
				if depth == 0 {
					toks = toks[:i]
				}
			}
			continue
		}
		if depth != 0 || t.Kind != sqltok.Word {
			continue
		}
		switch t.Lower {
		case "join":
			return "", "", fmt.Errorf("the model joined another table; rephrase the request for this table's columns")
		case "select", "from", "where", "group", "having", "order", "limit", "offset", "fetch", "union", "window", "for":
			if _, ok := idx[t.Lower]; !ok {
				idx[t.Lower] = i
			}
		}
		if t.Lower == "from" && alias == "" && i+2 < len(toks) {
			j := i + 1
			for j+2 < len(toks) && toks[j+1].Kind == sqltok.Punct && toks[j+1].Text == "." {
				j += 2 // schema.table
			}
			k := j + 1
			if k < len(toks) && toks[k].Is("as") {
				k++
			}
			if k < len(toks) && (toks[k].Kind == sqltok.Word || toks[k].Kind == sqltok.Ident) && !clauseWord[toks[k].Lower] {
				alias = toks[k].Text
			}
		}
	}
	if len(toks) == 0 {
		return "", "", fmt.Errorf("empty reply")
	}
	end := func(start int, stops ...string) int {
		e := len(toks)
		for _, s := range stops {
			if p, ok := idx[s]; ok && p > start && p < e {
				e = p
			}
		}
		return e
	}
	clause := func(from, to int) string {
		if from >= to {
			return ""
		}
		return strings.TrimSpace(lineBreakRe.ReplaceAllString(dropAlias(sql, toks[from:to], alias), " "))
	}
	_, hasSelect := idx["select"]
	if !hasSelect {
		// a bare condition: "status = 'x' ORDER BY …" or "WHERE status = 'x'"
		start := 0
		if toks[0].Is("where") {
			start = 1
		}
		e := end(start, "order", "limit", "group", "having")
		if start == 0 && !looksLikeCondition(toks[:e]) {
			return "", "", fmt.Errorf("the model did not return a filter: %s", truncateText(sql, 200))
		}
		where = clause(start, e)
	} else if w, ok := idx["where"]; ok {
		e := end(w, "group", "having", "order", "limit", "offset", "fetch", "union", "window", "for")
		where = clause(w+1, e)
	}
	if o, ok := idx["order"]; ok && o+1 < len(toks) && toks[o+1].Is("by") {
		e := end(o, "limit", "offset", "fetch", "union", "for")
		orderBy = clause(o+2, e)
	}
	if where == "" && orderBy == "" {
		if hasSelect {
			return "", "", fmt.Errorf("the model returned no filter")
		}
		return "", "", fmt.Errorf("the model did not return a filter: %s", truncateText(sql, 200))
	}
	return where, orderBy, nil
}

// looksLikeCondition: the tokens contain a comparison (=, <, >, LIKE, IN, IS, …).
func looksLikeCondition(toks []sqltok.Token) bool {
	for _, t := range toks {
		if t.Kind == sqltok.Punct && strings.ContainsAny(t.Text, "=<>!~") {
			return true
		}
		if t.Kind == sqltok.Word && conditionWord[t.Lower] {
			return true
		}
	}
	return false
}

var conditionWord = toSet("like ilike in is between exists glob regexp rlike match")

// the clauses go into single-line inputs
var lineBreakRe = regexp.MustCompile(`[ \t]*\r?\n\s*`)

var clauseWord = toSet("where group having order limit offset fetch union window for join left right inner outer cross natural on using")

// dropAlias returns the source text of toks with "alias." prefixes removed.
func dropAlias(src string, toks []sqltok.Token, alias string) string {
	if len(toks) == 0 {
		return ""
	}
	start, stop := toks[0].Start, toks[len(toks)-1].End
	if alias == "" {
		return src[start:stop]
	}
	la := strings.ToLower(strings.Trim(alias, "`\"[]"))
	var b strings.Builder
	pos := start
	for i := 0; i+1 < len(toks); i++ {
		t := toks[i]
		if (t.Kind == sqltok.Word || t.Kind == sqltok.Ident) && t.Lower == la && toks[i+1].Kind == sqltok.Punct && toks[i+1].Text == "." {
			b.WriteString(src[pos:t.Start])
			pos = toks[i+1].End
			i++
		}
	}
	b.WriteString(src[pos:stop])
	return b.String()
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
