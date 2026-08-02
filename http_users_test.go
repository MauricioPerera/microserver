package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPUserManagementAndRoles(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	// admin creates a read-only user
	body, _ := json.Marshal(createUserRequest{Username: "reader", Password: "readerpw1", Role: RoleReadOnly})
	resp := doAuthed(t, http.MethodPost, server.URL+"/users", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /users status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// duplicate -> 409
	resp = doAuthed(t, http.MethodPost, server.URL+"/users", body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate user, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// admin lists users, sees both admin (from token, not created via API
	// in this test) is irrelevant — just confirm reader shows up, no hash
	resp = doAuthed(t, http.MethodGet, server.URL+"/users", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /users status=%d", resp.StatusCode)
	}
	var users []UserInfo
	json.NewDecoder(resp.Body).Decode(&users)
	resp.Body.Close()
	found := false
	for _, u := range users {
		if u.Username == "reader" {
			found = true
			if u.Role != RoleReadOnly {
				t.Fatalf("expected reader role %q, got %q", RoleReadOnly, u.Role)
			}
		}
	}
	if !found {
		t.Fatalf("expected reader in user list, got %v", users)
	}

	// log in as reader for real, get a real read-only token
	loginBody, _ := json.Marshal(loginRequest{Username: "reader", Password: "readerpw1"})
	resp, err = http.Post(server.URL+"/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("POST /login as reader: %v", err)
	}
	var loginRes loginResponse
	json.NewDecoder(resp.Body).Decode(&loginRes)
	resp.Body.Close()
	if loginRes.Token == "" {
		t.Fatal("expected reader to receive a token")
	}

	readerReq := func(method, url string, body []byte) *http.Response {
		var r *bytes.Reader
		if body != nil {
			r = bytes.NewReader(body)
		} else {
			r = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, url, r)
		req.Header.Set("Authorization", "Bearer "+loginRes.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		return resp
	}

	// reader can GET /items
	resp = readerReq(http.MethodGet, server.URL+"/items", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected reader GET /items to succeed, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// reader cannot POST /items -> 403, not 401 (they ARE authenticated,
	// just not authorized)
	insertBody, _ := json.Marshal(insertRequest{Text: "should not be allowed"})
	resp = readerReq(http.MethodPost, server.URL+"/items", insertBody)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected reader POST /items to be 403, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// reader cannot manage users either
	resp = readerReq(http.MethodGet, server.URL+"/users", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected reader GET /users to be 403, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// admin deletes reader
	resp = doAuthed(t, http.MethodDelete, server.URL+"/users/reader", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /users/reader status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// deleting again -> 404
	resp = doAuthed(t, http.MethodDelete, server.URL+"/users/reader", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on second delete, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHTTPCreateUserValidationErrors(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	body, _ := json.Marshal(createUserRequest{Username: "x", Password: "short", Role: RoleAdmin})
	resp := doAuthed(t, http.MethodPost, server.URL+"/users", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for too-short password, got %d", resp.StatusCode)
	}
}
