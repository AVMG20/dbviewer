package ai

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"dbviewer/internal/sqltok"
)

// Inline completion ("ghost text") in the SQL console: the model continues the
// statement at the cursor. Requests run while the user types, so everything here
// is small: a few tables of schema, a short window of the editor, a few tokens of
// output. The schema part comes first and only changes when other tables become
// relevant, so Ollama's prompt cache makes repeated requests cheap.

// Limits for inline completions.
const (
	completeTables      = 6
	completeColumns     = 30
	completeSchemaChars = 3000
	// CompleteBeforeChars / CompleteAfterChars: how much of the editor around the
	// cursor the frontend sends (and the backend uses at most).
	CompleteBeforeChars = 4000
	CompleteAfterChars  = 1500
	// text of other statements around the current one given as context
	completeOtherBefore = 600
	completeOtherAfter  = 300
	// CompleteMaxTokens is num_predict: enough for a clause or a few short lines.
	CompleteMaxTokens = 56
	completeMaxLines  = 4
	completeMaxChars  = 240
)

// CursorMarker marks the cursor in chat prompts.
const CursorMarker = "<CURSOR>"

// CompletionInput is the editor text around the cursor, split into the current
// statement and the rest.
type CompletionInput struct {
	// Before / After: the current statement before and after the cursor.
	Before, After string
	// OtherBefore / OtherAfter: text of the editor before / after the statement (trimmed).
	OtherBefore, OtherAfter string
	// Names: known identifiers (lowercase table / column name -> the name), used to
	// tell a finished word from a partial one and to fix the case of a finished name.
	Names map[string]string
}

// SplitCompletion finds the statement around the cursor: before is the editor
// text before the cursor, after the text after it.
func SplitCompletion(dialect, before, after string) CompletionInput {
	if len(before) > CompleteBeforeChars {
		before = cutStart(before, len(before)-CompleteBeforeChars)
	}
	if len(after) > CompleteAfterChars {
		after = cutEnd(after, CompleteAfterChars)
	}
	in := CompletionInput{Before: before, After: after}
	toks := sqltok.Tokenize(dialect, before).Tokens
	for i := len(toks) - 1; i >= 0; i-- {
		if toks[i].Kind == sqltok.Punct && toks[i].Text == ";" {
			in.OtherBefore = before[:toks[i].End]
			in.Before = strings.TrimLeft(before[toks[i].End:], " \t\r\n")
			break
		}
	}
	for _, t := range sqltok.Tokenize(dialect, after).Tokens {
		if t.Kind == sqltok.Punct && t.Text == ";" {
			in.After = after[:t.Start]
			in.OtherAfter = after[t.Start:]
			break
		}
	}
	in.OtherBefore = strings.TrimSpace(tail(in.OtherBefore, completeOtherBefore))
	in.OtherAfter = strings.TrimSpace(head(in.OtherAfter, completeOtherAfter))
	return in
}

// cutStart drops the first n bytes (moving forward to a rune start).
func cutStart(s string, n int) string {
	for n < len(s) && !utf8.RuneStart(s[n]) {
		n++
	}
	return s[n:]
}

// cutEnd keeps the first n bytes (moving back to a rune start).
func cutEnd(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tail returns about the last n bytes of s, starting at a line start when possible.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = cutStart(s, len(s)-n)
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return s
}

// head returns about the first n bytes of s, ending at a line end when possible.
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = cutEnd(s, n)
	if i := strings.LastIndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	return s
}

// PickCompletionTables selects the few tables relevant at the cursor: the tables the
// statement references first, then tables matching the words of the statement and
// the line being typed. Most relevant first.
func PickCompletionTables(in CompletionInput, tables []TableInfo) []*TableInfo {
	stmt := in.Before + in.After
	words := stmt
	if line := lastLine(in.Before); line != "" {
		words += " " + line
	}
	picked := PickTables(words, stmt, in.OtherBefore+"\n"+in.OtherAfter, tables)
	// tables named in the statement always make it; the rest by relevance
	refs := ReferencedTables(stmt)
	var out []*TableInfo
	for _, t := range picked {
		if refs[strings.ToLower(t.Name)] && len(out) < completeTables {
			out = append(out, t)
		}
	}
	for _, t := range picked {
		if !refs[strings.ToLower(t.Name)] && len(out) < completeTables {
			out = append(out, t)
		}
	}
	return out
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// CompletionSchemaText renders the picked tables (most relevant first) compactly
// (see SchemaText) within the small inline-completion budget. The lines are sorted
// by table name, so the schema text (and with it the prompt prefix Ollama caches)
// stays the same while the user types in one statement.
func CompletionSchemaText(tables []*TableInfo, in CompletionInput) (string, []string) {
	type entry struct{ name, line string }
	var list []entry
	size := 0
	// columns of big tables: keys, the ones the statement names, common ones. The word
	// being typed is left out: the schema then only changes when a word is finished.
	stmt := strings.TrimRightFunc(in.Before, func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }) + " " + in.After
	words, _ := promptWords(stmt)
	for _, t := range tables {
		cols, hidden := trimColumns(t.Columns, words, completeColumns)
		line := tableLine(t, cols, hidden)
		if size+len(line) > completeSchemaChars && len(list) > 0 {
			continue
		}
		size += len(line)
		list = append(list, entry{t.Name, line})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
	var b strings.Builder
	names := make([]string, len(list))
	for i, e := range list {
		b.WriteString(e.line)
		names[i] = e.name
	}
	return b.String(), names
}

// CompleteMessages builds the chat prompt for instruct models: the schema and the
// editor with the cursor marked; the reply is the text to insert at the cursor.
// The system message and the schema come first (stable prefix for the prompt cache).
func CompleteMessages(driver, schemaText string, in CompletionInput) []Message {
	d := dialectName(driver)
	sys := fmt.Sprintf(`You are an autocomplete engine in a %s editor. You get the schema and the editor text with the cursor marked as %s.
Reply with only the text to insert at the cursor. Rules:
- Finish the current word, clause or line; at most 3 short lines.
- Never repeat text that is before or after the cursor.
- Use only the tables and columns of the schema; join on the references shown as "column -> table.column".
- No code fences, no comments, no explanation. Reply with nothing if nothing fits.`, d, CursorMarker)
	var u strings.Builder
	u.WriteString(schemaSection(schemaText))
	u.WriteString("\nEditor:\n")
	if in.OtherBefore != "" {
		u.WriteString(in.OtherBefore + "\n\n")
	}
	u.WriteString(in.Before + CursorMarker + in.After)
	if in.OtherAfter != "" {
		u.WriteString("\n" + in.OtherAfter)
	}
	return []Message{{Role: "system", Content: sys}, {Role: "user", Content: u.String()}}
}

// FIMPrompt builds prefix and suffix for fill-in-the-middle models: the schema as a
// SQL comment block at the top of the prefix, then the editor text.
func FIMPrompt(driver, schemaText string, in CompletionInput) (prefix, suffix string) {
	var b strings.Builder
	fmt.Fprintf(&b, "-- %s database\n", dialectName(driver))
	if schemaText != "" {
		b.WriteString("-- Tables:\n")
		for _, line := range strings.Split(strings.TrimRight(schemaText, "\n"), "\n") {
			b.WriteString("-- " + line + "\n")
		}
	}
	b.WriteString("\n")
	if in.OtherBefore != "" {
		b.WriteString(in.OtherBefore + "\n\n")
	}
	b.WriteString(in.Before)
	suffix = in.After
	if in.OtherAfter != "" {
		suffix += "\n" + in.OtherAfter
	}
	return b.String(), suffix
}

var (
	fenceLineRe   = regexp.MustCompile("(?m)^\\s*```[a-zA-Z]*\\s*$\n?")
	leadingWordRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
	clauseWords   = toSet("select from where join left right inner outer cross on and or order group by having limit offset as set values union into not in is null like between case when then else end asc desc distinct using returning")
)

// CleanCompletion turns a model reply into the text to show after the cursor, or ""
// when there is nothing useful. See CompletionText.
func CleanCompletion(reply string, in CompletionInput) string {
	s, _ := completionText(reply, in, true)
	return s
}

// CompletionText cleans a (possibly partial) model reply: strips thinking, code
// fences and the cursor marker, removes text the model repeated from before the
// cursor, and keeps one step: the rest of the line when the cursor line has text
// (up to 3 lines when it is empty), each line up to the next condition or clause
// (" AND …", " ORDER BY …"), up to the end of the statement, and not what already
// follows the cursor. complete reports that the reply went past that point, so a
// streaming request can stop. reply may be partial: a word at its end is taken as
// still being written.
func CompletionText(reply string, in CompletionInput) (text string, complete bool) {
	return completionText(reply, in, false)
}

func completionText(reply string, in CompletionInput, final bool) (text string, complete bool) {
	s := StripThinkingRaw(reply)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = fenceLineRe.ReplaceAllString(s, "")
	s = strings.Trim(s, "`")
	s = strings.ReplaceAll(s, CursorMarker, "")
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	before := in.Before
	// the model is still copying the statement: wait for the rest
	if echo := strings.TrimSpace(s); len(echo) < len(strings.TrimSpace(before)) && strings.HasPrefix(normSpace(strings.TrimSpace(before)), normSpace(echo)) {
		return "", false
	}
	s = stripRepeatedBefore(s, before)
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	lineHasText := strings.TrimSpace(lastLine(before)) != ""
	// a reply that starts on a new line while the cursor line is empty: keep it on this line
	if !lineHasText {
		s = strings.TrimLeft(s, "\n")
	}
	// a separating space the model left out ("FROM orders" + "WHERE …"), but not
	// when the reply finishes the word ("WHE" + "RE", "o.cust" + "omer_id")
	if before != "" && isIdentByte(before[len(before)-1]) && s != "" && isIdentByte(s[0]) {
		lw := strings.ToLower(lastIdent(before))
		fw := strings.ToLower(leadingWordRe.FindString(s))
		joined := lw + fw
		name, known := in.Names[joined]
		switch {
		case known && len(name) == len(joined) && strings.EqualFold(name[:len(lw)], lastIdent(before)):
			// the model finished a name in another case ("cre" + "ATED_AT"): use the name's
			s = name[len(lw):] + s[len(fw):]
		case !clauseWords[joined] && !known && (clauseWords[fw] || clauseWords[lw]):
			s = " " + s
		}
	}
	// a cursor after a space: no second one
	if strings.HasSuffix(before, " ") || strings.HasSuffix(before, "\t") {
		s = strings.TrimLeft(s, " \t")
	}
	// the end of the statement; keep the ";" only when the statement has none yet
	if i := statementEnd(s); i >= 0 {
		complete = true
		if strings.TrimSpace(in.After) == "" && in.OtherAfter == "" {
			s = s[:i+1]
		} else {
			s = s[:i]
		}
	}
	// lines: the rest of the cursor line, or a few lines on an empty one
	lines := strings.Split(s, "\n")
	maxLines := completeMaxLines
	if lineHasText {
		maxLines = 1
	}
	for i, l := range lines {
		if i > 0 && strings.TrimSpace(l) == "" { // blank line
			lines, complete = lines[:i], true
			break
		}
		if i >= maxLines {
			lines, complete = lines[:i], true
			break
		}
		if cut := clauseCut(l, final || i < len(lines)-1); cut >= 0 {
			lines, complete = append(lines[:i:i], l[:cut]), true
			break
		}
	}
	s = strings.Join(lines, "\n")
	s = stripRepeatedAfter(s, in.After)
	if len(s) > completeMaxChars {
		complete = true
		s = cutEnd(s, completeMaxChars)
		if i := strings.LastIndexAny(s, " \n,"); i > len(s)/2 {
			s = s[:i]
		}
	}
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	if strings.TrimSpace(s) == "" {
		return "", complete
	}
	// nothing new: the reply only repeats what follows the cursor
	if a := strings.TrimSpace(in.After); a != "" && strings.HasPrefix(normSpace(a), normSpace(s)) {
		return "", complete
	}
	return s, complete
}

// Words that start the next step of a statement: a suggestion stops before them.
var (
	condWords   = toSet("and or")
	clauseStart = toSet("where order group having limit offset join left right inner cross full union except intersect returning window")
)

// clauseCut returns where line should be cut: before the first top-level AND / OR
// or clause keyword that follows some text of the suggestion, -1 if nowhere.
// ended: the line is complete (a word at its end is finished).
func clauseCut(line string, ended bool) int {
	depth, cases := 0, 0
	var q byte
	between := false
	prev := ""
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
			continue
		case c == '\'' || c == '"' || c == '`':
			q = c
			continue
		case c == '(':
			depth++
			continue
		case c == ')':
			depth--
			continue
		}
		if !isIdentByte(c) || (i > 0 && isIdentByte(line[i-1])) {
			continue
		}
		j := i
		for j < len(line) && isIdentByte(line[j]) {
			j++
		}
		w := strings.ToLower(line[i:j])
		if (i > 0 && line[i-1] == '.') || (j == len(line) && !ended) {
			// a qualified name (o.order_id), or a word still being written: not a keyword
			prev = w
			i = j - 1
			continue
		}
		switch {
		case w == "between":
			between = true
		case w == "case":
			cases++
		case w == "end" && cases > 0:
			cases--
		case w == "and" && between:
			between = false
		case depth <= 0 && cases == 0 && (condWords[w] || clauseStart[w]) && !joinModifier[prev] && strings.TrimSpace(line[:i]) != "" &&
			!strings.HasPrefix(strings.TrimLeft(line[j:], " "), "("): // LEFT(…) is a function
			return len(strings.TrimRight(line[:i], " \t"))
		}
		prev = w
		i = j - 1
	}
	return -1
}

// words before JOIN that belong to it
var joinModifier = toSet("left right inner outer cross full natural")

// StripThinkingRaw removes <think> blocks like StripThinking but keeps the
// whitespace of the reply (a leading space or line break matters here).
func StripThinkingRaw(reply string) string {
	s := reply
	if loc := thinkRe.FindAllStringIndex(s, -1); len(loc) > 0 {
		s = strings.TrimLeft(s[loc[len(loc)-1][1]:], "\n")
	}
	if i := strings.Index(s, "<think>"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		s = strings.TrimLeft(s[i+len("</think>"):], "\n")
	}
	return s
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func lastIdent(s string) string {
	i := len(s)
	for i > 0 && isIdentByte(s[i-1]) {
		i--
	}
	return s[i:]
}

// stripRepeatedBefore removes the part of the reply the model copied from before
// the cursor: the whole statement, the current line, or an overlap of at least a
// word ("… WHERE status = " + "status = 'paid'").
func stripRepeatedBefore(s, before string) string {
	trimmed := strings.TrimLeft(s, " \t\n")
	for _, rep := range []string{strings.TrimSpace(before), strings.TrimSpace(lastLine(before))} {
		if len(rep) >= 3 && len(trimmed) >= len(rep) && strings.EqualFold(trimmed[:len(rep)], rep) {
			return trimmed[len(rep):]
		}
	}
	maxK := min(len(s), len(before), 200)
	for k := maxK; k >= 3; k-- {
		if !strings.EqualFold(before[len(before)-k:], s[:k]) {
			continue
		}
		// whole words only: the overlap starts at a word start in before
		start := len(before) - k
		if start > 0 && isIdentByte(before[start-1]) && isIdentByte(before[start]) {
			continue
		}
		if strings.TrimSpace(s[:k]) == "" || !strings.ContainsFunc(s[:k], unicode.IsLetter) {
			continue
		}
		return s[k:]
	}
	return s
}

// stripRepeatedAfter cuts the reply where it starts repeating the text after the
// cursor (closing brackets, a FROM line that is already there).
func stripRepeatedAfter(s, after string) string {
	a := strings.TrimLeft(after, " \t")
	if a == "" {
		return s
	}
	// trailing overlap with the start of after: "…id)" + ")"
	for k := min(len(s), len(a)); k >= 1; k-- {
		if strings.HasSuffix(s, a[:k]) && strings.TrimSpace(a[:k]) != "" {
			if k >= 2 || strings.ContainsAny(a[:k], ")]};'\"`") {
				s = s[:len(s)-k]
				break
			}
		}
	}
	// a line of the reply equal to the next non-empty line after the cursor
	var next string
	for _, l := range strings.Split(a, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			next = normSpace(t)
			break
		}
	}
	if next != "" && len(next) >= 4 {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			if i > 0 && normSpace(strings.TrimSpace(l)) == next {
				return strings.Join(lines[:i], "\n")
			}
		}
		if i := strings.Index(normSpace(s), next); i > 0 && strings.Contains(s, "\n") {
			// the repeated text starts inside a line: cut at that line
			lines := strings.Split(s, "\n")
			for j := 1; j < len(lines); j++ {
				if strings.HasPrefix(normSpace(strings.TrimSpace(lines[j])), next[:min(len(next), 8)]) {
					return strings.Join(lines[:j], "\n")
				}
			}
		}
	}
	return s
}

// statementEnd returns the index of the first ";" outside quotes, -1 if none.
func statementEnd(s string) int {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '\'' || c == '"' || c == '`':
			q = c
		case c == ';':
			return i
		}
	}
	return -1
}

var spaceRe = regexp.MustCompile(`\s+`)

func normSpace(s string) string { return strings.ToLower(spaceRe.ReplaceAllString(s, " ")) }
