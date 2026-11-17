package query

import (
	"unicode/utf16"
	"unicode/utf8"

	"dbviewer/internal/model"
	"dbviewer/internal/sqltok"
)

// Split splits a script into statements on top-level ';' using the dialect's
// tokenizer (quotes, E” strings, $$ bodies, backticks, nested and MySQL
// executable comments, MySQL "--" needing a following space). Offsets are UTF-16
// code unit indexes (JS string indexes). Empty statements are dropped. Each
// statement is classified (sqltok.Inspect) for the console's safety checks.
func Split(driver, sql string) []model.StatementRange {
	out := []model.StatementRange{}
	for _, r := range sqltok.Split(driver, sql) {
		in := sqltok.Inspect(driver, r.Tokens)
		kind := in.Main
		if kind == "" {
			kind = in.First
		}
		out = append(out, model.StatementRange{
			Start: utf16Index(sql, r.Start), End: utf16Index(sql, r.End), SQL: sql[r.Start:r.End],
			Kind: kind, Writes: in.Writes, Table: in.Table, NoWhere: in.NoWhere,
			TargetSchema: in.TargetSchema, TargetTable: in.TargetTable,
		})
	}
	return out
}

// statement is one statement with its tokens and classification.
type statement struct {
	sql   string
	class sqltok.Class
}

func splitClassified(driver, sql string) []statement {
	var out []statement
	for _, r := range sqltok.Split(driver, sql) {
		out = append(out, statement{sql: sql[r.Start:r.End], class: sqltok.Classify(driver, r.Tokens)})
	}
	return out
}

// utf16Index converts a byte offset into a UTF-16 code unit offset.
func utf16Index(s string, byteOff int) int {
	n := 0
	for _, r := range s[:byteOff] {
		if r == utf8.RuneError {
			n++
			continue
		}
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}
