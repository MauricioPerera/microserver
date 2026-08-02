package main

import (
	"encoding/json"
	"testing"
)

func TestCreateAndDropCollection(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "notas", false, 0, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}

	if err := createCollection(s, "notas", false, 0, nil); err == nil {
		t.Fatal("expected error creating duplicate collection")
	} else if err != ErrCollectionExists {
		t.Fatalf("expected ErrCollectionExists, got %v", err)
	}

	cols, err := listCollections(s)
	if err != nil {
		t.Fatalf("listCollections: %v", err)
	}
	if len(cols) != 1 || cols[0].Name != "notas" {
		t.Fatalf("expected [notas], got %v", cols)
	}

	if err := dropCollection(s, "notas"); err != nil {
		t.Fatalf("dropCollection: %v", err)
	}
	if err := dropCollection(s, "notas"); err != ErrCollectionNotFound {
		t.Fatalf("expected ErrCollectionNotFound on second drop, got %v", err)
	}
}

func TestRenameCollection(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "notas", false, 0, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	notasColl, err := getCollection(s, "notas")
	if err != nil {
		t.Fatalf("getCollection(notas): %v", err)
	}
	if _, err := insertDocument(s, notasColl, nil, "", json.RawMessage(`{"titulo":"compras"}`)); err != nil {
		t.Fatalf("insertDocument: %v", err)
	}

	if err := renameCollection(s, "notas", "apuntes"); err != nil {
		t.Fatalf("renameCollection: %v", err)
	}

	if _, err := getCollection(s, "notas"); err != ErrCollectionNotFound {
		t.Fatalf("expected old name to be gone, got %v", err)
	}
	coll, err := getCollection(s, "apuntes")
	if err != nil {
		t.Fatalf("getCollection(apuntes): %v", err)
	}

	docs, err := listDocuments(s, coll, 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("listDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected the document to survive the rename, got %d docs", len(docs))
	}
}

func TestRenameCollectionValidation(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "notas", false, 0, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	if err := createCollection(s, "recetas", false, 0, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}

	if err := renameCollection(s, "nosuchcollection", "algo"); err != ErrCollectionNotFound {
		t.Fatalf("expected ErrCollectionNotFound, got %v", err)
	}
	if err := renameCollection(s, "notas", "bad name"); err != ErrInvalidCollectionName {
		t.Fatalf("expected ErrInvalidCollectionName, got %v", err)
	}
	if err := renameCollection(s, "notas", "recetas"); err != ErrCollectionExists {
		t.Fatalf("expected ErrCollectionExists, got %v", err)
	}

	// still there under its original name after all the rejected attempts
	if _, err := getCollection(s, "notas"); err != nil {
		t.Fatalf("expected notas to be unaffected by rejected renames: %v", err)
	}
}

func TestRenameCollectionCascadesReferences(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "autores", false, 0, nil); err != nil {
		t.Fatalf("createCollection(autores): %v", err)
	}
	if err := createCollection(s, "posts", false, 0, map[string]ReferenceSpec{
		"autor_id": {Collection: "autores"},
	}); err != nil {
		t.Fatalf("createCollection(posts): %v", err)
	}

	if err := renameCollection(s, "autores", "escritores"); err != nil {
		t.Fatalf("renameCollection: %v", err)
	}

	refs, err := getReferences(s, "posts")
	if err != nil {
		t.Fatalf("getReferences: %v", err)
	}
	if len(refs) != 1 || refs[0].Collection != "escritores" {
		t.Fatalf("expected posts' reference to now point at escritores, got %+v", refs)
	}

	// the reference still resolves correctly post-rename: inserting a post
	// that points at a real id in the renamed collection should validate.
	escritoresColl, err := getCollection(s, "escritores")
	if err != nil {
		t.Fatalf("getCollection(escritores): %v", err)
	}
	autorID, err := insertDocument(s, escritoresColl, nil, "", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("insertDocument(escritores): %v", err)
	}
	postsColl, err := getCollection(s, "posts")
	if err != nil {
		t.Fatalf("getCollection(posts): %v", err)
	}
	postData, _ := json.Marshal(map[string]int64{"autor_id": autorID})
	if _, err := insertDocument(s, postsColl, nil, "", postData); err != nil {
		t.Fatalf("insertDocument(posts) with post-rename reference: %v", err)
	}
}

func TestCollectionNameValidation(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	bad := []string{
		"",
		"1starts_with_digit",
		"has spaces",
		`"; DROP TABLE collections; --`,
		"has-dash",
		"has.dot",
	}
	for _, name := range bad {
		if err := createCollection(s, name, false, 0, nil); err != ErrInvalidCollectionName {
			t.Errorf("name %q: expected ErrInvalidCollectionName, got %v", name, err)
		}
	}

	// confirm collections table wasn't corrupted by the injection attempt
	cols, err := listCollections(s)
	if err != nil {
		t.Fatalf("listCollections after bad names: %v", err)
	}
	if len(cols) != 0 {
		t.Fatalf("expected no collections created, got %v", cols)
	}
}

func TestVectorCollectionRequiresValidDimensions(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "vecs", true, 0, nil); err == nil {
		t.Fatal("expected error for dimensions=0 with vector=true")
	}
	if err := createCollection(s, "vecs", true, 100000, nil); err == nil {
		t.Fatal("expected error for dimensions over the max")
	}
}

func TestNonVectorCollectionDocuments(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "notas", false, 0, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, err := getCollection(s, "notas")
	if err != nil {
		t.Fatalf("getCollection: %v", err)
	}

	data := json.RawMessage(`{"titulo":"compras","items":["leche","pan"]}`)
	id, err := insertDocument(s, coll, nil, "", data)
	if err != nil {
		t.Fatalf("insertDocument: %v", err)
	}

	docs, err := listDocuments(s, coll, 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("listDocuments: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != id {
		t.Fatalf("expected 1 doc with id=%d, got %v", id, docs)
	}
	if string(docs[0].Data) != string(data) {
		t.Fatalf("expected data %s, got %s", data, docs[0].Data)
	}

	newData := json.RawMessage(`{"titulo":"compras","items":["leche","pan","huevos"]}`)
	found, err := updateDocument(s, coll, id, "", newData)
	if err != nil {
		t.Fatalf("updateDocument: %v", err)
	}
	if !found {
		t.Fatal("expected updateDocument to find the item")
	}

	docs, _ = listDocuments(s, coll, 10, 0, nil, nil)
	if string(docs[0].Data) != string(newData) {
		t.Fatalf("expected updated data %s, got %s", newData, docs[0].Data)
	}

	found, err = deleteDocument(s, coll, id)
	if err != nil {
		t.Fatalf("deleteDocument: %v", err)
	}
	if !found {
		t.Fatal("expected deleteDocument to find the item")
	}

	docs, _ = listDocuments(s, coll, 10, 0, nil, nil)
	if len(docs) != 0 {
		t.Fatalf("expected empty collection after delete, got %v", docs)
	}
}

func TestVectorCollectionSearchAndTextRequired(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "docs", true, 768, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, err := getCollection(s, "docs")
	if err != nil {
		t.Fatalf("getCollection: %v", err)
	}

	texts := map[string]json.RawMessage{
		"el gato duerme en el sofá":          json.RawMessage(`{"tag":"animales"}`),
		"un felino descansa sobre el mueble": json.RawMessage(`{"tag":"animales"}`),
		"la bolsa de valores subió hoy":      json.RawMessage(`{"tag":"finanzas"}`),
	}
	var catID int64
	for text, data := range texts {
		id, err := insertDocument(s, coll, nil, text, data)
		if err != nil {
			t.Fatalf("insertDocument: %v", err)
		}
		if text == "el gato duerme en el sofá" {
			catID = id
		}
	}

	docs, err := searchDocuments(s, coll, "un gato tomando una siesta", 1, true, nil, nil)
	if err != nil {
		t.Fatalf("searchDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 result, got %d", len(docs))
	}
	if docs[0].ID != catID && docs[0].Text != "un felino descansa sobre el mueble" {
		t.Fatalf("expected a cat-related result, got %+v", docs[0])
	}
	if docs[0].Distance == nil {
		t.Fatal("expected distance to be set")
	}

	// non-vector collection: search must be rejected, not silently empty
	if err := createCollection(s, "notas", false, 0, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	plain, _ := getCollection(s, "notas")
	if _, err := searchDocuments(s, plain, "algo", 1, true, nil, nil); err != ErrCollectionNotVector {
		t.Fatalf("expected ErrCollectionNotVector, got %v", err)
	}
}
