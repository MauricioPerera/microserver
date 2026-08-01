package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
)

type insertRequest struct {
	ID   int64  `json:"id,omitempty"`
	Text string `json:"text"`
}

type insertResponse struct {
	ID int64 `json:"id"`
}

type searchResult struct {
	ID       int64   `json:"id"`
	Text     string  `json:"text"`
	Distance float64 `json:"distance"`
}

// newRouter wires up all routes. /health and /login are public; everything
// else requires a valid bearer token issued by /login.
func newRouter(store *VecStore, auth AuthConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("POST /login", handleLogin(auth))

	mux.Handle("GET /items", requireAuth(auth, handleList(store)))
	mux.Handle("POST /items", requireAuth(auth, handleInsert(store)))
	mux.Handle("PUT /items/{id}", requireAuth(auth, handleUpdate(store)))
	mux.Handle("DELETE /items/{id}", requireAuth(auth, handleDelete(store)))
	mux.Handle("GET /search", requireAuth(auth, handleSearch(store)))

	mux.Handle("POST /collections", requireAuth(auth, handleCreateCollection(store)))
	mux.Handle("GET /collections", requireAuth(auth, handleListCollections(store)))
	mux.Handle("DELETE /collections/{name}", requireAuth(auth, handleDropCollection(store)))
	mux.Handle("POST /collections/{name}/items", requireAuth(auth, handleInsertDocument(store)))
	mux.Handle("GET /collections/{name}/items", requireAuth(auth, handleListDocuments(store)))
	mux.Handle("GET /collections/{name}/items/{id}", requireAuth(auth, handleGetDocument(store)))
	mux.Handle("PUT /collections/{name}/items/{id}", requireAuth(auth, handleUpdateDocument(store)))
	mux.Handle("DELETE /collections/{name}/items/{id}", requireAuth(auth, handleDeleteDocument(store)))
	mux.Handle("GET /collections/{name}/search", requireAuth(auth, handleSearchDocuments(store)))
	mux.Handle("GET /collections/{name}/aggregate", requireAuth(auth, handleAggregate(store)))
	return mux
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

// handleList: GET /items?limit=100&offset=0
func handleList(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := defaultListLimit
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 || n > maxListLimit {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be a positive integer up to %d", maxListLimit))
				return
			}
			limit = n
		}

		offset := 0
		if v := r.URL.Query().Get("offset"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
				return
			}
			offset = n
		}

		items, err := listItems(store, limit, offset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, items)
	}
}

// handleInsert: POST /items {"text": "...", "id": 123 (optional)}
// If id is omitted or 0, SQLite assigns one and it's returned in the body.
func handleInsert(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req insertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Text == "" {
			writeError(w, http.StatusBadRequest, "text is required")
			return
		}

		var id int64
		var err error
		if req.ID != 0 {
			id = req.ID
			err = insertText(store, req.ID, req.Text)
		} else {
			id, err = insertTextAuto(store, req.Text)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, insertResponse{ID: id})
	}
}

// handleUpdate: PUT /items/{id} {"text": "..."}
// Re-embeds text and overwrites the vectors for an existing item.
func handleUpdate(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "id must be an integer")
			return
		}

		var req insertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Text == "" {
			writeError(w, http.StatusBadRequest, "text is required")
			return
		}

		found, err := updateText(store, id, req.Text)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		writeJSON(w, http.StatusOK, insertResponse{ID: id})
	}
}

// handleDelete: DELETE /items/{id}
func handleDelete(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "id must be an integer")
			return
		}

		found, err := deleteItem(store, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleSearch: GET /search?q=texto&limit=10&rerank=true
// rerank defaults to true (full float32 quality). Pass rerank=false for the
// faster binary-only path (~20x faster at scale, ~5% lower accuracy).
func handleSearch(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			writeError(w, http.StatusBadRequest, "q is required")
			return
		}

		limit := 10
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				writeError(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			limit = n
		}

		rerank := true
		if v := r.URL.Query().Get("rerank"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "rerank must be true or false")
				return
			}
			rerank = b
		}

		rows, err := queryText(store, q, limit, rerank)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()

		results := []searchResult{}
		for rows.Next() {
			var res searchResult
			if err := rows.Scan(&res.ID, &res.Text, &res.Distance); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			results = append(results, res)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, results)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
