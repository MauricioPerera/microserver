package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestHTTPCollectionsFullFlow(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	// create a non-vector collection
	body, _ := json.Marshal(createCollectionRequest{Name: "notas", Vector: false})
	resp := doAuthed(t, http.MethodPost, server.URL+"/collections", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /collections status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// duplicate -> 409
	resp = doAuthed(t, http.MethodPost, server.URL+"/collections", body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate collection, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// bad name -> 400
	badBody, _ := json.Marshal(createCollectionRequest{Name: "bad name", Vector: false})
	resp = doAuthed(t, http.MethodPost, server.URL+"/collections", badBody)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad name, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// insert a document without text (fine, non-vector)
	docBody, _ := json.Marshal(documentRequest{Data: json.RawMessage(`{"titulo":"compras"}`)})
	resp = doAuthed(t, http.MethodPost, server.URL+"/collections/notas/items", docBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /collections/notas/items status=%d", resp.StatusCode)
	}
	var inserted insertResponse
	json.NewDecoder(resp.Body).Decode(&inserted)
	resp.Body.Close()

	// list documents
	resp = doAuthed(t, http.MethodGet, server.URL+"/collections/notas/items", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /collections/notas/items status=%d", resp.StatusCode)
	}
	var docs []Document
	json.NewDecoder(resp.Body).Decode(&docs)
	resp.Body.Close()
	if len(docs) != 1 || docs[0].ID != inserted.ID {
		t.Fatalf("expected 1 doc with id=%d, got %v", inserted.ID, docs)
	}

	// search on a non-vector collection -> 400
	resp = doAuthed(t, http.MethodGet, server.URL+"/collections/notas/search?q=algo", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 searching a non-vector collection, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// create a vector collection and exercise insert+search
	vecBody, _ := json.Marshal(createCollectionRequest{Name: "docs", Vector: true, Dimensions: 768})
	resp = doAuthed(t, http.MethodPost, server.URL+"/collections", vecBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /collections (vector) status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// missing text on a vector collection -> 400
	noText, _ := json.Marshal(documentRequest{Data: json.RawMessage(`{}`)})
	resp = doAuthed(t, http.MethodPost, server.URL+"/collections/docs/items", noText)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing text on vector collection, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	catBody, _ := json.Marshal(documentRequest{Text: "el gato duerme en el sofá"})
	resp = doAuthed(t, http.MethodPost, server.URL+"/collections/docs/items", catBody)
	var catDoc insertResponse
	json.NewDecoder(resp.Body).Decode(&catDoc)
	resp.Body.Close()

	stockBody, _ := json.Marshal(documentRequest{Text: "la bolsa de valores subió hoy"})
	resp = doAuthed(t, http.MethodPost, server.URL+"/collections/docs/items", stockBody)
	resp.Body.Close()

	resp = doAuthed(t, http.MethodGet, server.URL+"/collections/docs/search?q=un+gato+durmiendo&limit=1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /collections/docs/search status=%d", resp.StatusCode)
	}
	var results []Document
	json.NewDecoder(resp.Body).Decode(&results)
	resp.Body.Close()
	if len(results) != 1 || results[0].ID != catDoc.ID {
		t.Fatalf("expected top result id=%d, got %v", catDoc.ID, results)
	}

	// delete the vector collection, then confirm its items are gone (404)
	resp = doAuthed(t, http.MethodDelete, server.URL+"/collections/docs", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /collections/docs status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doAuthed(t, http.MethodGet, server.URL+"/collections/docs/items", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 listing items of dropped collection, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// GET /collections should now show only "notas"
	resp = doAuthed(t, http.MethodGet, server.URL+"/collections", nil)
	var cols []Collection
	json.NewDecoder(resp.Body).Decode(&cols)
	resp.Body.Close()
	if len(cols) != 1 || cols[0].Name != "notas" {
		t.Fatalf("expected only [notas] left, got %v", cols)
	}

	// update the notas document
	updateBody, _ := json.Marshal(documentRequest{Data: json.RawMessage(`{"titulo":"compras","done":true}`)})
	resp = doAuthed(t, http.MethodPut, server.URL+"/collections/notas/items/"+strconv.FormatInt(inserted.ID, 10), updateBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /collections/notas/items/%d status=%d", inserted.ID, resp.StatusCode)
	}
	resp.Body.Close()

	// delete it
	resp = doAuthed(t, http.MethodDelete, server.URL+"/collections/notas/items/"+strconv.FormatInt(inserted.ID, 10), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /collections/notas/items/%d status=%d", inserted.ID, resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHTTPBulkInsertDocuments(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	body, _ := json.Marshal(createCollectionRequest{Name: "posts", Vector: true, Dimensions: 768})
	resp := doAuthed(t, http.MethodPost, server.URL+"/collections", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /collections status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	docs := []documentRequest{
		{Text: "primer post", Data: json.RawMessage(`{"autor":"ana"}`)},
		{Text: "segundo post", Data: json.RawMessage(`{"autor":"beto"}`)},
	}
	body, _ = json.Marshal(docs)
	resp = doAuthed(t, http.MethodPost, server.URL+"/collections/posts/items/bulk", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /collections/posts/items/bulk status=%d", resp.StatusCode)
	}
	var results []insertResponse
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decoding bulk insert response: %v", err)
	}
	resp.Body.Close()
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	coll, err := getCollection(store, "posts")
	if err != nil {
		t.Fatalf("getCollection: %v", err)
	}
	listed, err := listDocuments(store, coll, 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("listDocuments: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(listed))
	}

	// missing text on a vector collection rolls back the whole batch
	badBody, _ := json.Marshal([]documentRequest{
		{Text: "tercer post"},
		{Data: json.RawMessage(`{"autor":"sin texto"}`)},
	})
	resp = doAuthed(t, http.MethodPost, server.URL+"/collections/posts/items/bulk", badBody)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for item missing text, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	listed, err = listDocuments(store, coll, 10, 0, nil, nil)
	if err != nil {
		t.Fatalf("listDocuments: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected still only 2 documents after rejected batch, got %d", len(listed))
	}
}

func TestHTTPCollectionInjectionAttemptRejected(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	body, _ := json.Marshal(createCollectionRequest{Name: `x"; DROP TABLE collections; --`, Vector: false})
	resp := doAuthed(t, http.MethodPost, server.URL+"/collections", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 rejecting injection attempt, got %d", resp.StatusCode)
	}

	// collections table must still be usable
	resp2 := doAuthed(t, http.MethodGet, server.URL+"/collections", nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected /collections to still work after injection attempt, got %d", resp2.StatusCode)
	}
	var buf bytes.Buffer
	buf.ReadFrom(resp2.Body)
}
