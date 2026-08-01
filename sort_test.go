package main

import (
	"encoding/json"
	"strconv"
	"testing"
)

func TestListDocumentsWithSort(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "productos", false, 0, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "productos")

	// insert out of price order on purpose
	prices := []int64{45, 5, 250, 15}
	ids := make([]int64, len(prices))
	for i, p := range prices {
		id, err := insertDocument(s, coll, nil, "", json.RawMessage(`{"precio":`+strconv.FormatInt(p, 10)+`}`))
		if err != nil {
			t.Fatalf("insertDocument: %v", err)
		}
		ids[i] = id
	}

	t.Run("ascending", func(t *testing.T) {
		docs, err := listDocuments(s, coll, 100, 0, nil, &sortSpec{field: "precio"})
		if err != nil {
			t.Fatalf("listDocuments: %v", err)
		}
		want := []int64{5, 15, 45, 250}
		got := pricesOf(t, docs)
		assertIntSliceEqual(t, want, got)
	})

	t.Run("descending", func(t *testing.T) {
		docs, err := listDocuments(s, coll, 100, 0, nil, &sortSpec{field: "precio", desc: true})
		if err != nil {
			t.Fatalf("listDocuments: %v", err)
		}
		want := []int64{250, 45, 15, 5}
		got := pricesOf(t, docs)
		assertIntSliceEqual(t, want, got)
	})

	t.Run("combined with filter", func(t *testing.T) {
		filters := []filterCondition{{field: "precio", sqlOp: "<", value: int64(100)}}
		docs, err := listDocuments(s, coll, 100, 0, filters, &sortSpec{field: "precio", desc: true})
		if err != nil {
			t.Fatalf("listDocuments: %v", err)
		}
		want := []int64{45, 15, 5}
		got := pricesOf(t, docs)
		assertIntSliceEqual(t, want, got)
	})

	t.Run("invalid sort field from query", func(t *testing.T) {
		q := parseQueryValues(t, "sort=has-dash")
		if _, err := parseSort(q); err == nil {
			t.Fatal("expected error for invalid sort field")
		}
	})

	t.Run("no sort param returns nil spec", func(t *testing.T) {
		q := parseQueryValues(t, "limit=10")
		spec, err := parseSort(q)
		if err != nil {
			t.Fatalf("parseSort: %v", err)
		}
		if spec != nil {
			t.Fatalf("expected nil sortSpec, got %+v", spec)
		}
	})

	t.Run("leading dash means descending", func(t *testing.T) {
		q := parseQueryValues(t, "sort=-precio")
		spec, err := parseSort(q)
		if err != nil {
			t.Fatalf("parseSort: %v", err)
		}
		if spec == nil || spec.field != "precio" || !spec.desc {
			t.Fatalf("expected {field:precio desc:true}, got %+v", spec)
		}
	})
}

func TestSearchDocumentsWithSort(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "docs", true, 768, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "docs")

	// all semantically close to the query, but with distinct "orden" values
	texts := []struct {
		text  string
		orden int64
	}{
		{"el gato duerme en el sofá", 3},
		{"un felino descansa sobre el mueble", 1},
		{"un gato juega con un ovillo", 2},
	}
	for _, tc := range texts {
		if _, err := insertDocument(s, coll, nil, tc.text, json.RawMessage(`{"orden":`+strconv.FormatInt(tc.orden, 10)+`}`)); err != nil {
			t.Fatalf("insertDocument: %v", err)
		}
	}

	sort := &sortSpec{field: "orden"}

	t.Run("rerank=true", func(t *testing.T) {
		docs, err := searchDocuments(s, coll, "un gato tomando una siesta", 5, true, nil, sort)
		if err != nil {
			t.Fatalf("searchDocuments: %v", err)
		}
		if len(docs) != 3 {
			t.Fatalf("expected 3 results, got %d", len(docs))
		}
		got := ordenOf(t, docs)
		assertIntSliceEqual(t, []int64{1, 2, 3}, got)
	})

	t.Run("rerank=false", func(t *testing.T) {
		docs, err := searchDocuments(s, coll, "un gato tomando una siesta", 5, false, nil, sort)
		if err != nil {
			t.Fatalf("searchDocuments: %v", err)
		}
		if len(docs) != 3 {
			t.Fatalf("expected 3 results, got %d", len(docs))
		}
		got := ordenOf(t, docs)
		assertIntSliceEqual(t, []int64{1, 2, 3}, got)
	})
}

func pricesOf(t *testing.T, docs []Document) []int64 {
	t.Helper()
	out := make([]int64, len(docs))
	for i, d := range docs {
		var m map[string]int64
		if err := json.Unmarshal(d.Data, &m); err != nil {
			t.Fatalf("unmarshaling data: %v", err)
		}
		out[i] = m["precio"]
	}
	return out
}

func ordenOf(t *testing.T, docs []Document) []int64 {
	t.Helper()
	out := make([]int64, len(docs))
	for i, d := range docs {
		var m map[string]int64
		if err := json.Unmarshal(d.Data, &m); err != nil {
			t.Fatalf("unmarshaling data: %v", err)
		}
		out[i] = m["orden"]
	}
	return out
}

func assertIntSliceEqual(t *testing.T, want, got []int64) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}
