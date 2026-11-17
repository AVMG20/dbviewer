package meta

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"dbviewer/internal/model"
)

// Score is an fzf-style subsequence match: every query rune must appear in order.
// Consecutive matches, matches at word boundaries (start, after _ . - space or a
// case change) and prefix matches score higher; gaps cost a little. Returns ok=false
// when the query is not a subsequence. Positions are byte offsets into s.
func Score(query, s string) (float64, []int, bool) {
	q := []rune(strings.ToLower(strings.TrimSpace(query)))
	if len(q) == 0 {
		return 0, nil, true
	}
	lower := strings.ToLower(s)
	// exact / substring fast paths score best
	if i := strings.Index(lower, string(q)); i >= 0 {
		pos := make([]int, 0, len(q))
		off := i
		for _, r := range string(q) {
			pos = append(pos, off)
			off += utf8.RuneLen(r)
		}
		score := 100.0 + float64(len(q))*10
		if i == 0 {
			score += 40
		} else if boundary(s, i) {
			score += 20
		}
		if len(lower) == len(string(q)) {
			score += 60
		}
		return score - float64(len(lower))*0.1, pos, true
	}
	var pos []int
	score := 0.0
	qi := 0
	prev := -2
	for bi, r := range lower {
		if qi >= len(q) {
			break
		}
		if r != q[qi] {
			continue
		}
		pts := 10.0
		if bi == prev+utf8.RuneLen(r) || (prev >= 0 && bi == prev+1) {
			pts += 15
		}
		if boundary(s, bi) {
			pts += 12
		}
		if prev >= 0 {
			pts -= float64(bi-prev) * 0.2
		}
		score += pts
		pos = append(pos, bi)
		prev = bi
		qi++
	}
	if qi < len(q) {
		return 0, nil, false
	}
	return score - float64(len(lower))*0.1, pos, true
}

func boundary(s string, i int) bool {
	if i == 0 {
		return true
	}
	p, _ := utf8.DecodeLastRuneInString(s[:i])
	c, _ := utf8.DecodeRuneInString(s[i:])
	return p == '_' || p == '.' || p == '-' || p == ' ' || p == '/' || (unicode.IsLower(p) && unicode.IsUpper(c))
}

// SearchInput is one connection's index (plus its name for labels).
type SearchInput struct {
	ConnID string
	Index  model.SchemaIndex
}

// Search ranks tables/views, columns and saved queries against q.
func Search(q string, inputs []SearchInput, saved []model.SavedQuery, limit int) []model.SearchHit {
	if limit <= 0 {
		limit = 50
	}
	var hits []model.SearchHit
	for _, in := range inputs {
		for _, t := range in.Index.Tables {
			if s, pos, ok := Score(q, t.Name); ok {
				hits = append(hits, model.SearchHit{ConnID: in.ConnID, Schema: in.Index.Schema, Table: t.Name, Kind: t.Kind,
					Label: t.Name, Score: s + 5, Positions: pos})
			}
			// "table column" / "table.column": the part before the last space or dot
			// narrows the table, the rest matches a column of it.
			if tq, cq, split := splitTableColumn(q); split {
				if tScore, tPos, ok := Score(tq, t.Name); ok {
					for _, c := range t.Columns {
						if cs, cpos, ok := Score(cq, c); ok {
							label := t.Name + " · " + c
							off := len(t.Name) + len(" · ")
							pos := append([]int{}, tPos...)
							for _, p := range cpos {
								pos = append(pos, p+off)
							}
							hits = append(hits, model.SearchHit{ConnID: in.ConnID, Schema: in.Index.Schema, Table: t.Name, Column: c,
								Kind: "column", Label: label, Score: tScore + cs, Positions: pos})
						}
					}
					continue
				}
			}
			for _, c := range t.Columns {
				if s, pos, ok := Score(q, c); ok {
					label := t.Name + " · " + c
					off := len(t.Name) + len(" · ")
					for i := range pos {
						pos[i] += off
					}
					hits = append(hits, model.SearchHit{ConnID: in.ConnID, Schema: in.Index.Schema, Table: t.Name, Column: c,
						Kind: "column", Label: label, Score: s - 10 - float64(len(t.Name))*0.05, Positions: pos})
				}
			}
		}
	}
	for _, sq := range saved {
		if s, pos, ok := Score(q, sq.Name); ok {
			hits = append(hits, model.SearchHit{ConnID: sq.ConnID, Kind: "saved_query", SavedID: sq.ID, Label: sq.Name, Score: s + 5, Positions: pos})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Label < hits[j].Label
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	for i := range hits {
		hits[i].Positions = toUTF16(hits[i].Label, hits[i].Positions)
	}
	return hits
}

// splitTableColumn splits "sales_order grand" / "sales_order.grand_total" at the last
// space or dot into a table part and a column part (both non-empty).
func splitTableColumn(q string) (string, string, bool) {
	q = strings.TrimSpace(q)
	i := strings.LastIndexAny(q, " .")
	if i <= 0 || i >= len(q)-1 {
		return "", "", false
	}
	t, c := strings.TrimSpace(q[:i]), strings.TrimSpace(q[i+1:])
	if t == "" || c == "" {
		return "", "", false
	}
	return t, c, true
}

// toUTF16 converts byte offsets into UTF-16 code unit offsets (JS string indexes).
func toUTF16(s string, pos []int) []int {
	out := make([]int, 0, len(pos))
	if len(pos) == 0 {
		return out
	}
	m := map[int]int{}
	u := 0
	for bi, r := range s {
		m[bi] = u
		if r >= 0x10000 {
			u += 2
		} else {
			u++
		}
	}
	for _, p := range pos {
		out = append(out, m[p])
	}
	return out
}
