package meta

import (
	"testing"

	"dbviewer/internal/model"
)

func TestSearchTableColumn(t *testing.T) {
	in := []SearchInput{{ConnID: "c", Index: model.SchemaIndex{Tables: []model.SchemaIndexTable{
		{Name: "sales_order", Kind: "table", Columns: []string{"entity_id", "grand_total"}},
		{Name: "quote", Kind: "table", Columns: []string{"grand_total"}},
	}}}}
	for _, q := range []string{"sales_order grand", "sales_order.grand_total", "so grand"} {
		hits := Search(q, in, nil, 10)
		if len(hits) == 0 || hits[0].Table != "sales_order" || hits[0].Column != "grand_total" {
			t.Fatalf("%q: got %+v", q, hits)
		}
		if hits[0].Label != "sales_order · grand_total" || len(hits[0].Positions) == 0 {
			t.Fatalf("%q: label/positions %+v", q, hits[0])
		}
	}
	// a plain query still finds the column in every table
	if hits := Search("grand_total", in, nil, 10); len(hits) != 2 {
		t.Fatalf("plain: %+v", hits)
	}
}
