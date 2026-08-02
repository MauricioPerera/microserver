package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
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

// bulkMaxItems caps how many items a single bulk insert request may carry —
// each one embeds a text via a network call to Ollama, so an unbounded
// batch would tie up the request (and the single write connection) for an
// unpredictable amount of time.
const bulkMaxItems = 100

type searchResult struct {
	ID       int64   `json:"id"`
	Text     string  `json:"text"`
	Distance float64 `json:"distance"`
}

// loginRateLimit: 5 attempts/min per IP (burst 5, refilling at 5/60 per
// second) — generous enough for a legitimate user fumbling their password
// a couple of times, tight enough to make brute-forcing impractical. There
// is no account lockout otherwise, so this is the only thing standing
// between an attacker and unlimited password guesses.
const (
	loginRateLimitPerSecond = 5.0 / 60.0
	loginRateLimitBurst     = 5
)

// newRouter wires up all routes. /health and /login are public; everything
// else requires a valid bearer token issued by /login. Reads (GET) are
// open to any authenticated user regardless of role; writes (POST/PUT/
// DELETE) and all of /users additionally require the admin role — a
// read-only user gets 403 on those, not 401. /login gets its own rate
// limiter (see loginRateLimit* above) — general API rate limiting and body
// size limits are applied outside this function, as middleware wrapping
// the handler main.go actually serves, so they don't affect callers
// (tests included) that exercise this router directly.
func newRouter(store *VecStore, auth AuthConfig) http.Handler {
	loginLimiter := newRateLimiter(loginRateLimitPerSecond, loginRateLimitBurst)

	// admin composes requireAuth+requireAdmin so route registrations below
	// read as "auth(...)" for any authenticated user, "admin(...)" for
	// admin-only, without repeating the nesting at each call site.
	authOnly := func(h http.Handler) http.Handler { return requireAuth(auth, h) }
	admin := func(h http.Handler) http.Handler { return requireAuth(auth, requireAdmin(h)) }

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	mux.Handle("POST /login", rateLimitMiddleware(loginLimiter, handleLogin(auth, store)))

	mux.Handle("GET /items", authOnly(handleList(store)))
	mux.Handle("POST /items", admin(handleInsert(store)))
	mux.Handle("POST /items/bulk", admin(handleBulkInsert(store)))
	mux.Handle("PUT /items/{id}", admin(handleUpdate(store)))
	mux.Handle("DELETE /items/{id}", admin(handleDelete(store)))
	mux.Handle("GET /search", authOnly(handleSearch(store)))

	mux.Handle("POST /collections", admin(handleCreateCollection(store)))
	mux.Handle("GET /collections", authOnly(handleListCollections(store)))
	mux.Handle("DELETE /collections/{name}", admin(handleDropCollection(store)))
	mux.Handle("PUT /collections/{name}/rename", admin(handleRenameCollection(store)))
	mux.Handle("POST /collections/{name}/items", admin(handleInsertDocument(store)))
	mux.Handle("POST /collections/{name}/items/bulk", admin(handleBulkInsertDocument(store)))
	mux.Handle("GET /collections/{name}/items", authOnly(handleListDocuments(store)))
	mux.Handle("GET /collections/{name}/items/{id}", authOnly(handleGetDocument(store)))
	mux.Handle("PUT /collections/{name}/items/{id}", admin(handleUpdateDocument(store)))
	mux.Handle("DELETE /collections/{name}/items/{id}", admin(handleDeleteDocument(store)))
	mux.Handle("GET /collections/{name}/search", authOnly(handleSearchDocuments(store)))
	mux.Handle("GET /collections/{name}/aggregate", authOnly(handleAggregate(store)))
	mux.Handle("GET /collections/{name}/fulltext", authOnly(handleFullTextSearch(store)))
	mux.Handle("GET /collections/{name}/export", authOnly(handleExportDocuments(store)))

	mux.Handle("PUT /users/me/password", authOnly(handleChangePassword(store)))
	mux.Handle("POST /users", admin(handleCreateUser(store)))
	mux.Handle("GET /users", admin(handleListUsers(store)))
	mux.Handle("DELETE /users/{username}", admin(handleDeleteUser(store)))
	mux.Handle("PUT /users/{username}/password", admin(handleResetPassword(store)))
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
		if !decodeJSON(w, r, &req) {
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

// handleBulkInsert: POST /items/bulk [{"text": "...", "id": 123?}, ...]
// Atomic: every item is embedded first, then all inserted in a single
// transaction, so a caller never has to reconcile a partially-applied
// batch. Limited to bulkMaxItems items per request.
func handleBulkInsert(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var reqs []insertRequest
		if !decodeJSON(w, r, &reqs) {
			return
		}
		if len(reqs) == 0 {
			writeError(w, http.StatusBadRequest, "at least one item is required")
			return
		}
		if len(reqs) > bulkMaxItems {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d items per bulk request", bulkMaxItems))
			return
		}

		items := make([]BulkItem, len(reqs))
		for i, req := range reqs {
			if req.Text == "" {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("item %d: text is required", i))
				return
			}
			items[i] = BulkItem{ID: req.ID, Text: req.Text}
		}

		ids, err := insertTextBulk(store, items)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		results := make([]insertResponse, len(ids))
		for i, id := range ids {
			results[i] = insertResponse{ID: id}
		}
		writeJSON(w, http.StatusCreated, results)
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
		if !decodeJSON(w, r, &req) {
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
		slog.Error("writing JSON response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
