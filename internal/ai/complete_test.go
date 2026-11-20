package ai

import (
	"strings"
	"testing"

	"dbviewer/internal/model"
)

// at splits "text with | cursor" into the completion input.
func at(t *testing.T, text string) CompletionInput {
	t.Helper()
	i := strings.Index(text, "|")
	if i < 0 {
		t.Fatalf("no cursor in %q", text)
	}
	in := SplitCompletion(model.DriverMySQL, text[:i], text[i+1:])
	in.Names = map[string]string{}
	for _, n := range []string{"order_id", "increment_id", "entity_id", "sales_order", "customer_id", "created_at"} {
		in.Names[n] = n
	}
	return in
}

func TestSplitCompletion(t *testing.T) {
	in := SplitCompletion(model.DriverMySQL, "SELECT 1;\nSELECT ';' AS x FROM t WHERE ", " AND b = 2;\nSELECT 3;")
	if in.Before != "SELECT ';' AS x FROM t WHERE " || in.After != " AND b = 2" {
		t.Errorf("statement: %q | %q", in.Before, in.After)
	}
	if in.OtherBefore != "SELECT 1;" || in.OtherAfter != ";\nSELECT 3;" {
		t.Errorf("other: %q | %q", in.OtherBefore, in.OtherAfter)
	}
	// a cursor right after a ";" starts a new statement
	in = SplitCompletion(model.DriverMySQL, "SELECT 1;\n", "")
	if in.Before != "" || in.OtherBefore != "SELECT 1;" {
		t.Errorf("after ;: %+v", in)
	}
	// long text is cut at rune starts
	long := strings.Repeat("é", CompleteBeforeChars)
	in = SplitCompletion(model.DriverMySQL, long, long)
	if !strings.HasPrefix(in.Before, "é") || len(in.Before) > CompleteBeforeChars || len(in.After) > CompleteAfterChars {
		t.Errorf("cut: %d %d", len(in.Before), len(in.After))
	}
}

func TestCleanCompletion(t *testing.T) {
	cases := []struct{ name, text, reply, want string }{
		{"plain", "SELECT * FROM sales_order o JOIN customer c |", "ON o.customer_id = c.entity_id", "ON o.customer_id = c.entity_id"},
		{"code fence", "SELECT * FROM t WHERE |", "```sql\nid = 1\n```", "id = 1"},
		{"thinking", "SELECT * FROM t WHERE |", "<think>hmm</think>\nid = 1", "id = 1"},
		{"cursor marker", "SELECT * FROM t WHERE email = |", "'<CURSOR>'", "''"},
		{"repeats statement", "SELECT * FROM sales_order\nWHERE |", "SELECT * FROM sales_order\nWHERE status = 'paid'", "status = 'paid'"},
		{"repeats line", "SELECT o.id\nFROM sales_order o\nJOIN customer c |", "JOIN customer c ON c.id = o.customer_id", "ON c.id = o.customer_id"},
		{"overlap", "SELECT * FROM t WHERE status = |", "status = 'paid'", "'paid'"},
		{"finishes word", "SELECT COUNT(*) FROM sales_ord|", "sales_order", "er"},
		{"finishes word, no overlap", "SELECT COUNT(*) FROM sales_ord|", "er WHERE id = 1", "er"},
		{"missing space", "SELECT * FROM sales_order|", "WHERE id = 1", " WHERE id = 1"},
		{"missing space after keyword", "SELECT * FROM t WHERE|", "id = 1", " id = 1"},
		{"keyword being typed", "SELECT * FROM t WHE|", "RE id = 1", "RE id = 1"},
		{"column being typed", "SELECT o.incre|", "ment_id", "ment_id"},
		{"name in another case", "SELECT * FROM t WHERE cre|", "ATED_AT > NOW()", "ated_at > NOW()"},
		{"partial keyword that is a name", "SELECT or|", "der_id FROM t", "der_id FROM t"},
		{"no double space", "SELECT * FROM t |", " WHERE id = 1", "WHERE id = 1"},
		{"one condition", "SELECT * FROM t WHERE |", "status = 'paid' AND id > 3 OR x = 1", "status = 'paid'"},
		{"between", "SELECT * FROM t WHERE |", "d BETWEEN 1 AND 5 AND x = 1", "d BETWEEN 1 AND 5"},
		{"condition in parens", "SELECT * FROM t WHERE |", "(a = 1 OR b = 2) AND c = 3", "(a = 1 OR b = 2)"},
		{"clause", "SELECT * FROM t ORDER BY |", "id DESC LIMIT 10", "id DESC"},
		{"left join", "SELECT * FROM t\n|", "LEFT JOIN u ON u.id = t.u_id WHERE u.x = 1", "LEFT JOIN u ON u.id = t.u_id"},
		{"function named like a keyword", "SELECT |", "LEFT(name, 3) FROM t", "LEFT(name, 3) FROM t"},
		{"qualified name", "SELECT * FROM t i JOIN o ON |", "i.order_id = o.entity_id", "i.order_id = o.entity_id"},
		{"case", "SELECT |", "CASE WHEN a = 1 AND b = 2 THEN 1 END AS x WHERE", "CASE WHEN a = 1 AND b = 2 THEN 1 END AS x"},
		{"quoted keyword", "SELECT * FROM t WHERE note = |", "'this and that'", "'this and that'"},
		{"rest of line only", "SELECT * FROM t\nWHERE id = 1 |", "AND x = 2\nORDER BY id", "AND x = 2"},
		{"lines on empty line", "SELECT store_id, COUNT(*)\nFROM sales_order\n|", "WHERE x = 1\nGROUP BY store_id\nORDER BY 2 DESC\nLIMIT 5\nOFFSET 3", "WHERE x = 1\nGROUP BY store_id\nORDER BY 2 DESC\nLIMIT 5"},
		{"newline on empty line", "SELECT store_id\nFROM sales_order\n|", "\nGROUP BY store_id", "GROUP BY store_id"},
		{"blank line", "SELECT *\nFROM t\n|", "WHERE id = 1\n\nSELECT 2", "WHERE id = 1"},
		{"semicolon kept at the end", "SELECT * FROM t WHERE id = |", "1;\nSELECT 2", "1;"},
		{"semicolon dropped before more", "SELECT * FROM t WHERE id = |\nSELECT 2;", "1;", "1"},
		{"closing paren already there", "SELECT * FROM t WHERE id IN (SELECT id FROM u WHERE |)", "x = 1)", "x = 1"},
		{"repeats the suffix", "SELECT a, |\nFROM t", "b\nFROM t", "b"},
		{"only the suffix", "SELECT * FROM t WHERE id IN (1, 2|)", ")", ""},
		{"empty", "SELECT |", "   \n", ""},
		{"long", "SELECT |", strings.Repeat("abc, ", 100), ""},
	}
	for _, c := range cases {
		got := CleanCompletion(c.reply, at(t, c.text))
		if c.name == "long" {
			if len(got) > completeMaxChars || got == "" {
				t.Errorf("%s: %d chars", c.name, len(got))
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: CleanCompletion(%q) = %q, want %q", c.name, c.reply, got, c.want)
		}
	}
}

func TestCompletionTextStreaming(t *testing.T) {
	in := at(t, "SELECT * FROM t i JOIN o ON |")
	// a word at the end may still grow: "i.order" -> "i.order_id", "AND" -> "ANDROID"
	for _, partial := range []string{"i.order", "i.order_id = o.entity_id AND", "i.order_id = o.entity_id OR"} {
		if s, done := CompletionText(partial, in); done {
			t.Errorf("%q: complete too early (%q)", partial, s)
		}
	}
	if s, done := CompletionText("i.order_id = o.entity_id AND o.x", in); !done || s != "i.order_id = o.entity_id" {
		t.Errorf("after AND: %q %v", s, done)
	}
	// the model copying the statement: wait
	in = at(t, "SELECT o.id\nFROM sales_order o\nWHERE |")
	if s, done := CompletionText("SELECT o.id\nFROM sales", in); done || s != "" {
		t.Errorf("echo: %q %v", s, done)
	}
	// a newline ends a suggestion on a line with text
	in = at(t, "SELECT * FROM t |")
	if s, done := CompletionText("WHERE id = 1\nORDER", in); !done || s != "WHERE id = 1" {
		t.Errorf("newline: %q %v", s, done)
	}
}

func TestFIMPrompt(t *testing.T) {
	in := at(t, "SELECT 1;\nSELECT * FROM sales_order WHERE |;\nSELECT 2;")
	prefix, suffix := FIMPrompt(model.DriverMySQL, "sales_order(entity_id int PK)\n", in)
	if !strings.HasPrefix(prefix, "-- MySQL database\n-- Tables:\n-- sales_order(entity_id int PK)\n\nSELECT 1;\n\nSELECT * FROM sales_order WHERE ") {
		t.Errorf("prefix: %q", prefix)
	}
	if suffix != "\n;\nSELECT 2;" {
		t.Errorf("suffix: %q", suffix)
	}
}

func TestCompleteMessages(t *testing.T) {
	in := at(t, "SELECT * FROM t WHERE |")
	a := CompleteMessages(model.DriverSQLite, "t(id int PK)\n", in)
	in2 := at(t, "SELECT * FROM t WHERE id = |")
	b := CompleteMessages(model.DriverSQLite, "t(id int PK)\n", in2)
	if a[0].Content != b[0].Content || !strings.HasPrefix(b[1].Content, "Schema:\nt(id int PK)\n") {
		t.Errorf("prefix not stable:\n%s\n%s", a[1].Content, b[1].Content)
	}
	if !strings.Contains(b[1].Content, "WHERE id = "+CursorMarker) {
		t.Errorf("no cursor marker: %s", b[1].Content)
	}
}

func TestPickCompletionTables(t *testing.T) {
	ts := magentoTables()
	in := at(t, "SELECT i.sku, SUM(i.qty_ordered)\nFROM sales_order_item i\n|")
	picked := PickCompletionTables(in, ts)
	if len(picked) == 0 || len(picked) > completeTables || picked[0].Name != "sales_order_item" {
		t.Fatalf("picked %v", names(picked))
	}
	text, shown := CompletionSchemaText(picked, in)
	if index(shown, "sales_order_item") < 0 || len(text) > completeSchemaChars+500 {
		t.Errorf("schema text: %v (%d chars)", shown, len(text))
	}
	// sorted by name: the same tables give the same text
	for i := 1; i < len(shown); i++ {
		if shown[i-1] > shown[i] {
			t.Errorf("not sorted: %v", shown)
		}
	}
}
