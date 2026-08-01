package main

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestListDocumentsWithFilters(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "productos", false, 0, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "productos")

	products := []string{
		`{"nombre":"mouse","precio":15,"categoria":"perifericos"}`,
		`{"nombre":"teclado","precio":45,"categoria":"perifericos"}`,
		`{"nombre":"monitor","precio":250,"categoria":"pantallas"}`,
		`{"nombre":"cable","precio":5,"categoria":"perifericos"}`,
	}
	for _, p := range products {
		if _, err := insertDocument(s, coll, nil, "", json.RawMessage(p)); err != nil {
			t.Fatalf("insertDocument: %v", err)
		}
	}

	cases := []struct {
		name    string
		filters []filterCondition
		want    int
	}{
		{"eq string", []filterCondition{{field: "categoria", sqlOp: "=", value: "perifericos"}}, 3},
		{"lt number", []filterCondition{{field: "precio", sqlOp: "<", value: int64(50)}}, 3},
		{"gte number", []filterCondition{{field: "precio", sqlOp: ">=", value: int64(45)}}, 2},
		{"combined AND", []filterCondition{
			{field: "categoria", sqlOp: "=", value: "perifericos"},
			{field: "precio", sqlOp: "<", value: int64(20)},
		}, 2},
		{"no match", []filterCondition{{field: "categoria", sqlOp: "=", value: "no_existe"}}, 0},
		{"nonexistent field", []filterCondition{{field: "peso", sqlOp: "=", value: int64(1)}}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			docs, err := listDocuments(s, coll, 100, 0, c.filters, nil)
			if err != nil {
				t.Fatalf("listDocuments: %v", err)
			}
			if len(docs) != c.want {
				t.Fatalf("expected %d docs, got %d: %v", c.want, len(docs), docs)
			}
		})
	}
}

func TestParseFiltersFromQuery(t *testing.T) {
	q := parseQueryValues(t, "categoria=perifericos&precio__lt=50&limit=10&offset=0")
	conds, err := parseFilters(q)
	if err != nil {
		t.Fatalf("parseFilters: %v", err)
	}
	if len(conds) != 2 {
		t.Fatalf("expected 2 conditions (limit/offset excluded), got %d: %+v", len(conds), conds)
	}

	// unknown operator -> error
	badQ := parseQueryValues(t, "precio__bogus=5")
	if _, err := parseFilters(badQ); err == nil {
		t.Fatal("expected error for unknown filter operator")
	}

	// invalid field name -> error
	badFieldQ := parseQueryValues(t, "has-dash=5")
	if _, err := parseFilters(badFieldQ); err == nil {
		t.Fatal("expected error for invalid filter field name")
	}
}

func TestSearchDocumentsWithFilter(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "docs", true, 768, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "docs")

	catA, err := insertDocument(s, coll, nil, "el gato duerme en el sofá", json.RawMessage(`{"cat":"a"}`))
	if err != nil {
		t.Fatalf("insertDocument: %v", err)
	}
	if _, err := insertDocument(s, coll, nil, "un felino descansa sobre el mueble", json.RawMessage(`{"cat":"b"}`)); err != nil {
		t.Fatalf("insertDocument: %v", err)
	}

	filters := []filterCondition{{field: "cat", sqlOp: "=", value: "a"}}
	docs, err := searchDocuments(s, coll, "un gato tomando una siesta", 5, true, filters, nil)
	if err != nil {
		t.Fatalf("searchDocuments with filter: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != catA {
		t.Fatalf("expected only the cat=a document (id=%d), got %v", catA, docs)
	}

	// same filter, rerank=false path
	docs, err = searchDocuments(s, coll, "un gato tomando una siesta", 5, false, filters, nil)
	if err != nil {
		t.Fatalf("searchDocuments (no rerank) with filter: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != catA {
		t.Fatalf("expected only the cat=a document (id=%d) without rerank, got %v", catA, docs)
	}
}

func parseQueryValues(t *testing.T, raw string) url.Values {
	t.Helper()
	values, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parsing test query %q: %v", raw, err)
	}
	return values
}
