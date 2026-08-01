package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
)

func TestReferenceValidationOnInsert(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "autores", false, 0, nil); err != nil {
		t.Fatalf("createCollection autores: %v", err)
	}
	autor, err := getCollection(s, "autores")
	if err != nil {
		t.Fatalf("getCollection autores: %v", err)
	}
	autorID, err := insertDocument(s, autor, nil, "", json.RawMessage(`{"nombre":"Ana"}`))
	if err != nil {
		t.Fatalf("insertDocument autor: %v", err)
	}

	refs := map[string]ReferenceSpec{
		"autor_id": {Collection: "autores"},
	}
	if err := createCollection(s, "publicaciones", false, 0, refs); err != nil {
		t.Fatalf("createCollection publicaciones: %v", err)
	}
	pub, err := getCollection(s, "publicaciones")
	if err != nil {
		t.Fatalf("getCollection publicaciones: %v", err)
	}

	// valid reference: works
	postID, err := insertDocument(s, pub, nil, "", json.RawMessage(`{"titulo":"Hola","autor_id":`+strconv.FormatInt(autorID, 10)+`}`))
	if err != nil {
		t.Fatalf("insertDocument with valid reference: %v", err)
	}

	// invalid reference: rejected with ReferenceValidationError
	_, err = insertDocument(s, pub, nil, "", json.RawMessage(`{"titulo":"Chau","autor_id":9999}`))
	if err == nil {
		t.Fatal("expected error inserting with a nonexistent autor_id")
	}
	var valErr *ReferenceValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("expected ReferenceValidationError, got %T: %v", err, err)
	}

	// null reference: allowed (optional)
	if _, err := insertDocument(s, pub, nil, "", json.RawMessage(`{"titulo":"Sin autor","autor_id":null}`)); err != nil {
		t.Fatalf("expected null reference to be allowed, got %v", err)
	}

	// missing reference field entirely: also allowed
	if _, err := insertDocument(s, pub, nil, "", json.RawMessage(`{"titulo":"Tampoco autor"}`)); err != nil {
		t.Fatalf("expected missing reference field to be allowed, got %v", err)
	}

	_ = postID
}

func TestReferenceRestrictBlocksDelete(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "autores", false, 0, nil); err != nil {
		t.Fatalf("createCollection autores: %v", err)
	}
	autor, _ := getCollection(s, "autores")
	autorID, err := insertDocument(s, autor, nil, "", json.RawMessage(`{"nombre":"Ana"}`))
	if err != nil {
		t.Fatalf("insertDocument autor: %v", err)
	}

	refs := map[string]ReferenceSpec{
		"autor_id": {Collection: "autores", OnDelete: "restrict"},
	}
	if err := createCollection(s, "publicaciones", false, 0, refs); err != nil {
		t.Fatalf("createCollection publicaciones: %v", err)
	}
	pub, _ := getCollection(s, "publicaciones")
	if _, err := insertDocument(s, pub, nil, "", json.RawMessage(`{"titulo":"Hola","autor_id":`+strconv.FormatInt(autorID, 10)+`}`)); err != nil {
		t.Fatalf("insertDocument publicacion: %v", err)
	}

	// deleting the author must be blocked
	_, err = deleteDocument(s, autor, autorID)
	if err == nil {
		t.Fatal("expected delete to be blocked by restrict reference")
	}
	var constraintErr *ReferentialConstraintError
	if !errors.As(err, &constraintErr) {
		t.Fatalf("expected ReferentialConstraintError, got %T: %v", err, err)
	}

	// author must still exist
	if _, err := getDocumentByID(s, autor, autorID); err != nil {
		t.Fatalf("expected author to still exist after blocked delete, got %v", err)
	}
}

func TestReferenceSetNullOnDelete(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "autores", false, 0, nil); err != nil {
		t.Fatalf("createCollection autores: %v", err)
	}
	autor, _ := getCollection(s, "autores")
	autorID, err := insertDocument(s, autor, nil, "", json.RawMessage(`{"nombre":"Ana"}`))
	if err != nil {
		t.Fatalf("insertDocument autor: %v", err)
	}

	refs := map[string]ReferenceSpec{
		"autor_id": {Collection: "autores", OnDelete: "set_null"},
	}
	if err := createCollection(s, "publicaciones", false, 0, refs); err != nil {
		t.Fatalf("createCollection publicaciones: %v", err)
	}
	pub, _ := getCollection(s, "publicaciones")
	postID, err := insertDocument(s, pub, nil, "", json.RawMessage(`{"titulo":"Hola","autor_id":`+strconv.FormatInt(autorID, 10)+`}`))
	if err != nil {
		t.Fatalf("insertDocument publicacion: %v", err)
	}

	found, err := deleteDocument(s, autor, autorID)
	if err != nil {
		t.Fatalf("expected delete to succeed with set_null, got %v", err)
	}
	if !found {
		t.Fatal("expected author to be found and deleted")
	}

	doc, err := getDocumentByID(s, pub, postID)
	if err != nil {
		t.Fatalf("getDocumentByID publicacion: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc.Data, &m); err != nil {
		t.Fatalf("unmarshaling data: %v", err)
	}
	if string(m["autor_id"]) != "null" {
		t.Fatalf("expected autor_id to be null after set_null delete, got %s", m["autor_id"])
	}
	if string(m["titulo"]) != `"Hola"` {
		t.Fatalf("expected other fields to survive set_null, got %s", doc.Data)
	}
}

func TestReferenceTargetCollectionMustExist(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	refs := map[string]ReferenceSpec{
		"autor_id": {Collection: "no_existe"},
	}
	if err := createCollection(s, "publicaciones", false, 0, refs); err == nil {
		t.Fatal("expected error creating a collection that references a nonexistent collection")
	}
}

func TestGetDocumentByID(t *testing.T) {
	s, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := createCollection(s, "notas", false, 0, nil); err != nil {
		t.Fatalf("createCollection: %v", err)
	}
	coll, _ := getCollection(s, "notas")
	id, err := insertDocument(s, coll, nil, "", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("insertDocument: %v", err)
	}

	doc, err := getDocumentByID(s, coll, id)
	if err != nil {
		t.Fatalf("getDocumentByID: %v", err)
	}
	if doc.ID != id {
		t.Fatalf("expected id=%d, got %d", id, doc.ID)
	}

	if _, err := getDocumentByID(s, coll, id+999); err != ErrDocumentNotFound {
		t.Fatalf("expected ErrDocumentNotFound, got %v", err)
	}
}
