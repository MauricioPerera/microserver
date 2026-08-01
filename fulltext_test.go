package main

import (
	"encoding/json"
	"testing"
)

func TestFullTextSearchBasic(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "docs", true, 768, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "docs")

	catID, err := insertDocument(s, coll, nil, "el gato duerme en el sofa", json.RawMessage(`{"cat":"a"}`))
	if err != nil {
		t.Fatalf("insertDocument: %v", err)
	}
	if _, err := insertDocument(s, coll, nil, "un perro corre en el parque", json.RawMessage(`{"cat":"b"}`)); err != nil {
		t.Fatalf("insertDocument: %v", err)
	}
	catID2, err := insertDocument(s, coll, nil, "el gato juega con un ovillo de lana", json.RawMessage(`{"cat":"a"}`))
	if err != nil {
		t.Fatalf("insertDocument: %v", err)
	}

	docs, err := fullTextSearchDocuments(s, coll, "gato", 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("fullTextSearchDocuments: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 matches for 'gato', got %d: %v", len(docs), docs)
	}
	ids := map[int64]bool{docs[0].ID: true, docs[1].ID: true}
	if !ids[catID] || !ids[catID2] {
		t.Fatalf("expected matches to include ids %d and %d, got %v", catID, catID2, docs)
	}
}

func TestFullTextSearchPhraseAndBoolean(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "docs", true, 768, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "docs")

	insertDocument(s, coll, nil, "el gato negro duerme", nil)
	insertDocument(s, coll, nil, "el perro negro corre", nil)
	insertDocument(s, coll, nil, "negro es un color, el gato es blanco", nil)

	// exact phrase "gato negro" should match only the first document
	docs, err := fullTextSearchDocuments(s, coll, `"gato negro"`, 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("fullTextSearchDocuments (phrase): %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 exact-phrase match, got %d: %v", len(docs), docs)
	}

	// boolean AND: gato AND negro should match doc 1 and doc 3 (both terms present)
	docs, err = fullTextSearchDocuments(s, coll, "gato AND negro", 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("fullTextSearchDocuments (AND): %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 matches for 'gato AND negro', got %d: %v", len(docs), docs)
	}
}

func TestFullTextSearchWithFilterAndSort(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "docs", true, 768, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "docs")

	insertDocument(s, coll, nil, "el gato duerme", json.RawMessage(`{"cat":"a","fecha":3}`))
	insertDocument(s, coll, nil, "el gato come", json.RawMessage(`{"cat":"a","fecha":1}`))
	insertDocument(s, coll, nil, "el gato juega", json.RawMessage(`{"cat":"b","fecha":2}`))

	filters := []filterCondition{{field: "cat", sqlOp: "=", value: "a"}}
	docs, err := fullTextSearchDocuments(s, coll, "gato", 10, 0, filters, &sortSpec{field: "fecha"})
	if err != nil {
		t.Fatalf("fullTextSearchDocuments: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 filtered matches, got %d: %v", len(docs), docs)
	}
	if docs[0].Text != "el gato come" || docs[1].Text != "el gato duerme" {
		t.Fatalf("expected ascending fecha order [come, duerme], got %+v", docs)
	}
}

func TestFullTextSearchRejectsNonVectorCollection(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "notas", false, 0, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "notas")

	if _, err := fullTextSearchDocuments(s, coll, "algo", 10, 0, nil, nil); err != ErrCollectionNoFullText {
		t.Fatalf("expected ErrCollectionNoFullText, got %v", err)
	}
}

func TestFullTextIndexStaysInSyncOnUpdateAndDelete(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "docs", true, 768, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "docs")

	id, err := insertDocument(s, coll, nil, "contenido original sobre elefantes", nil)
	if err != nil {
		t.Fatalf("insertDocument: %v", err)
	}

	docs, err := fullTextSearchDocuments(s, coll, "elefantes", 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("fullTextSearchDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 match before update, got %d", len(docs))
	}

	if _, err := updateDocument(s, coll, id, "contenido actualizado sobre jirafas", nil); err != nil {
		t.Fatalf("updateDocument: %v", err)
	}

	docs, err = fullTextSearchDocuments(s, coll, "elefantes", 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("fullTextSearchDocuments after update: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("expected 0 matches for 'elefantes' after update, got %d: %v", len(docs), docs)
	}

	docs, err = fullTextSearchDocuments(s, coll, "jirafas", 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("fullTextSearchDocuments for updated text: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 match for 'jirafas' after update, got %d", len(docs))
	}

	found, err := deleteDocument(s, coll, id)
	if err != nil {
		t.Fatalf("deleteDocument: %v", err)
	}
	if !found {
		t.Fatal("expected deleteDocument to find the item")
	}

	docs, err = fullTextSearchDocuments(s, coll, "jirafas", 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("fullTextSearchDocuments after delete: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("expected 0 matches after delete, got %d: %v", len(docs), docs)
	}
}

func TestFullTextSearchMalformedQueryReturnsError(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "docs", true, 768, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "docs")
	insertDocument(s, coll, nil, "contenido de prueba", nil)

	// unbalanced quote is invalid FTS5 syntax
	if _, err := fullTextSearchDocuments(s, coll, `"gato`, 10, 0, nil, nil); err == nil {
		t.Fatal("expected an error for malformed FTS5 query syntax")
	}
}
