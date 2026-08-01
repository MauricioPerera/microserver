package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func testAuth() AuthConfig {
	return AuthConfig{
		Username: "admin",
		Password: "s3cret",
		Secret:   []byte("fixed-test-secret-not-random-ok"),
		TTL:      time.Hour,
	}
}

func authedRequest(t *testing.T, method, url string, body []byte) *http.Request {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testAuth().generateToken(testAuth().Username))
	return req
}

func doAuthed(t *testing.T, method, url string, body []byte) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(authedRequest(t, method, url, body))
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func TestHTTPHealth(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHTTPLogin(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	t.Run("valid credentials", func(t *testing.T) {
		body, _ := json.Marshal(loginRequest{Username: "admin", Password: "s3cret"})
		resp, err := http.Post(server.URL+"/login", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /login: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var res loginResponse
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("decoding login response: %v", err)
		}
		if res.Token == "" {
			t.Fatal("expected non-empty token")
		}

		// the issued token should work against a protected endpoint
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/items", nil)
		req.Header.Set("Authorization", "Bearer "+res.Token)
		resp2, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /items with issued token: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 using issued token, got %d", resp2.StatusCode)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		body, _ := json.Marshal(loginRequest{Username: "admin", Password: "wrong"})
		resp, err := http.Post(server.URL+"/login", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /login: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})
}

func TestHTTPProtectedRequiresAuth(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	t.Run("no header", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/items")
		if err != nil {
			t.Fatalf("GET /items: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("garbage token", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/items", nil)
		req.Header.Set("Authorization", "Bearer not-a-real-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /items: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		expired := testAuth()
		expired.TTL = -time.Hour
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/items", nil)
		req.Header.Set("Authorization", "Bearer "+expired.generateToken(expired.Username))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /items: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})
}

func TestHTTPInsertAndSearch(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	insert := func(text string) int64 {
		body, _ := json.Marshal(insertRequest{Text: text})
		resp := doAuthed(t, http.MethodPost, server.URL+"/items", body)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST /items status=%d", resp.StatusCode)
		}
		var res insertResponse
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("decoding insert response: %v", err)
		}
		if res.ID == 0 {
			t.Fatal("expected non-zero auto-assigned id")
		}
		return res.ID
	}

	insert("el gato duerme en el sofá")
	insert("un felino descansa sobre el mueble")
	insert("la bolsa de valores subió hoy")

	q := url.Values{}
	q.Set("q", "un gato tomando una siesta")
	q.Set("limit", "1")

	resp := doAuthed(t, http.MethodGet, server.URL+"/search?"+q.Encode(), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var results []searchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decoding search response: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	t.Logf("top result: id=%d distance=%f", results[0].ID, results[0].Distance)
}

func TestHTTPList(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	texts := []string{"el gato duerme en el sofá", "un felino descansa sobre el mueble", "la bolsa de valores subió hoy"}
	var ids []int64
	for _, text := range texts {
		body, _ := json.Marshal(insertRequest{Text: text})
		resp := doAuthed(t, http.MethodPost, server.URL+"/items", body)
		var res insertResponse
		json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		ids = append(ids, res.ID)
	}

	resp := doAuthed(t, http.MethodGet, server.URL+"/items", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var listed []Item
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(listed) != len(ids) {
		t.Fatalf("expected %d ids, got %d: %v", len(ids), len(listed), listed)
	}
	for i, it := range listed {
		if it.Text != texts[0] && it.Text != texts[1] && it.Text != texts[2] {
			t.Fatalf("item %d: unexpected text %q", i, it.Text)
		}
	}

	// pagination: limit=1&offset=1 should return exactly the second id
	resp2 := doAuthed(t, http.MethodGet, server.URL+"/items?limit=1&offset=1", nil)
	defer resp2.Body.Close()
	var page []Item
	json.NewDecoder(resp2.Body).Decode(&page)
	if len(page) != 1 || page[0].ID != listed[1].ID {
		t.Fatalf("expected page [%v], got %v", listed[1], page)
	}
}

func TestHTTPUpdate(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	body, _ := json.Marshal(insertRequest{Text: "la bolsa de valores subió hoy"})
	resp := doAuthed(t, http.MethodPost, server.URL+"/items", body)
	var inserted insertResponse
	json.NewDecoder(resp.Body).Decode(&inserted)
	resp.Body.Close()

	updateBody, _ := json.Marshal(insertRequest{Text: "el gato duerme en el sofá"})
	resp2 := doAuthed(t, http.MethodPut, server.URL+"/items/"+strconv.FormatInt(inserted.ID, 10), updateBody)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}

	// search for the new content should now find this id
	q := url.Values{}
	q.Set("q", "un gato tomando una siesta")
	q.Set("limit", "1")
	resp3 := doAuthed(t, http.MethodGet, server.URL+"/search?"+q.Encode(), nil)
	defer resp3.Body.Close()
	var results []searchResult
	json.NewDecoder(resp3.Body).Decode(&results)
	if len(results) != 1 || results[0].ID != inserted.ID {
		t.Fatalf("expected updated item %d to be the top match, got %+v", inserted.ID, results)
	}
}

func TestHTTPUpdateNotFound(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	body, _ := json.Marshal(insertRequest{Text: "no existe"})
	resp := doAuthed(t, http.MethodPut, server.URL+"/items/999", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHTTPDelete(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	body, _ := json.Marshal(insertRequest{Text: "el gato duerme en el sofá"})
	resp := doAuthed(t, http.MethodPost, server.URL+"/items", body)
	var inserted insertResponse
	json.NewDecoder(resp.Body).Decode(&inserted)
	resp.Body.Close()

	resp2 := doAuthed(t, http.MethodDelete, server.URL+"/items/"+strconv.FormatInt(inserted.ID, 10), nil)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp2.StatusCode)
	}

	// deleting again should 404
	resp3 := doAuthed(t, http.MethodDelete, server.URL+"/items/"+strconv.FormatInt(inserted.ID, 10), nil)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on second delete, got %d", resp3.StatusCode)
	}

	// search should no longer find it
	q := url.Values{}
	q.Set("q", "un gato tomando una siesta")
	q.Set("limit", "10")
	resp4 := doAuthed(t, http.MethodGet, server.URL+"/search?"+q.Encode(), nil)
	defer resp4.Body.Close()
	var results []searchResult
	json.NewDecoder(resp4.Body).Decode(&results)
	for _, r := range results {
		if r.ID == inserted.ID {
			t.Fatalf("deleted id=%d still present in search results", inserted.ID)
		}
	}
}

func TestHTTPDeleteInvalidID(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	resp := doAuthed(t, http.MethodDelete, server.URL+"/items/not-a-number", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHTTPInsertMissingText(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	resp := doAuthed(t, http.MethodPost, server.URL+"/items", []byte(`{}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
