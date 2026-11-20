package ai

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"dbviewer/internal/model"
)

// Limits for the schema part of the prompt. Small local models are fast with a
// short prompt and get confused by a long one, so only the relevant tables go in.
const (
	maxTables      = 8
	maxColumns     = 40
	schemaBudget   = 14000 // characters
	allTablesBelow = 20    // schemas this small are sent whole
	// ContextChars: how much of the rest of the editor is used to find referenced tables.
	ContextChars     = 6000
	explicitMention  = 100.0
	inQueryBonus     = 80.0
	inContextBonus   = 15.0
	nameWordMatch    = 10.0
	nameSubstrMatch  = 3.0
	columnWordMatch  = 2.0
	auxiliaryPenalty = 4.0
	importanceWeight = 1.5 // per incoming foreign key (max 4)
)

var stopWords = toSet(`a an the of for with without by in on at to from and or not no me my our we i you
get give show list find fetch return select query make write all any every each per their its it them
some this that these those which where who what when how many much is are was were be been do does
than more less most least between top first last latest newest oldest recent new old only also just
please can could would should want need table tables row rows record records data value values
count number sum average avg min max sort ascending descending asc desc
there here into have has had one two three ten hundred day days week weeks month months year years today yesterday
same other`)

// Words that usually mark helper tables (indexes, replicas, temp tables).
var auxiliaryWords = toSet("tmp temp replica idx cl backup bak old index grid cache")

func toSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

var wordRe = regexp.MustCompile(`[A-Za-z0-9_]+`)

// stem reduces simple English inflections ("orders" -> "order", "categories" ->
// "category", "ordered" -> "order", "shipping" -> "shipp"). Prompt words and
// table/column names go through the same function, so odd stems still match.
func stem(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 4 && (strings.HasSuffix(w, "ses") || strings.HasSuffix(w, "xes") || strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes")):
		return w[:len(w)-2]
	case len(w) > 5 && strings.HasSuffix(w, "ing"):
		return w[:len(w)-3]
	case len(w) > 4 && strings.HasSuffix(w, "ed") && !strings.HasSuffix(w, "eed"):
		return w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us"):
		return w[:len(w)-1]
	}
	return w
}

// synonyms: prompt words that also match these name parts (both stemmed).
var synonyms = map[string][]string{
	"quantity": {"qty"}, "qty": {"quantity"},
	"address": {"addr"}, "number": {"num", "no"},
	"description": {"desc", "descr"}, "product": {"sku"},
}

// partMatch: the prompt word w names the name part p.
func partMatch(w, p string) bool {
	if p == w {
		return true
	}
	for _, s := range synonyms[w] {
		if p == s {
			return true
		}
	}
	return false
}

// promptWords returns the meaningful words of a prompt (lowercase, stemmed) and
// the raw tokens (for exact table names like sales_order).
func promptWords(prompt string) (words []string, raw map[string]bool) {
	raw = map[string]bool{}
	seen := map[string]bool{}
	for _, tok := range wordRe.FindAllString(strings.ToLower(prompt), -1) {
		raw[tok] = true
		for _, part := range strings.Split(tok, "_") {
			if len(part) < 3 || stopWords[part] {
				continue
			}
			if strings.Trim(part, "0123456789") == "" {
				continue
			}
			s := stem(part)
			if !seen[s] {
				seen[s] = true
				words = append(words, s)
			}
		}
	}
	return words, raw
}

func nameParts(name string) []string {
	parts := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '_' || r == '-' || r == ' ' || r == '.' })
	for i, p := range parts {
		parts[i] = stem(p)
	}
	return parts
}

var tableRefRe = regexp.MustCompile("(?i)\\b(?:from|join|update|into|table)\\s+((?:[`\"\\w$]+\\.)?[`\"\\w$]+)")

// ReferencedTables returns the (unquoted, lowercase) table names a SQL text refers to.
func ReferencedTables(sql string) map[string]bool {
	out := map[string]bool{}
	for _, m := range tableRefRe.FindAllStringSubmatch(sql, -1) {
		name := m[1]
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		out[strings.ToLower(strings.Trim(name, "`\""))] = true
	}
	return out
}

// TableInfo is one table with its columns.
type TableInfo struct {
	Name    string
	Kind    string
	Comment string
	Rows    int64 // estimated rows, -1 unknown
	Columns []model.Column
}

type scored struct {
	t     *TableInfo
	score float64
}

// PickTables selects the tables most relevant to the prompt: tables named in the
// prompt or in the query being changed first, then tables whose names (and to a
// lesser degree columns) match the prompt's words, then the tables those
// reference through foreign keys.
func PickTables(prompt, sql, editorContext string, tables []TableInfo) []*TableInfo {
	if len(tables) == 0 {
		return nil
	}
	if len(tables) <= allTablesBelow {
		out := make([]*TableInfo, len(tables))
		for i := range tables {
			out[i] = &tables[i]
		}
		return out
	}
	words, raw := promptWords(prompt)
	inQuery := ReferencedTables(sql)
	inContext := ReferencedTables(editorContext)
	byName := map[string]*TableInfo{}
	// main entities (sales_order, customer_entity, catalog_product_entity) are the
	// targets of many foreign keys; helper and junk tables of none
	inbound := map[string]int{}
	for i := range tables {
		for _, c := range tables[i].Columns {
			if c.FK != nil && !strings.EqualFold(c.FK.Table, tables[i].Name) {
				inbound[strings.ToLower(c.FK.Table)]++
			}
		}
	}
	var list []scored
	for i := range tables {
		t := &tables[i]
		lname := strings.ToLower(t.Name)
		byName[lname] = t
		s := 0.0
		if raw[lname] {
			s += explicitMention
		}
		if inQuery[lname] {
			s += inQueryBonus
		}
		if inContext[lname] {
			s += inContextBonus
		}
		parts := nameParts(t.Name)
		matched := 0
		for _, w := range words {
			hit := false
			for _, p := range parts {
				if partMatch(w, p) {
					hit = true
					break
				}
			}
			switch {
			case hit:
				s += nameWordMatch
				matched++
			case len(w) >= 4 && strings.Contains(lname, w):
				s += nameSubstrMatch
				matched++
			default:
				for _, c := range t.Columns {
					cp := nameParts(c.Name)
					found := false
					for _, p := range cp {
						if partMatch(w, p) {
							found = true
							break
						}
					}
					if found {
						s += columnWordMatch
						break
					}
				}
			}
		}
		if s == 0 {
			continue
		}
		// prefer the main table ("sales_order") over its helpers ("sales_order_grid",
		// "sales_order_tax_item") when they match the same words
		extra := len(parts) - matched
		if extra > 0 {
			s -= 0.75 * float64(extra)
		}
		for _, p := range parts {
			if auxiliaryWords[p] {
				s -= auxiliaryPenalty
			}
		}
		if s > 0 {
			s += importanceWeight * float64(min(inbound[lname], 4))
			if t.Rows > 0 {
				s += min(2, math.Log10(float64(t.Rows))/2)
			}
		}
		if s > 0 {
			list = append(list, scored{t, s})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return len(list[i].t.Name) < len(list[j].t.Name)
	})
	var out []*TableInfo
	picked := map[string]bool{}
	add := func(t *TableInfo) {
		if t != nil && !picked[strings.ToLower(t.Name)] && len(out) < maxTables {
			picked[strings.ToLower(t.Name)] = true
			out = append(out, t)
		}
	}
	// keep room for the tables the best matches join to
	primary := maxTables - 2
	for _, s := range list {
		if len(out) >= primary {
			break
		}
		add(s.t)
	}
	for _, t := range append([]*TableInfo(nil), out...) {
		if len(out) >= maxTables {
			break
		}
		for _, c := range t.Columns {
			if c.FK != nil {
				add(byName[strings.ToLower(c.FK.Table)])
			}
		}
	}
	for _, s := range list {
		add(s.t)
	}
	return out
}

var commonColumns = toSet("id name title email status state type code sku created_at updated_at date amount total price qty quantity")

// trimColumns keeps at most max columns: keys, foreign keys, columns matching the
// prompt and common ones first, in their original order.
func trimColumns(cols []model.Column, words []string, max int) ([]model.Column, int) {
	if len(cols) <= max {
		return cols, 0
	}
	keep := make([]bool, len(cols))
	n := 0
	mark := func(i int) {
		if !keep[i] && n < max {
			keep[i] = true
			n++
		}
	}
	for i, c := range cols {
		if c.PK || c.FK != nil {
			mark(i)
		}
	}
	for i, c := range cols {
		parts := nameParts(c.Name)
		for _, w := range words {
			for _, p := range parts {
				if partMatch(w, p) {
					mark(i)
				}
			}
		}
	}
	for i, c := range cols {
		if commonColumns[strings.ToLower(c.Name)] {
			mark(i)
		}
	}
	for i := range cols {
		mark(i)
	}
	out := make([]model.Column, 0, n)
	for i, c := range cols {
		if keep[i] {
			out = append(out, c)
		}
	}
	return out, len(cols) - len(out)
}

// SchemaText renders the tables as compact pseudo-DDL, one line per table:
//
//	sales_order(entity_id int PK, customer_id int -> customer_entity.entity_id, …)
func SchemaText(tables []*TableInfo, prompt string) (string, []string) {
	words, _ := promptWords(prompt)
	var b strings.Builder
	var names []string
	for _, t := range tables {
		cols, hidden := trimColumns(t.Columns, words, maxColumns)
		line := tableLine(t, cols, hidden)
		if b.Len()+len(line) > schemaBudget && len(names) > 0 {
			break
		}
		b.WriteString(line)
		names = append(names, t.Name)
	}
	return b.String(), names
}

// TableText renders one table with all its columns (one line, see SchemaText).
func TableText(t *TableInfo) string { return tableLine(t, t.Columns, 0) }

func tableLine(t *TableInfo, cols []model.Column, hidden int) string {
	var line strings.Builder
	line.WriteString(t.Name)
	if t.Kind == "view" || t.Kind == "materialized_view" {
		line.WriteString(" (view)")
	}
	line.WriteString("(")
	for i, c := range cols {
		if i > 0 {
			line.WriteString(", ")
		}
		line.WriteString(c.Name)
		if c.Type != "" {
			line.WriteString(" " + c.Type)
		}
		if c.PK {
			line.WriteString(" PK")
		}
		if c.FK != nil {
			line.WriteString(" -> " + c.FK.Table + "." + c.FK.Column)
		}
	}
	if hidden > 0 {
		fmt.Fprintf(&line, ", … %d more columns", hidden)
	}
	line.WriteString(")")
	if t.Comment != "" {
		line.WriteString(" -- " + strings.ReplaceAll(t.Comment, "\n", " "))
	}
	line.WriteString("\n")
	return line.String()
}

func dialectName(driver string) string {
	switch driver {
	case model.DriverMySQL:
		return "MySQL"
	case model.DriverPostgres:
		return "PostgreSQL"
	case model.DriverSQLite:
		return "SQLite"
	}
	return "SQL"
}

func dialectHints(driver string) string {
	switch driver {
	case model.DriverMySQL:
		return "Use MySQL syntax: NOW(), CURDATE(), DATE_SUB(NOW(), INTERVAL 7 DAY), DATE_FORMAT(col, '%Y-%m'), backticks only when a name needs quoting."
	case model.DriverPostgres:
		return "Use PostgreSQL syntax: now(), current_date, now() - interval '7 days', date_trunc('month', col), ILIKE for case-insensitive matching, double quotes only when a name needs quoting."
	case model.DriverSQLite:
		return "Use SQLite syntax: date('now'), date('now', '-7 days'), strftime('%Y-%m', col); there is no YEAR() or NOW()."
	}
	return ""
}

// Messages builds the chat messages for writing (sql == "") or changing a query.
func Messages(driver, prompt, sql, schemaText string, totalTables, shownTables int, now time.Time) []Message {
	d := dialectName(driver)
	sys := fmt.Sprintf(`You write %s queries for a database tool. Rules:
- Reply with exactly one SQL statement in a single `+"```sql"+` code block and nothing else: no explanation.
- Use only the tables and columns listed in the schema. Never invent a table or column.
- Join tables with explicit JOIN ... ON, using the references shown as "column -> table.column".
- Give tables short aliases and qualify every column with its alias when the query has more than one table.
- %s
- Only write UPDATE, DELETE, INSERT or schema changes when the user explicitly asks for them.`, d, dialectHints(driver))

	var u strings.Builder
	u.WriteString("Schema")
	if totalTables > shownTables {
		fmt.Fprintf(&u, " (the %d tables most relevant to the request, out of %d)", shownTables, totalTables)
	}
	u.WriteString(":\n")
	if schemaText == "" {
		u.WriteString("(no matching tables found)\n")
	} else {
		u.WriteString(schemaText)
	}
	fmt.Fprintf(&u, "\nToday is %s.\n\n", now.Format("2006-01-02 (Monday)"))
	if strings.TrimSpace(sql) == "" {
		fmt.Fprintf(&u, "Write a query for: %s", strings.TrimSpace(prompt))
	} else {
		fmt.Fprintf(&u, "Current query:\n```sql\n%s\n```\n\nChange it: %s\nReturn the complete changed query.", strings.TrimSpace(sql), strings.TrimSpace(prompt))
	}
	return []Message{{Role: "system", Content: sys}, {Role: "user", Content: u.String()}}
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

var sqlStartRe = regexp.MustCompile(`(?im)^\s*(select|with|insert|update|delete|create|alter|drop|show|explain|replace|truncate|describe|desc|pragma|set|call|grant|revoke)\b`)

// ExtractSQL pulls the SQL out of a (possibly partial) model reply. thinking is
// true while the reply is still inside a <think> block.
func ExtractSQL(reply string) (sql string, thinking bool) {
	s := thinkRe.ReplaceAllString(reply, "")
	if strings.Contains(s, "<think>") {
		return "", true
	}
	// some chat templates open the think block in the prompt: only "</think>" shows up
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		nl := strings.IndexByte(rest, '\n')
		if nl < 0 {
			return "", false // fence line still streaming
		}
		rest = rest[nl+1:]
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		} else if k := strings.LastIndex(rest, "\n`"); k >= 0 && strings.Trim(rest[k+1:], "`") == "" {
			rest = rest[:k] // closing fence half streamed
		}
		return strings.TrimSpace(rest), false
	}
	if loc := sqlStartRe.FindStringIndex(s); loc != nil {
		return strings.TrimSpace(s[loc[0]:]), false
	}
	return "", false
}
