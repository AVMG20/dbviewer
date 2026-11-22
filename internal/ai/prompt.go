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

// Default limits for small local models (Ollama): fast with a short prompt.
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

// PickOptions controls how many tables and columns are sent in a prompt.
type PickOptions struct {
	MaxTables      int
	MaxColumns     int
	SchemaBudget   int
	AllTablesBelow int
	// ForceTables names tables (case-insensitive) that are always included first.
	ForceTables []string
}

// DefaultPickOptions returns compact limits tuned for small local Ollama models.
func DefaultPickOptions() PickOptions {
	return PickOptions{
		MaxTables:      maxTables,
		MaxColumns:     maxColumns,
		SchemaBudget:   schemaBudget,
		AllTablesBelow: allTablesBelow,
	}
}

// AgyPickOptions returns larger, still token-efficient limits for Antigravity CLI models.
func AgyPickOptions() PickOptions {
	return PickOptions{
		MaxTables:      20,
		MaxColumns:     50,
		SchemaBudget:   28000,
		AllTablesBelow: 40,
	}
}

func (o PickOptions) normalized() PickOptions {
	d := DefaultPickOptions()
	if o.MaxTables <= 0 {
		o.MaxTables = d.MaxTables
	}
	if o.MaxColumns <= 0 {
		o.MaxColumns = d.MaxColumns
	}
	if o.SchemaBudget <= 0 {
		o.SchemaBudget = d.SchemaBudget
	}
	if o.AllTablesBelow <= 0 {
		o.AllTablesBelow = d.AllTablesBelow
	}
	return o
}

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

// PromptKeywords returns deduplicated, meaningful lowercase words (both raw and stemmed)
// from a user prompt, skipping common SQL/English stop words. Used for dynamic metadata lookup.
func PromptKeywords(prompt string) []string {
	words, raw := promptWords(prompt)
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if len(s) < 3 || stopWords[s] || seen[s] {
			return
		}
		if strings.Trim(s, "0123456789") == "" {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	// Prefer exact raw tokens first (e.g. "showrooms", "custom_code"), then stems ("showroom").
	for _, tok := range wordRe.FindAllString(strings.ToLower(prompt), -1) {
		if raw[tok] {
			add(tok)
		}
	}
	for _, w := range words {
		add(w)
	}
	return out
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

// PickTables selects the tables most relevant to the prompt using DefaultPickOptions.
func PickTables(prompt, sql, editorContext string, tables []TableInfo) []*TableInfo {
	return PickTablesWith(prompt, sql, editorContext, tables, DefaultPickOptions())
}

// PickTablesWith selects the tables most relevant to the prompt: forced tables and
// tables named in the prompt or in the query being changed first, then tables whose
// names or columns match the prompt's words, then bidirectional FK neighbors (both
// parent and child tables, plus 2nd-hop parent references such as EAV attribute tables)
// and non-auxiliary prefix siblings (e.g. catalog_product_entity_*).
func PickTablesWith(prompt, sql, editorContext string, tables []TableInfo, opts PickOptions) []*TableInfo {
	opts = opts.normalized()
	if len(tables) == 0 {
		return nil
	}
	if len(tables) <= opts.AllTablesBelow {
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
	// targets of many foreign keys; helper and junk tables of none.
	inbound := map[string]int{}
	children := map[string][]*TableInfo{}
	childSeen := map[string]bool{}
	for i := range tables {
		t := &tables[i]
		lname := strings.ToLower(t.Name)
		byName[lname] = t
		for _, c := range t.Columns {
			if c.FK != nil && !strings.EqualFold(c.FK.Table, t.Name) {
				target := strings.ToLower(c.FK.Table)
				inbound[target]++
				edge := target + "\x00" + lname
				if !childSeen[edge] {
					childSeen[edge] = true
					children[target] = append(children[target], t)
				}
			}
		}
	}
	isAux := func(name string) bool {
		for _, p := range nameParts(name) {
			if auxiliaryWords[p] {
				return true
			}
		}
		return false
	}
	wordHitCount := func(t *TableInfo) int {
		parts := nameParts(t.Name)
		hits := 0
		for _, w := range words {
			for _, p := range parts {
				if partMatch(w, p) {
					hits++
					break
				}
			}
		}
		return hits
	}
	var list []scored
	for i := range tables {
		t := &tables[i]
		lname := strings.ToLower(t.Name)
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
	add := func(t *TableInfo) bool {
		if t != nil && !picked[strings.ToLower(t.Name)] && len(out) < opts.MaxTables {
			picked[strings.ToLower(t.Name)] = true
			out = append(out, t)
			return true
		}
		return false
	}
	// 1. Forced tables (e.g. from EAV metadata lookup or NEED_TABLES retry) always go first.
	for _, fn := range opts.ForceTables {
		add(byName[strings.ToLower(strings.TrimSpace(fn))])
	}
	// 2. Primary scored matches (keep room for FK / child / sibling expansion).
	reserve := max(2, min(8, opts.MaxTables*2/5))
	primaryLimit := max(1, opts.MaxTables-reserve)
	var primaries []*TableInfo
	for _, s := range list {
		if len(out) >= primaryLimit {
			break
		}
		if add(s.t) {
			primaries = append(primaries, s.t)
		}
	}
	// 3. Outbound FKs of already-picked tables (parent lookups).
	for _, t := range append([]*TableInfo(nil), out...) {
		if len(out) >= opts.MaxTables {
			break
		}
		for _, c := range t.Columns {
			if c.FK != nil {
				add(byName[strings.ToLower(c.FK.Table)])
			}
		}
	}
	// 4. Inbound FK children of top primary tables (e.g. catalog_product_entity_varchar ->
	// catalog_product_entity, sales_order_item -> sales_order). Prioritize prefix siblings,
	// prompt-word matches, and non-auxiliary tables; cap per parent so hubs don't flood.
	expandRoots := primaries
	if len(expandRoots) > 4 {
		expandRoots = expandRoots[:4]
	}
	var addedChildren []*TableInfo
	perParent := 3
	if opts.MaxTables >= 16 {
		perParent = 6
	}
	for _, root := range expandRoots {
		if len(out) >= opts.MaxTables {
			break
		}
		rname := strings.ToLower(root.Name)
		prefix := rname + "_"
		kids := append([]*TableInfo(nil), children[rname]...)
		if len(kids) > 0 {
			sort.SliceStable(kids, func(i, j int) bool {
				li, lj := strings.ToLower(kids[i].Name), strings.ToLower(kids[j].Name)
				ai, aj := isAux(li), isAux(lj)
				if ai != aj {
					return !ai
				}
				pi, pj := strings.HasPrefix(li, prefix), strings.HasPrefix(lj, prefix)
				if pi != pj {
					return pi
				}
				hi, hj := wordHitCount(kids[i]), wordHitCount(kids[j])
				if hi != hj {
					return hi > hj
				}
				if kids[i].Rows != kids[j].Rows {
					return kids[i].Rows > kids[j].Rows
				}
				return len(li) < len(lj)
			})
			n := 0
			for _, k := range kids {
				if n >= perParent || len(out) >= opts.MaxTables {
					break
				}
				if isAux(k.Name) && wordHitCount(k) == 0 {
					continue
				}
				// For huge hub tables (like `store`), only pull direct prefix siblings or word matches.
				if inbound[rname] > 20 && !strings.HasPrefix(strings.ToLower(k.Name), prefix) && wordHitCount(k) == 0 {
					continue
				}
				if add(k) {
					addedChildren = append(addedChildren, k)
					n++
				}
			}
		}
	}
	// 5. 2nd-hop outbound FKs from added children (e.g. catalog_product_entity_varchar.attribute_id ->
	// eav_attribute -> eav_entity_type).
	for _, ch := range addedChildren {
		if len(out) >= opts.MaxTables {
			break
		}
		for _, c := range ch.Columns {
			if c.FK != nil {
				parent := byName[strings.ToLower(c.FK.Table)]
				if add(parent) && parent != nil {
					for _, pc := range parent.Columns {
						if pc.FK != nil {
							add(byName[strings.ToLower(pc.FK.Table)])
						}
					}
				}
			}
		}
	}
	// 6. Prefix-sibling fallback for schemas without FK constraints (e.g. SQLite/MyISAM).
	if opts.MaxTables >= 12 {
		for _, root := range expandRoots {
			if len(out) >= opts.MaxTables {
				break
			}
			rname := strings.ToLower(root.Name)
			if len(children[rname]) > 0 {
				continue
			}
			prefix := rname + "_"
			var sibs []*TableInfo
			for i := range tables {
				t := &tables[i]
				lt := strings.ToLower(t.Name)
				if strings.HasPrefix(lt, prefix) && !picked[lt] && !isAux(lt) {
					sibs = append(sibs, t)
				}
			}
			sort.SliceStable(sibs, func(i, j int) bool {
				hi, hj := wordHitCount(sibs[i]), wordHitCount(sibs[j])
				if hi != hj {
					return hi > hj
				}
				if sibs[i].Rows != sibs[j].Rows {
					return sibs[i].Rows > sibs[j].Rows
				}
				return len(sibs[i].Name) < len(sibs[j].Name)
			})
			for i := 0; i < len(sibs) && i < 5 && len(out) < opts.MaxTables; i++ {
				add(sibs[i])
			}
		}
	}
	// 7. Fill remaining slots with next highest-scored tables.
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

// SchemaText renders the tables as compact pseudo-DDL using DefaultPickOptions.
func SchemaText(tables []*TableInfo, prompt string) (string, []string) {
	return SchemaTextWith(tables, prompt, DefaultPickOptions())
}

// SchemaTextWith renders the tables as compact pseudo-DDL within opts.SchemaBudget:
//
//	sales_order(entity_id int PK, customer_id int -> customer_entity.entity_id, …)
func SchemaTextWith(tables []*TableInfo, prompt string, opts PickOptions) (string, []string) {
	opts = opts.normalized()
	words, _ := promptWords(prompt)
	var b strings.Builder
	var names []string
	for _, t := range tables {
		cols, hidden := trimColumns(t.Columns, words, opts.MaxColumns)
		line := tableLine(t, cols, hidden)
		if b.Len()+len(line) > opts.SchemaBudget && len(names) > 0 {
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

// PromptExtras adds optional metadata context and self-healing table lookup to MessagesWith.
type PromptExtras struct {
	// Metadata describes dynamic schema facts looked up read-only (e.g. Magento EAV attributes).
	Metadata string
	// AllowNeedTables instructs the model to reply with "NEED_TABLES: ..." if required
	// tables are missing from a partial schema so the caller can fetch them and retry once.
	AllowNeedTables bool
}

// Messages builds the chat messages for writing (sql == "") or changing a query.
func Messages(driver, prompt, sql, schemaText string, totalTables, shownTables int, now time.Time) []Message {
	return MessagesWith(driver, prompt, sql, schemaText, totalTables, shownTables, now, PromptExtras{})
}

// MessagesWith builds chat messages with optional dynamic metadata and NEED_TABLES fallback.
func MessagesWith(driver, prompt, sql, schemaText string, totalTables, shownTables int, now time.Time, extras PromptExtras) []Message {
	d := dialectName(driver)
	var extraRule string
	if extras.AllowNeedTables && totalTables > shownTables {
		extraRule = "\n- If the query requires table(s) not listed in the schema above, do NOT invent tables or explain — reply with ONLY one line: NEED_TABLES: <comma-separated table names or prefixes>"
	}
	sys := fmt.Sprintf(`You write %s queries for a database tool. Rules:
- Reply with exactly one SQL statement in a single `+"```sql"+` code block and nothing else: no explanation.
- Use only the tables and columns listed in the schema. Never invent a table or column.
- Join tables with explicit JOIN ... ON, using the references shown as "column -> table.column".
- Give tables short aliases and qualify every column with its alias when the query has more than one table.
- %s
- Only write UPDATE, DELETE, INSERT or schema changes when the user explicitly asks for them.%s`, d, dialectHints(driver), extraRule)

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
	if meta := strings.TrimSpace(extras.Metadata); meta != "" {
		u.WriteString("\nDatabase metadata:\n" + meta + "\n")
	}
	fmt.Fprintf(&u, "\nToday is %s.\n\n", now.Format("2006-01-02 (Monday)"))
	if strings.TrimSpace(sql) == "" {
		fmt.Fprintf(&u, "Write a query for: %s", strings.TrimSpace(prompt))
	} else {
		fmt.Fprintf(&u, "Current query:\n```sql\n%s\n```\n\nChange it: %s\nReturn the complete changed query.", strings.TrimSpace(sql), strings.TrimSpace(prompt))
	}
	return []Message{{Role: "system", Content: sys}, {Role: "user", Content: u.String()}}
}

var needTablesRe = regexp.MustCompile(`(?im)^\s*NEED_TABLES\s*:\s*(.+)$`)

// ParseNeedTables extracts requested table names / prefixes from a NEED_TABLES reply.
// Returns nil if the reply contains SQL or no NEED_TABLES directive.
func ParseNeedTables(reply string) []string {
	s := StripThinking(reply)
	if strings.Contains(s, "```") {
		return nil
	}
	m := needTablesRe.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		t := strings.TrimSpace(part)
		t = strings.Trim(t, "`\"'() ")
		// strip placeholders like catalog_product_entity_<backend_type> or catalog_product_entity_*
		if i := strings.IndexAny(t, "<*%{["); i >= 0 {
			t = t[:i]
		}
		t = strings.TrimSpace(t)
		low := strings.ToLower(t)
		if len(low) < 2 || seen[low] {
			continue
		}
		seen[low] = true
		out = append(out, low)
	}
	return out
}

// ResolveRequestedTables matches model-requested table names / prefixes / keywords
// against the full schema catalog and returns concrete table names (capped at max).
func ResolveRequestedTables(terms []string, tables []TableInfo, max int) []string {
	if len(terms) == 0 || len(tables) == 0 || max <= 0 {
		return nil
	}
	byName := make(map[string]string, len(tables))
	for i := range tables {
		byName[strings.ToLower(tables[i].Name)] = tables[i].Name
	}
	var out []string
	seen := map[string]bool{}
	add := func(name string) bool {
		low := strings.ToLower(name)
		if low == "" || seen[low] || len(out) >= max {
			return false
		}
		seen[low] = true
		out = append(out, name)
		return true
	}
	isAux := func(name string) bool {
		for _, p := range nameParts(name) {
			if auxiliaryWords[p] {
				return true
			}
		}
		return false
	}
	// Pass 1: exact names
	for _, term := range terms {
		if exact, ok := byName[term]; ok {
			add(exact)
		}
	}
	// Pass 2: prefix matches (e.g. "catalog_product_entity_" -> _varchar, _int, _text, …)
	for _, term := range terms {
		if _, exact := byName[term]; exact {
			continue
		}
		added := 0
		for i := range tables {
			lt := strings.ToLower(tables[i].Name)
			if strings.HasPrefix(lt, term) && !isAux(lt) {
				if add(tables[i].Name) {
					added++
					if added >= 8 {
						break
					}
				}
			}
		}
	}
	// Pass 3: substring matches for any remaining term
	for _, term := range terms {
		if _, exact := byName[term]; exact || len(term) < 3 {
			continue
		}
		added := 0
		for i := range tables {
			lt := strings.ToLower(tables[i].Name)
			if strings.Contains(lt, term) && !isAux(lt) {
				if add(tables[i].Name) {
					added++
					if added >= 4 {
						break
					}
				}
			}
		}
	}
	return out
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
