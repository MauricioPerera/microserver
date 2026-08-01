package main

import "testing"

func TestVecInsertAndQuery(t *testing.T) {
	db, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer db.Close()

	docs := map[int64]string{
		1: "el gato duerme en el sofá",
		2: "un felino descansa sobre el mueble",
		3: "la bolsa de valores subió hoy",
	}
	for id, text := range docs {
		if err := insertText(db, id, text); err != nil {
			t.Fatalf("insertText id=%d: %v", id, err)
		}
	}

	t.Run("rerank=true (exact float32 quality)", func(t *testing.T) {
		rows, err := queryText(db, "un gato tomando una siesta", 1, true)
		if err != nil {
			t.Fatalf("queryText: %v", err)
		}
		defer rows.Close()

		if !rows.Next() {
			t.Fatal("expected at least one result")
		}
		var id int64
		var distance float64
		if err := rows.Scan(&id, &distance); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if id != 1 && id != 2 {
			t.Fatalf("expected nearest neighbor to be the cat sentence (id=1 or 2), got id=%d (distance=%f)", id, distance)
		}
		t.Logf("nearest neighbor: id=%d distance=%f", id, distance)
	})

	t.Run("rerank=false (binary-only, fast path)", func(t *testing.T) {
		rows, err := queryText(db, "un gato tomando una siesta", 1, false)
		if err != nil {
			t.Fatalf("queryText: %v", err)
		}
		defer rows.Close()

		if !rows.Next() {
			t.Fatal("expected at least one result")
		}
		var id int64
		var distance float64
		if err := rows.Scan(&id, &distance); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if id != 1 && id != 2 {
			t.Fatalf("expected nearest neighbor to be the cat sentence (id=1 or 2), got id=%d (distance=%f)", id, distance)
		}
		t.Logf("nearest neighbor: id=%d distance=%f", id, distance)
	})
}
