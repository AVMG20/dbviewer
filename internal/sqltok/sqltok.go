// Package sqltok is a small SQL tokenizer for the three supported dialects. It is
// used to split scripts into statements and to classify statements (read-only
// checks, "does this return rows"). It does not parse SQL; it only has to agree
// with the servers on what is a comment, a string, an identifier and a keyword.
package sqltok

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Dialects.
const (
	Postgres = "postgres"
	MySQL    = "mysql"
	SQLite   = "sqlite"
)

// Kind of a token.
type Kind int

const (
	Word   Kind = iota // unquoted identifier or keyword
	Ident              // quoted identifier ("x", `x`, [x])
	String             // string literal (any quoting style)
	Number             // numeric literal
	Param              // ?, $1, :name, @var
	Punct              // operators and punctuation, one token per character (except :: and multi-char ops)
)

// Token is one lexical token.
type Token struct {
	Kind  Kind
	Text  string // raw text
	Lower string // lowercased name for Word / Ident (unquoted), "" otherwise
	Start int    // byte offset in the input
	End   int    // byte offset after the token
}

// Is reports whether t is the unquoted word w (case-insensitive).
func (t Token) Is(w string) bool { return t.Kind == Word && t.Lower == w }

// Name returns the identifier name for Word/Ident tokens ("" otherwise).
func (t Token) Name() string {
	if t.Kind == Word || t.Kind == Ident {
		return t.Lower
	}
	return ""
}

// Result of tokenizing.
type Result struct {
	Tokens []Token
	// Unterminated is set when the input ends inside a string, quoted identifier
	// or comment.
	Unterminated bool
	// HasComment is set when the input contains a comment (MySQL executable
	// comments are not comments: their content is tokenized as code).
	HasComment bool
}

// Tokenize splits sql into tokens. Comments and whitespace are dropped.
func Tokenize(dialect, sql string) Result {
	var r Result
	n := len(sql)
	execDepth := 0 // inside MySQL /*! ... */ (content is code)
	for i := 0; i < n; {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < n && sql[i+1] == '-' && (dialect != MySQL || i+2 >= n || isSpaceOrCtl(sql[i+2])):
			r.HasComment = true
			i = skipLine(sql, i)
		case c == '#' && dialect == MySQL:
			r.HasComment = true
			i = skipLine(sql, i)
		case c == '/' && i+1 < n && sql[i+1] == '*':
			if dialect == MySQL && i+2 < n && (sql[i+2] == '!' || sql[i+2] == '+') {
				// executable comment / optimizer hint: the server runs the content
				j := i + 3
				if sql[i+2] == '!' {
					for j < n && j < i+9 && sql[j] >= '0' && sql[j] <= '9' {
						j++
					}
				}
				execDepth++
				i = j
				continue
			}
			r.HasComment = true
			j, ok := skipBlockComment(sql, i, dialect == Postgres)
			if !ok {
				r.Unterminated = true
			}
			i = j
		case c == '*' && i+1 < n && sql[i+1] == '/' && execDepth > 0:
			execDepth--
			i += 2
		case c == '\'':
			backslash := dialect == MySQL
			j, ok := skipQuoted(sql, i, '\'', backslash)
			r.add(String, sql, i, j, "")
			r.Unterminated = r.Unterminated || !ok
			i = j
		case (c == 'E' || c == 'e') && dialect == Postgres && i+1 < n && sql[i+1] == '\'' && !prevIsWordChar(sql, i):
			j, ok := skipQuoted(sql, i+1, '\'', true)
			r.add(String, sql, i, j, "")
			r.Unterminated = r.Unterminated || !ok
			i = j
		case (c == 'U' || c == 'u') && dialect == Postgres && i+2 < n && sql[i+1] == '&' && (sql[i+2] == '\'' || sql[i+2] == '"') && !prevIsWordChar(sql, i):
			q := sql[i+2]
			j, ok := skipQuoted(sql, i+2, q, false)
			if q == '\'' {
				r.add(String, sql, i, j, "")
			} else {
				r.add(Ident, sql, i, j, strings.ToLower(unquote(sql[i+2:j], '"')))
			}
			r.Unterminated = r.Unterminated || !ok
			i = j
		case (c == 'X' || c == 'x' || c == 'B' || c == 'b' || c == 'N' || c == 'n') && i+1 < n && sql[i+1] == '\'' && !prevIsWordChar(sql, i):
			j, ok := skipQuoted(sql, i+1, '\'', dialect == MySQL)
			r.add(String, sql, i, j, "")
			r.Unterminated = r.Unterminated || !ok
			i = j
		case c == '"':
			if dialect == MySQL { // double-quoted string (default sql_mode)
				j, ok := skipQuoted(sql, i, '"', true)
				r.add(String, sql, i, j, "")
				r.Unterminated = r.Unterminated || !ok
				i = j
			} else {
				j, ok := skipQuoted(sql, i, '"', false)
				r.add(Ident, sql, i, j, strings.ToLower(unquote(sql[i:j], '"')))
				r.Unterminated = r.Unterminated || !ok
				i = j
			}
		case c == '`' && dialect != Postgres:
			j, ok := skipQuoted(sql, i, '`', false)
			r.add(Ident, sql, i, j, strings.ToLower(unquote(sql[i:j], '`')))
			r.Unterminated = r.Unterminated || !ok
			i = j
		case c == '[' && dialect == SQLite:
			j := strings.IndexByte(sql[i:], ']')
			if j < 0 {
				r.Unterminated = true
				j = n - i - 1
			}
			r.add(Ident, sql, i, i+j+1, strings.ToLower(sql[i+1:i+j]))
			i += j + 1
		case c == '$' && dialect == Postgres:
			if tag, ok := dollarTag(sql[i:]); ok {
				end := strings.Index(sql[i+len(tag):], tag)
				j := n
				if end >= 0 {
					j = i + len(tag) + end + len(tag)
				} else {
					r.Unterminated = true
				}
				r.add(String, sql, i, j, "")
				i = j
			} else if i+1 < n && sql[i+1] >= '0' && sql[i+1] <= '9' {
				j := i + 1
				for j < n && sql[j] >= '0' && sql[j] <= '9' {
					j++
				}
				r.add(Param, sql, i, j, "")
				i = j
			} else {
				r.add(Punct, sql, i, i+1, "")
				i++
			}
		case c == '?':
			j := i + 1
			for dialect == SQLite && j < n && sql[j] >= '0' && sql[j] <= '9' {
				j++
			}
			r.add(Param, sql, i, j, "")
			i = j
		case (c == ':' || c == '@') && i+1 < n && isWordStart(sql[i+1]) && !(c == ':' && i > 0 && sql[i-1] == ':'):
			if c == '@' && i+1 < n && sql[i+1] == '@' {
				// @@system_variable
				j := i + 2
				for j < n && (isWordByte(sql[j]) || sql[j] == '.') {
					j++
				}
				r.add(Param, sql, i, j, "")
				i = j
				continue
			}
			j := i + 1
			for j < n && isWordByte(sql[j]) {
				j++
			}
			r.add(Param, sql, i, j, "")
			i = j
		case c >= '0' && c <= '9' || (c == '.' && i+1 < n && sql[i+1] >= '0' && sql[i+1] <= '9'):
			j := i
			for j < n && (isWordByte(sql[j]) || sql[j] == '.' ||
				((sql[j] == '+' || sql[j] == '-') && j > i && (sql[j-1] == 'e' || sql[j-1] == 'E'))) {
				j++
			}
			r.add(Number, sql, i, j, "")
			i = j
		case isWordStart(c):
			j := i
			for j < n {
				if isWordByte(sql[j]) || sql[j] == '$' && dialect != MySQL && j > i {
					j++
					continue
				}
				if sql[j] >= utf8.RuneSelf {
					rr, size := utf8.DecodeRuneInString(sql[j:])
					if unicode.IsLetter(rr) || unicode.IsDigit(rr) {
						j += size
						continue
					}
				}
				break
			}
			r.add(Word, sql, i, j, strings.ToLower(sql[i:j]))
			i = j
		case c >= utf8.RuneSelf:
			rr, size := utf8.DecodeRuneInString(sql[i:])
			if unicode.IsLetter(rr) {
				j := i + size
				for j < n {
					if isWordByte(sql[j]) {
						j++
						continue
					}
					r2, s2 := utf8.DecodeRuneInString(sql[j:])
					if r2 >= utf8.RuneSelf && (unicode.IsLetter(r2) || unicode.IsDigit(r2)) {
						j += s2
						continue
					}
					break
				}
				r.add(Word, sql, i, j, strings.ToLower(sql[i:j]))
				i = j
			} else {
				r.add(Punct, sql, i, i+size, "")
				i += size
			}
		default:
			j := i + 1
			if c == ':' && j < n && sql[j] == ':' {
				j++
			}
			r.add(Punct, sql, i, j, "")
			i = j
		}
	}
	if execDepth > 0 {
		r.Unterminated = true
	}
	return r
}

func (r *Result) add(k Kind, sql string, s, e int, lower string) {
	if e > len(sql) {
		e = len(sql)
	}
	r.Tokens = append(r.Tokens, Token{Kind: k, Text: sql[s:e], Lower: lower, Start: s, End: e})
}

func isSpaceOrCtl(c byte) bool { return c <= ' ' }

func isWordStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isWordByte(c byte) bool { return isWordStart(c) || c >= '0' && c <= '9' }

func prevIsWordChar(s string, i int) bool { return i > 0 && (isWordByte(s[i-1]) || s[i-1] == '$') }

func skipLine(s string, i int) int {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(s)
}

// skipBlockComment skips /* ... */ (nested in Postgres).
func skipBlockComment(s string, i int, nested bool) (int, bool) {
	depth := 0
	n := len(s)
	for i < n {
		if s[i] == '/' && i+1 < n && s[i+1] == '*' {
			if depth == 0 || nested {
				depth++
			}
			i += 2
			continue
		}
		if s[i] == '*' && i+1 < n && s[i+1] == '/' {
			depth--
			i += 2
			if depth == 0 {
				return i, true
			}
			continue
		}
		i++
	}
	return n, false
}

// skipQuoted skips a quoted run starting at s[i] == q. Doubled quotes escape;
// with backslash=true a backslash escapes the next byte.
func skipQuoted(s string, i int, q byte, backslash bool) (int, bool) {
	n := len(s)
	i++
	for i < n {
		switch s[i] {
		case '\\':
			if backslash {
				i += 2
				continue
			}
		case q:
			if i+1 < n && s[i+1] == q {
				i += 2
				continue
			}
			return i + 1, true
		}
		i++
	}
	return n, false
}

func unquote(s string, q byte) string {
	if len(s) >= 2 && s[0] == q && s[len(s)-1] == q {
		s = s[1 : len(s)-1]
	} else if len(s) >= 1 && s[0] == q {
		s = s[1:]
	}
	return strings.ReplaceAll(s, string([]byte{q, q}), string(q))
}

func dollarTag(s string) (string, bool) {
	for j := 1; j < len(s); j++ {
		c := s[j]
		if c == '$' {
			return s[:j+1], true
		}
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80 || (j > 1 && c >= '0' && c <= '9')) {
			return "", false
		}
	}
	return "", false
}

// Range is one statement of a script.
type Range struct {
	Start, End int // byte offsets (End excludes the ';')
	Tokens     []Token
}

// Split splits a script into statements on top-level ';' tokens. Statements
// without tokens (only comments/whitespace) are dropped.
func Split(dialect, sql string) []Range {
	res := Tokenize(dialect, sql)
	var out []Range
	var cur []Token
	flush := func() {
		if len(cur) > 0 {
			out = append(out, Range{Start: cur[0].Start, End: cur[len(cur)-1].End, Tokens: cur})
		}
		cur = nil
	}
	depth := 0 // parentheses; also BEGIN ... END bodies are not split inside CREATE TRIGGER (sqlite)
	beginDepth := 0
	for i, t := range res.Tokens {
		switch {
		case t.Kind == Punct && t.Text == "(":
			depth++
		case t.Kind == Punct && t.Text == ")":
			if depth > 0 {
				depth--
			}
		case t.Is("begin") && isTriggerBody(cur):
			beginDepth++
		case t.Is("end") && beginDepth > 0 && !(i+1 < len(res.Tokens) && (res.Tokens[i+1].Is("if") || res.Tokens[i+1].Is("loop"))):
			beginDepth--
		}
		if t.Kind == Punct && t.Text == ";" && depth == 0 && beginDepth == 0 {
			flush()
			continue
		}
		cur = append(cur, t)
	}
	flush()
	return out
}

// isTriggerBody: inside CREATE [TEMP] TRIGGER (sqlite/mysql) a BEGIN ... END block
// contains ';' that do not end the statement.
func isTriggerBody(cur []Token) bool {
	if len(cur) < 2 || !cur[0].Is("create") {
		return false
	}
	for _, t := range cur[1:min(len(cur), 6)] {
		if t.Is("trigger") {
			return true
		}
	}
	return false
}
