package main

import (
	"encoding/json"
	"testing"
)

func setupProductos(t *testing.T) (*VecStore, *Collection) {
	t.Helper()
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
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
	return s, coll
}

func TestAggregateWithoutGroupBy(t *testing.T) {
	s, coll := setupProductos(t)
	defer s.Close()

	cases := []struct {
		op, field string
		want      float64
	}{
		{"count", "", 4},
		{"sum", "precio", 315},
		{"avg", "precio", 78.75},
		{"min", "precio", 5},
		{"max", "precio", 250},
	}
	for _, c := range cases {
		t.Run(c.op, func(t *testing.T) {
			results, err := aggregateDocuments(s, coll, c.op, c.field, "", nil)
			if err != nil {
				t.Fatalf("aggregateDocuments: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if results[0].Group != nil {
				t.Fatalf("expected no group, got %v", *results[0].Group)
			}
			if results[0].Value == nil || *results[0].Value != c.want {
				t.Fatalf("expected value %v, got %v", c.want, results[0].Value)
			}
		})
	}
}

func TestAggregateWithGroupBy(t *testing.T) {
	s, coll := setupProductos(t)
	defer s.Close()

	results, err := aggregateDocuments(s, coll, "sum", "precio", "categoria", nil)
	if err != nil {
		t.Fatalf("aggregateDocuments: %v", err)
	}
	want := map[string]float64{"perifericos": 65, "pantallas": 250}
	if len(results) != len(want) {
		t.Fatalf("expected %d groups, got %d: %+v", len(want), len(results), results)
	}
	for _, r := range results {
		if r.Group == nil {
			t.Fatalf("expected a group label, got nil in %+v", r)
		}
		wantVal, ok := want[*r.Group]
		if !ok {
			t.Fatalf("unexpected group %q", *r.Group)
		}
		if r.Value == nil || *r.Value != wantVal {
			t.Fatalf("group %q: expected %v, got %v", *r.Group, wantVal, r.Value)
		}
	}
}

func TestAggregateWithFilter(t *testing.T) {
	s, coll := setupProductos(t)
	defer s.Close()

	filters := []filterCondition{{field: "categoria", sqlOp: "=", value: "perifericos"}}
	results, err := aggregateDocuments(s, coll, "count", "", "", filters)
	if err != nil {
		t.Fatalf("aggregateDocuments: %v", err)
	}
	if len(results) != 1 || results[0].Value == nil || *results[0].Value != 3 {
		t.Fatalf("expected count=3 for perifericos, got %+v", results)
	}
}

func TestAggregateNoMatchesReturnsNullNotZero(t *testing.T) {
	s, coll := setupProductos(t)
	defer s.Close()

	filters := []filterCondition{{field: "categoria", sqlOp: "=", value: "no_existe"}}
	results, err := aggregateDocuments(s, coll, "sum", "precio", "", filters)
	if err != nil {
		t.Fatalf("aggregateDocuments: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Value != nil {
		t.Fatalf("expected nil value (SQL NULL, not 0) for SUM over zero rows, got %v", *results[0].Value)
	}
}

func TestAggregateValidation(t *testing.T) {
	s, coll := setupProductos(t)
	defer s.Close()

	if _, err := aggregateDocuments(s, coll, "bogus", "precio", "", nil); err == nil {
		t.Fatal("expected error for unknown op")
	}
	if _, err := aggregateDocuments(s, coll, "sum", "", "", nil); err == nil {
		t.Fatal("expected error: sum requires a field")
	}
	if _, err := aggregateDocuments(s, coll, "count", "has-dash", "", nil); err == nil {
		t.Fatal("expected error for invalid field name")
	}
	if _, err := aggregateDocuments(s, coll, "count", "", "has-dash", nil); err == nil {
		t.Fatal("expected error for invalid group_by name")
	}
	// count with no field is fine (count(*))
	if _, err := aggregateDocuments(s, coll, "count", "", "", nil); err != nil {
		t.Fatalf("expected count without field to be valid, got %v", err)
	}
}

func TestAggregateWorksOnVectorCollection(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "docs", true, 768, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "docs")

	if _, err := insertDocument(s, coll, nil, "el gato duerme en el sofá", json.RawMessage(`{"vistas":10}`)); err != nil {
		t.Fatalf("insertDocument: %v", err)
	}
	if _, err := insertDocument(s, coll, nil, "un felino descansa sobre el mueble", json.RawMessage(`{"vistas":30}`)); err != nil {
		t.Fatalf("insertDocument: %v", err)
	}

	results, err := aggregateDocuments(s, coll, "sum", "vistas", "", nil)
	if err != nil {
		t.Fatalf("aggregateDocuments on vector collection: %v", err)
	}
	if len(results) != 1 || results[0].Value == nil || *results[0].Value != 40 {
		t.Fatalf("expected sum=40, got %+v", results)
	}
}
