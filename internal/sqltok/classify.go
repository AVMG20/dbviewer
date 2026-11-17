package sqltok

import "strings"

// Class describes one statement.
type Class struct {
	First       string // first keyword (lowercase), after leading '('
	Main        string // WITH: the keyword of the main statement after the CTE list; otherwise First
	ReturnsRows bool   // run with Query (result set expected)
	// ReadOnly is "" when the statement may run on a read-only connection,
	// otherwise a user-facing reason why it is refused.
	ReadOnly string
}

var rowFirst = map[string]bool{
	"select": true, "show": true, "explain": true, "describe": true, "desc": true, "pragma": true,
	"values": true, "table": true, "call": true, "fetch": true, "execute": true, "analyze": true,
	"check": true, "optimize": true, "repair": true, "checksum": true, "handler": true, "help": true,
}

// read statements allowed on read-only connections (by first keyword)
var readFirst = map[string]bool{
	"select": true, "with": true, "show": true, "describe": true, "desc": true, "explain": true,
	"values": true, "table": true,
}

// Keywords that must not appear anywhere in a read-only statement (outside
// strings, comments and quoted identifiers): DML, DDL, transaction control,
// SET/RESET, CALL/DO/COPY/LOAD/HANDLER/LOCK, SELECT ... INTO and server
// administration. Columns with these names must be quoted on read-only connections.
var writeWords = map[string]bool{
	"insert": true, "update": true, "delete": true, "merge": true, "upsert": true, "replace": true,
	"create": true, "alter": true, "drop": true, "truncate": true, "rename": true, "grant": true, "revoke": true,
	"set": true, "reset": true, "call": true, "do": true, "copy": true, "handler": true, "lock": true, "unlock": true,
	"begin": true, "commit": true, "rollback": true, "savepoint": true, "prepare": true, "execute": true,
	"deallocate": true, "discard": true, "listen": true, "notify": true, "unlisten": true, "vacuum": true,
	"analyze": true, "cluster": true, "reindex": true, "refresh": true, "into": true, "outfile": true,
	"dumpfile": true, "kill": true, "shutdown": true, "flush": true, "purge": true, "install": true,
	"uninstall": true, "optimize": true, "repair": true, "attach": true, "detach": true, "set_var": true,
	"persist": true, "persist_only": true, "xa": true, "binlog": true,
}

// isWriteWord reports whether toks[k] is a write keyword in context.
func isWriteWord(toks []Token, k int) bool {
	t := toks[k]
	if t.Kind != Word {
		return false
	}
	next := func() Token {
		if k+1 < len(toks) {
			return toks[k+1]
		}
		return Token{}
	}
	switch t.Lower {
	case "start": // START TRANSACTION
		return next().Is("transaction")
	case "release": // RELEASE SAVEPOINT
		return next().Is("savepoint")
	case "load": // LOAD DATA / LOAD XML / LOAD INDEX / LOAD 'library'
		n := next()
		return n.Is("data") || n.Is("xml") || n.Is("index") || n.Kind == String
	case "set":
		// CHARACTER SET / CHAR SET inside CAST/CONVERT is a read
		return !(k > 0 && (toks[k-1].Is("character") || toks[k-1].Is("char")))
	case "replace": // REPLACE() string function
		return !(next().Kind == Punct && next().Text == "(")
	}
	return writeWords[t.Lower]
}

// Functions that change state (session settings, locks, sequences, files,
// other databases) or execute SQL given as text.
var writeFuncs = map[string]bool{
	"set_config": true, "pg_terminate_backend": true, "pg_cancel_backend": true, "pg_reload_conf": true,
	"pg_rotate_logfile": true, "pg_switch_wal": true, "pg_create_restore_point": true, "pg_promote": true,
	"nextval": true, "setval": true, "pg_file_write": true, "pg_read_file": true, "pg_read_binary_file": true,
	"pg_ls_dir": true, "pg_stat_reset": true, "pg_stat_statements_reset": true, "query_to_xml": true,
	"query_to_xmlschema": true, "query_to_xml_and_xmlschema": true, "cursor_to_xml": true,
	"pg_import_system_collations": true, "txid_current": true, "pg_current_xact_id": true,
	"get_lock": true, "release_lock": true, "release_all_locks": true, "load_file": true,
	"sys_exec": true, "sys_eval": true, "load_extension": true, "writefile": true, "readfile": true,
	"edit": true, "fts3_tokenizer": true,
}

var writeFuncPrefixes = []string{"pg_advisory", "pg_try_advisory", "lo_", "dblink", "pg_logical_", "pg_replication_",
	"pg_create_", "pg_drop_", "pg_copy_", "pg_write_", "pg_stat_reset", "pg_wal_replay"}

// read-only PRAGMAs (SQLite)
var readPragmas = map[string]bool{
	"table_info": true, "table_xinfo": true, "table_list": true, "index_list": true, "index_info": true,
	"index_xinfo": true, "foreign_key_list": true, "foreign_key_check": true, "database_list": true,
	"collation_list": true, "function_list": true, "module_list": true, "pragma_list": true,
	"compile_options": true, "integrity_check": true, "quick_check": true, "page_count": true,
	"page_size": true, "freelist_count": true, "encoding": true, "user_version": true,
	"schema_version": true, "application_id": true, "data_version": true, "journal_mode": true,
	"foreign_keys": true, "query_only": true, "max_page_count": true, "auto_vacuum": true,
}

// pragmas whose (argument) form sets a value
var settablePragmas = map[string]bool{
	"user_version": true, "application_id": true, "journal_mode": true, "foreign_keys": true,
	"query_only": true, "page_size": true, "schema_version": true, "max_page_count": true, "auto_vacuum": true,
}

// Classify classifies the tokens of one statement.
func Classify(dialect string, toks []Token) Class {
	var c Class
	i := 0
	for i < len(toks) && toks[i].Kind == Punct && toks[i].Text == "(" {
		i++
	}
	if i >= len(toks) {
		c.ReadOnly = "empty statement"
		return c
	}
	if toks[i].Kind == Word {
		c.First = toks[i].Lower
	}
	c.Main = c.First
	if c.First == "with" {
		c.Main = mainAfterWith(toks[i+1:])
	}
	hasReturning := false
	for _, t := range toks {
		if t.Is("returning") {
			hasReturning = true
		}
	}
	switch {
	case c.First == "with":
		c.ReturnsRows = c.Main == "select" || c.Main == "values" || c.Main == "table" || hasReturning
	case rowFirst[c.First]:
		c.ReturnsRows = true
	default:
		c.ReturnsRows = hasReturning
	}
	c.ReadOnly = readOnlyReason(dialect, c, toks)
	return c
}

// mainAfterWith returns the first keyword at parenthesis depth 0 after the CTE list.
func mainAfterWith(toks []Token) string {
	depth := 0
	for _, t := range toks {
		if t.Kind == Punct {
			switch t.Text {
			case "(":
				depth++
			case ")":
				depth--
			}
			continue
		}
		if depth == 0 && t.Kind == Word {
			switch t.Lower {
			case "select", "insert", "update", "delete", "merge", "values", "table", "replace":
				return t.Lower
			}
		}
	}
	return ""
}

func readOnlyReason(dialect string, c Class, toks []Token) string {
	const generic = "only SELECT, WITH … SELECT, SHOW, DESCRIBE, EXPLAIN, VALUES and TABLE are allowed"
	if c.First == "pragma" && dialect == SQLite {
		return pragmaReason(toks)
	}
	if !readFirst[c.First] {
		return generic
	}
	if c.First == "with" && c.Main != "select" && c.Main != "values" && c.Main != "table" {
		return "data-modifying WITH statements are not allowed"
	}
	if c.First == "show" {
		// SHOW CREATE TABLE etc. are reads; only check for statement chaining
		for _, t := range toks {
			if t.Kind == Punct && t.Text == ";" {
				return "multiple statements are not allowed"
			}
		}
		return ""
	}
	for k, t := range toks {
		switch t.Kind {
		case Punct:
			if t.Text == ";" {
				return "multiple statements are not allowed"
			}
		case Word:
			if isWriteWord(toks, k) {
				if t.Lower == "analyze" && c.First == "explain" {
					return "EXPLAIN ANALYZE executes the statement and is not allowed"
				}
				if t.Lower == "into" {
					return "SELECT … INTO is not allowed"
				}
				if t.Lower == "update" || t.Lower == "share" || t.Lower == "lock" {
					return "locking reads (FOR UPDATE / LOCK IN SHARE MODE) are not allowed"
				}
				return strings.ToUpper(t.Lower) + " is not allowed"
			}
		}
		if (t.Kind == Word || t.Kind == Ident) && k+1 < len(toks) && toks[k+1].Kind == Punct && toks[k+1].Text == "(" {
			if isWriteFunc(t.Lower) {
				return t.Lower + "() is not allowed"
			}
		}
	}
	return ""
}

func isWriteFunc(name string) bool {
	if writeFuncs[name] {
		return true
	}
	for _, p := range writeFuncPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func pragmaReason(toks []Token) string {
	// PRAGMA [schema.]name [= value | (arg)]
	k := 1
	if k+1 < len(toks) && toks[k+1].Kind == Punct && toks[k+1].Text == "." {
		k += 2
	}
	if k >= len(toks) || !readPragmas[toks[k].Name()] {
		return "this PRAGMA is not allowed"
	}
	name := toks[k].Name()
	rest := toks[k+1:]
	if len(rest) == 0 {
		return ""
	}
	if rest[0].Kind == Punct && rest[0].Text == "=" {
		return "changing PRAGMA values is not allowed"
	}
	if rest[0].Kind == Punct && rest[0].Text == "(" {
		if settablePragmas[name] {
			return "changing PRAGMA values is not allowed"
		}
		// pragma name(arg): exactly one parenthesized argument
		if len(rest) <= 3 && rest[len(rest)-1].Text == ")" {
			return ""
		}
	}
	return "this PRAGMA is not allowed"
}

// CheckFragment validates a WHERE/ORDER BY fragment typed by the user for a
// read-only connection: no statement separators, write keywords or state-changing
// functions.
func CheckFragment(dialect, s string) string {
	res := Tokenize(dialect, s)
	if res.Unterminated {
		return "unterminated string or comment"
	}
	for k, t := range res.Tokens {
		if t.Kind == Punct && t.Text == ";" {
			return "';' is not allowed"
		}
		if isWriteWord(res.Tokens, k) {
			return strings.ToUpper(t.Lower) + " is not allowed"
		}
		if (t.Kind == Word || t.Kind == Ident) && k+1 < len(res.Tokens) && res.Tokens[k+1].Text == "(" && isWriteFunc(t.Lower) {
			return t.Lower + "() is not allowed"
		}
	}
	return ""
}
