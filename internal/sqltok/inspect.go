package sqltok

// Info describes one statement for the console: the safety checks it runs before
// executing a script (writes on production connections, UPDATE/DELETE without
// WHERE) and tab naming (first referenced table). See Inspect.
type Info struct {
	Class
	// Writes is set when the statement changes data or the schema (DML, DDL,
	// GRANT/REVOKE, CALL/DO/EXECUTE, data-modifying WITH, EXPLAIN ANALYZE of DML,
	// SELECT … INTO). Session settings and transaction control (SET, USE, BEGIN,
	// COMMIT, …) are not writes.
	Writes bool
	// Table is the first table the statement references, unquoted, as written
	// ("" when there is none, e.g. SELECT 1).
	Table string
	// NoWhere is set for an UPDATE / DELETE without WHERE (and without LIMIT or
	// JOIN): it affects every row of the table.
	NoWhere bool
	// TargetSchema / TargetTable are set for a NoWhere statement on exactly one
	// plain table (no WITH, UPDATE … FROM, DELETE … USING, ONLY, multi-table
	// syntax), so the number of affected rows is a SELECT COUNT(*) of that table.
	// Names are unquoted as the database stores them; TargetSchema may be "".
	TargetSchema, TargetTable string
}

// first keywords (Class.Main) of statements that change data or the schema
var writeMain = map[string]bool{
	"insert": true, "update": true, "delete": true, "merge": true, "upsert": true, "replace": true,
	"truncate": true, "create": true, "alter": true, "drop": true, "rename": true, "grant": true,
	"revoke": true, "comment": true, "copy": true, "load": true, "call": true, "do": true,
	"execute": true, "import": true, "refresh": true,
}

var dmlWords = map[string]bool{"insert": true, "update": true, "delete": true, "merge": true}

// words that may follow FROM / INTO / UPDATE / TABLE … before the table name
var tableNoise = map[string]bool{
	"only": true, "if": true, "not": true, "exists": true, "lateral": true, "ignore": true,
	"low_priority": true, "high_priority": true, "delayed": true, "quick": true,
}

// keywords after which the next name is a table (or view)
var tableIntro = map[string]bool{"from": true, "join": true, "into": true, "update": true, "table": true, "view": true}

// Inspect classifies the tokens of one statement and extracts what the console
// needs to know before running it.
func Inspect(dialect string, toks []Token) Info {
	in := Info{Class: Classify(dialect, toks)}
	start := 0
	for start < len(toks) && toks[start].Kind == Punct && toks[start].Text == "(" {
		start++
	}
	in.Writes = writes(in.Class, toks)
	in.Table = firstTable(toks[start:], cteNames(in.Class, toks[start:]))
	if in.Main == "update" || in.Main == "delete" {
		inspectTarget(dialect, &in, toks[start:])
	}
	return in
}

func writes(c Class, toks []Token) bool {
	if writeMain[c.Main] {
		return true
	}
	switch c.First {
	case "with": // data-modifying CTE: WITH d AS (DELETE … RETURNING *) SELECT …
		for k, t := range toks {
			if t.Kind == Word && dmlWords[t.Lower] && !(k > 0 && (toks[k-1].Is("for") || toks[k-1].Is("key"))) {
				return true
			}
		}
	case "explain": // EXPLAIN ANALYZE executes the statement
		analyze, dml := false, false
		for k, t := range toks {
			analyze = analyze || t.Is("analyze")
			dml = dml || t.Kind == Word && dmlWords[t.Lower] && !(k > 0 && (toks[k-1].Is("for") || toks[k-1].Is("key")))
		}
		return analyze && dml
	case "select": // SELECT … INTO new_table / OUTFILE
		for _, t := range toks {
			if t.Is("into") {
				return true
			}
		}
	}
	return false
}

// cteNames returns the names defined by a WITH list (lowercase).
func cteNames(c Class, toks []Token) map[string]bool {
	if c.First != "with" {
		return nil
	}
	names := map[string]bool{}
	depth := 0
	for k := 1; k < len(toks); k++ {
		t := toks[k]
		if t.Kind == Punct {
			switch t.Text {
			case "(":
				depth++
			case ")":
				depth--
			}
			continue
		}
		if depth != 0 {
			continue
		}
		prev := toks[k-1]
		if (prev.Is("with") || prev.Is("recursive") || prev.Kind == Punct && prev.Text == ",") && (t.Kind == Word || t.Kind == Ident) && !t.Is("recursive") {
			names[t.Lower] = true
		}
		if t.Kind == Word && t.Lower == c.Main {
			break
		}
	}
	return names
}

// firstTable returns the first table name after FROM / JOIN / INTO / UPDATE /
// TABLE / VIEW (top level first, then inside subqueries), skipping CTE names and
// table functions.
func firstTable(toks []Token, cte map[string]bool) string {
	depths := make([]int, len(toks))
	d := 0
	for k, t := range toks {
		if t.Kind == Punct && t.Text == "(" {
			d++
		}
		depths[k] = d
		if t.Kind == Punct && t.Text == ")" && d > 0 {
			d--
		}
	}
	first := ""
	if len(toks) > 0 {
		first = toks[0].Lower
	}
	for pass := 0; pass < 2; pass++ {
		for k, t := range toks {
			if t.Kind != Word || pass == 0 && depths[k] != 0 {
				continue
			}
			if !tableIntro[t.Lower] && !(k == 0 && (t.Lower == "truncate" || t.Lower == "describe" || t.Lower == "desc")) {
				continue
			}
			if k > 0 && t.Lower == "update" && (toks[k-1].Is("for") || toks[k-1].Is("key")) {
				continue // SELECT … FOR [NO KEY] UPDATE
			}
			if t.Lower == "table" && first == "show" && !(k > 0 && toks[k-1].Is("create")) {
				continue // SHOW TABLE STATUS
			}
			j := k + 1
			for j < len(toks) {
				if toks[j].Kind == Word && tableNoise[toks[j].Lower] {
					j++
				} else if toks[j].Is("or") && t.Lower == "update" { // UPDATE OR REPLACE (sqlite)
					j += 2
				} else {
					break
				}
			}
			name, next := qualifiedName(toks, j)
			if len(name) == 0 {
				continue
			}
			if (t.Lower == "from" || t.Lower == "join") && next < len(toks) && toks[next].Kind == Punct && toks[next].Text == "(" {
				continue // table function
			}
			last := name[len(name)-1]
			if last.Kind == Word && (cte[last.Lower] || isKeyword(last.Lower)) {
				continue
			}
			return identText(last)
		}
	}
	return ""
}

// a few keywords that may directly follow a table-introducing keyword
func isKeyword(w string) bool {
	switch w {
	case "select", "values", "set", "where", "default", "using", "on", "as", "with", "outfile", "dumpfile":
		return true
	}
	return false
}

// qualifiedName reads [a.]b[.c] starting at toks[j]; returns the name parts and
// the index after them (no parts when toks[j] is not a name).
func qualifiedName(toks []Token, j int) ([]Token, int) {
	var parts []Token
	for j < len(toks) && (toks[j].Kind == Word || toks[j].Kind == Ident) {
		parts = append(parts, toks[j])
		j++
		if j+1 < len(toks) && toks[j].Kind == Punct && toks[j].Text == "." {
			j++
			continue
		}
		break
	}
	return parts, j
}

// identText returns a name as written, without quotes.
func identText(t Token) string {
	if t.Kind != Ident || len(t.Text) < 2 {
		return t.Text
	}
	switch q := t.Text[0]; q {
	case '"', '`':
		return unquote(t.Text, q)
	case '[':
		return t.Text[1 : len(t.Text)-1]
	}
	return t.Lower // U&"…"
}

// storedName returns the name as the database stores it: unquoted words fold to
// lowercase in Postgres; quoted names are exact.
func storedName(dialect string, t Token) string {
	if t.Kind == Word && dialect == Postgres {
		return t.Lower
	}
	return identText(t)
}

// inspectTarget sets NoWhere and, for a single plain table, the target.
func inspectTarget(dialect string, in *Info, toks []Token) {
	mainIdx := 0
	if in.First == "with" {
		mainIdx = -1
		depth := 0
		for k, t := range toks {
			if t.Kind == Punct && t.Text == "(" {
				depth++
			} else if t.Kind == Punct && t.Text == ")" {
				depth--
			} else if depth == 0 && t.Is(in.Main) {
				mainIdx = k
				break
			}
		}
		if mainIdx < 0 {
			return
		}
	}
	depth := 0
	var where, limit, join, from, multi bool
	seenSet := false
	for _, t := range toks[mainIdx+1:] {
		if t.Kind == Punct {
			switch t.Text {
			case "(":
				depth++
			case ")":
				depth--
			case ",":
				// UPDATE t1, t2 SET … / DELETE t1, t2 FROM …
				if depth == 0 && !seenSet && (in.Main == "update" || !from) {
					multi = true
				}
			}
			continue
		}
		if depth != 0 || t.Kind != Word {
			continue
		}
		switch t.Lower {
		case "where":
			where = true
		case "limit":
			limit = true
		case "join":
			join = true
		case "set":
			seenSet = true
		case "from":
			if in.Main == "update" {
				multi = true // UPDATE … FROM other tables
			} else {
				from = true
			}
		case "using":
			multi = true
		}
	}
	in.NoWhere = !where && !limit && !join
	if !in.NoWhere || in.First == "with" || multi {
		return
	}
	// UPDATE [LOW_PRIORITY] [IGNORE] [OR …] t [[AS] a] SET …
	// DELETE [LOW_PRIORITY] [QUICK] [IGNORE] FROM t [[AS] a] [RETURNING …]
	j := 1
	for j < len(toks) && (toks[j].Is("low_priority") || toks[j].Is("quick") || toks[j].Is("ignore")) {
		j++
	}
	if j+1 < len(toks) && toks[j].Is("or") {
		j += 2
	}
	if in.Main == "delete" {
		if j >= len(toks) || !toks[j].Is("from") {
			return // DELETE t1 FROM … (multi-table)
		}
		j++
	}
	name, next := qualifiedName(toks, j)
	if len(name) == 0 || len(name) > 2 || name[len(name)-1].Is("only") || name[0].Is("only") {
		return
	}
	// after the name: optional alias, then SET (update) or the end / RETURNING (delete)
	rest := toks[next:]
	if len(rest) > 0 && rest[0].Is("as") {
		rest = rest[1:]
	}
	if len(rest) > 0 && (rest[0].Kind == Word || rest[0].Kind == Ident) && !rest[0].Is("set") && !rest[0].Is("returning") {
		rest = rest[1:]
	}
	if in.Main == "update" && (len(rest) == 0 || !rest[0].Is("set")) {
		return
	}
	if in.Main == "delete" && len(rest) > 0 && !rest[0].Is("returning") {
		return
	}
	in.TargetTable = storedName(dialect, name[len(name)-1])
	if len(name) == 2 {
		in.TargetSchema = storedName(dialect, name[0])
	}
}
