package services

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"dbviewer/internal/ai"
	"dbviewer/internal/conn"
	"dbviewer/internal/model"
)

// dynamicMetaResult holds lightweight schema-level metadata discovered read-only
// (e.g. Magento EAV attribute definitions) so the LLM can write exact queries in 1 call
// without guessing backend_type tables or attribute IDs.
type dynamicMetaResult struct {
	// Text is a compact human/LLM-readable metadata block to append to the prompt.
	Text string
	// ForceTables lists table names that should always be included in the schema section.
	ForceTables []string
}

// genericMetaWords are common domain/prompt words that are too broad to match EAV
// attribute codes or meta_key values usefully.
var genericMetaWords = map[string]bool{
	"product": true, "products": true, "customer": true, "customers": true,
	"category": true, "categories": true, "order": true, "orders": true,
	"invoice": true, "invoices": true, "shipment": true, "shipments": true,
	"attribute": true, "attributes": true, "entity": true, "entities": true,
	"magento": true, "wordpress": true, "filled": true, "empty": true,
	"blank": true, "missing": true, "present": true, "exists": true,
	"existing": true, "active": true, "enabled": true, "disabled": true,
	"store": true, "stores": true, "website": true, "websites": true,
	"create": true, "write": true, "return": true, "select": true,
	"having": true, "where": true, "join": true, "group": true,
	"limit": true, "count": true, "total": true, "null": true,
	"string": true, "varchar": true, "text": true, "decimal": true,
	"datetime": true, "boolean": true, "option": true, "options": true,
}

func specificKeywords(prompt string, extraTerms []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(w string) {
		w = strings.ToLower(strings.TrimSpace(w))
		if len(w) < 3 || genericMetaWords[w] || seen[w] {
			return
		}
		seen[w] = true
		out = append(out, w)
	}
	for _, w := range ai.PromptKeywords(prompt) {
		add(w)
	}
	for _, w := range extraTerms {
		add(w)
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func hasColumn(t *ai.TableInfo, name string) bool {
	for _, c := range t.Columns {
		if strings.EqualFold(c.Name, name) {
			return true
		}
	}
	return false
}

// lookupDynamicMeta checks for known schema-metadata tables (currently EAV such as
// Magento's eav_attribute / eav_entity_type, and small key-value *_meta tables)
// using fast read-only queries. Never reads business/user data rows and silently
// degrades on timeout or error.
func (s *AIService) lookupDynamicMeta(ctx context.Context, p *conn.Pool, schema, prompt string, extraTerms []string, infos []ai.TableInfo) dynamicMetaResult {
	byName := make(map[string]*ai.TableInfo, len(infos))
	var eavAttr *ai.TableInfo
	for i := range infos {
		t := &infos[i]
		lt := strings.ToLower(t.Name)
		byName[lt] = t
		if (lt == "eav_attribute" || strings.HasSuffix(lt, "_eav_attribute")) &&
			hasColumn(t, "attribute_id") && hasColumn(t, "attribute_code") && hasColumn(t, "backend_type") {
			if eavAttr == nil || lt == "eav_attribute" {
				eavAttr = t
			}
		}
	}

	keywords := specificKeywords(prompt, extraTerms)
	var out dynamicMetaResult
	if eavAttr != nil {
		out = s.lookupEAVMeta(ctx, p, schema, prompt, keywords, eavAttr, byName)
	}
	if out.Text == "" && len(keywords) > 0 {
		out = s.lookupMetaKeys(ctx, p, schema, prompt, keywords, infos)
	}
	return out
}

func placeholder(driver string, n int) string {
	if driver == model.DriverPostgres {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `!`, `!!`)
	s = strings.ReplaceAll(s, `%`, `!%`)
	s = strings.ReplaceAll(s, `_`, `!_`)
	return s
}

func (s *AIService) lookupEAVMeta(ctx context.Context, p *conn.Pool, schema, prompt string, keywords []string, eavAttr *ai.TableInfo, byName map[string]*ai.TableInfo) dynamicMetaResult {
	var res dynamicMetaResult
	lowAttr := strings.ToLower(eavAttr.Name)
	prefix := strings.TrimSuffix(lowAttr, "eav_attribute")
	eavType := byName[prefix+"eav_entity_type"]
	hasType := eavType != nil && hasColumn(eavType, "entity_type_id") && hasColumn(eavType, "entity_type_code") && hasColumn(eavType, "entity_table")
	hasInput := hasColumn(eavAttr, "frontend_input")
	hasLabel := hasColumn(eavAttr, "frontend_label")

	if len(keywords) == 0 {
		return res
	}

	sch := p.Schema(schema)
	qAttr := p.Driver.QualifiedName(sch, eavAttr.Name)
	selectCols := []string{"a.attribute_id", "a.attribute_code", "a.backend_type"}
	if hasInput {
		selectCols = append(selectCols, "COALESCE(a.frontend_input, '')")
	} else {
		selectCols = append(selectCols, "''")
	}
	if hasLabel {
		selectCols = append(selectCols, "COALESCE(a.frontend_label, '')")
	} else {
		selectCols = append(selectCols, "''")
	}
	fromClause := qAttr + " a"
	if hasType {
		selectCols = append(selectCols, "COALESCE(t.entity_type_code, '')", "COALESCE(t.entity_table, '')")
		fromClause += " LEFT JOIN " + p.Driver.QualifiedName(sch, eavType.Name) + " t ON t.entity_type_id = a.entity_type_id"
	} else {
		selectCols = append(selectCols, "''", "''")
	}

	var conds []string
	var args []any
	var exactConds []string
	var exactArgs []any
	argN := 1
	for _, kw := range keywords {
		phExact := placeholder(p.Config.Driver, argN)
		argN++
		exactConds = append(exactConds, fmt.Sprintf("LOWER(a.attribute_code) = %s", phExact))
		exactArgs = append(exactArgs, kw)
	}
	for _, kw := range keywords {
		likeVal := "%" + escapeLike(kw) + "%"
		ph1 := placeholder(p.Config.Driver, argN)
		argN++
		conds = append(conds, fmt.Sprintf("LOWER(a.attribute_code) LIKE %s ESCAPE '!'", ph1))
		args = append(args, likeVal)
		if hasLabel {
			ph2 := placeholder(p.Config.Driver, argN)
			argN++
			conds = append(conds, fmt.Sprintf("LOWER(a.frontend_label) LIKE %s ESCAPE '!'", ph2))
			args = append(args, likeVal)
		}
	}
	allArgs := append(exactArgs, args...)
	hint := ""
	if p.Config.Driver == model.DriverMySQL {
		hint = "/*+ MAX_EXECUTION_TIME(1500) */ "
	}
	q := fmt.Sprintf(
		"SELECT %s%s FROM %s WHERE %s ORDER BY CASE WHEN %s THEN 0 ELSE 1 END, a.attribute_id LIMIT 12",
		hint, strings.Join(selectCols, ", "), fromClause, strings.Join(conds, " OR "), strings.Join(exactConds, " OR "),
	)

	cctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()

	type attrRow struct {
		id         int64
		code       string
		backend    string
		input      string
		label      string
		entityCode string
		entityTbl  string
	}
	var rowsOut []attrRow
	err := p.Read(cctx, true, func(db conn.Querier) error {
		if p.Config.Driver == model.DriverPostgres {
			if r, err := db.QueryContext(cctx, "SET LOCAL statement_timeout = 1500"); err == nil {
				r.Close()
			}
		}
		rows, err := db.QueryContext(cctx, q, allArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r attrRow
			var id sql.NullInt64
			var code, backend, input, label, entityCode, entityTbl sql.NullString
			if err := rows.Scan(&id, &code, &backend, &input, &label, &entityCode, &entityTbl); err != nil {
				return err
			}
			r = attrRow{
				id: id.Int64, code: code.String, backend: backend.String,
				input: input.String, label: label.String,
				entityCode: entityCode.String, entityTbl: entityTbl.String,
			}
			rowsOut = append(rowsOut, r)
		}
		return rows.Err()
	})
	if err != nil || len(rowsOut) == 0 {
		// Even if no keyword matched (e.g. abbreviated attribute_code), when the prompt
		// explicitly mentions attribute/EAV/Magento, ensure eav_attribute + entity_type are included.
		lowPrompt := strings.ToLower(prompt)
		if strings.Contains(lowPrompt, "attribute") || strings.Contains(lowPrompt, "eav") || strings.Contains(lowPrompt, "magento") {
			res.ForceTables = append(res.ForceTables, eavAttr.Name)
			if hasType {
				res.ForceTables = append(res.ForceTables, eavType.Name)
			}
		}
		return res
	}

	forceSeen := map[string]bool{}
	addForce := func(name string) {
		low := strings.ToLower(name)
		if t, ok := byName[low]; ok && !forceSeen[low] {
			forceSeen[low] = true
			res.ForceTables = append(res.ForceTables, t.Name)
		}
	}
	addForce(eavAttr.Name)
	if hasType {
		addForce(eavType.Name)
	}

	var b strings.Builder
	b.WriteString("EAV attributes found in " + eavAttr.Name + ":\n")
	for _, r := range rowsOut {
		entityTable := ""
		if r.entityTbl != "" {
			if strings.HasPrefix(strings.ToLower(r.entityTbl), prefix) {
				entityTable = r.entityTbl
			} else {
				entityTable = prefix + r.entityTbl
			}
			addForce(entityTable)
		}
		valTable := ""
		if entityTable != "" {
			switch strings.ToLower(r.backend) {
			case "", "static":
				valTable = entityTable + " (static column)"
			default:
				cand := entityTable + "_" + strings.ToLower(r.backend)
				if t, ok := byName[strings.ToLower(cand)]; ok {
					valTable = t.Name
					addForce(t.Name)
				} else {
					valTable = cand
				}
			}
		}
		if r.input == "select" || r.input == "multiselect" {
			addForce(prefix + "eav_attribute_option")
			addForce(prefix + "eav_attribute_option_value")
		}
		fmt.Fprintf(&b, "- %s (attribute_id=%d", r.code, r.id)
		if r.entityCode != "" {
			fmt.Fprintf(&b, ", entity=%s", r.entityCode)
		}
		if r.backend != "" {
			fmt.Fprintf(&b, ", backend_type=%s", r.backend)
		}
		if valTable != "" {
			fmt.Fprintf(&b, ", value_table=%s", valTable)
		}
		if r.input != "" {
			fmt.Fprintf(&b, ", input=%s", r.input)
		}
		if r.label != "" && !strings.EqualFold(r.label, r.code) {
			fmt.Fprintf(&b, ", label=%q", r.label)
		}
		b.WriteString(")\n")
	}
	res.Text = b.String()
	return res
}

// lookupMetaKeys discovers matching meta_key values in small key-value meta tables
// (such as WordPress wp_postmeta / wp_usermeta) when their estimated row count is small
// enough for a sub-second lookup.
func (s *AIService) lookupMetaKeys(ctx context.Context, p *conn.Pool, schema, prompt string, keywords []string, infos []ai.TableInfo) dynamicMetaResult {
	var res dynamicMetaResult
	// Only probe tables with a meta_key column that are small (<= 50k est rows) to stay fast.
	var candidates []*ai.TableInfo
	for i := range infos {
		t := &infos[i]
		if !hasColumn(t, "meta_key") {
			continue
		}
		if t.Rows > 50000 {
			continue
		}
		candidates = append(candidates, t)
		if len(candidates) >= 2 {
			break
		}
	}
	if len(candidates) == 0 {
		return res
	}
	// Skip if any keyword already matches a column name on a relevant table.
	sch := p.Schema(schema)
	var b strings.Builder
	for _, t := range candidates {
		qt := p.Driver.QualifiedName(sch, t.Name)
		var conds []string
		var args []any
		for i, kw := range keywords {
			conds = append(conds, fmt.Sprintf("LOWER(meta_key) LIKE %s ESCAPE '!'", placeholder(p.Config.Driver, i+1)))
			args = append(args, "%"+escapeLike(kw)+"%")
		}
		hint := ""
		if p.Config.Driver == model.DriverMySQL {
			hint = "/*+ MAX_EXECUTION_TIME(600) */ "
		}
		q := fmt.Sprintf("SELECT %sDISTINCT meta_key FROM %s WHERE %s LIMIT 12", hint, qt, strings.Join(conds, " OR "))
		cctx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
		var keys []string
		err := p.Read(cctx, true, func(db conn.Querier) error {
			if p.Config.Driver == model.DriverPostgres {
				if r, err := db.QueryContext(cctx, "SET LOCAL statement_timeout = 600"); err == nil {
					r.Close()
				}
			}
			rows, err := db.QueryContext(cctx, q, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var k sql.NullString
				if err := rows.Scan(&k); err != nil {
					return err
				}
				if k.String != "" {
					keys = append(keys, k.String)
				}
			}
			return rows.Err()
		})
		cancel()
		if err != nil || len(keys) == 0 {
			continue
		}
		res.ForceTables = append(res.ForceTables, t.Name)
		fmt.Fprintf(&b, "Matching %s.meta_key values: %s\n", t.Name, strings.Join(keys, ", "))
	}
	res.Text = b.String()
	return res
}
