package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

type createCollectionRequest struct {
	Name       string                   `json:"name"`
	Vector     bool                     `json:"vector"`
	Dimensions int                      `json:"dimensions"`
	References map[string]ReferenceSpec `json:"references,omitempty"`
}

// handleCreateCollection: POST /collections
// {"name":"...", "vector":bool, "dimensions":N, "references":{"field":{"collection":"...","on_delete":"restrict|set_null"}}}
// dimensions is required (and must be 1..8192) when vector is true. Every
// referenced collection must already exist.
func handleCreateCollection(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createCollectionRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}

		if err := createCollection(store, req.Name, req.Vector, req.Dimensions, req.References); err != nil {
			if errors.Is(err, ErrCollectionExists) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, Collection{Name: req.Name, HasVector: req.Vector, Dimensions: req.Dimensions})
	}
}

// handleListCollections: GET /collections
func handleListCollections(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cols, err := listCollections(store)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, cols)
	}
}

// handleDropCollection: DELETE /collections/{name}
func handleDropCollection(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := dropCollection(store, name); err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				writeError(w, http.StatusNotFound, "collection not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type documentRequest struct {
	ID   *int64          `json:"id,omitempty"`
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// handleInsertDocument: POST /collections/{name}/items {"id":123?, "text":"..."?, "data":{...}?}
// text is required only if the collection has vectors.
func handleInsertDocument(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		coll, err := getCollection(store, r.PathValue("name"))
		if err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				writeError(w, http.StatusNotFound, "collection not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		var req documentRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if coll.HasVector && req.Text == "" {
			writeError(w, http.StatusBadRequest, "text is required for vector collections")
			return
		}

		id, err := insertDocument(store, coll, req.ID, req.Text, req.Data)
		if err != nil {
			writeDocumentError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, insertResponse{ID: id})
	}
}

// handleBulkInsertDocument: POST /collections/{name}/items/bulk
// [{"id":123?, "text":"..."?, "data":{...}?}, ...]
// Same all-or-nothing semantics as the fixed table's bulk insert. Limited
// to bulkMaxItems items per request.
func handleBulkInsertDocument(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		coll, err := getCollection(store, r.PathValue("name"))
		if err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				writeError(w, http.StatusNotFound, "collection not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		var reqs []documentRequest
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

		items := make([]BulkDocumentItem, len(reqs))
		for i, req := range reqs {
			if coll.HasVector && req.Text == "" {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("item %d: text is required for vector collections", i))
				return
			}
			items[i] = BulkDocumentItem{ID: req.ID, Text: req.Text, Data: req.Data}
		}

		ids, err := insertDocumentBulk(store, coll, items)
		if err != nil {
			writeDocumentError(w, err)
			return
		}

		results := make([]insertResponse, len(ids))
		for i, id := range ids {
			results[i] = insertResponse{ID: id}
		}
		writeJSON(w, http.StatusCreated, results)
	}
}

// handleGetDocument: GET /collections/{name}/items/{id}
func handleGetDocument(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		coll, err := getCollection(store, r.PathValue("name"))
		if err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				writeError(w, http.StatusNotFound, "collection not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "id must be an integer")
			return
		}

		doc, err := getDocumentByID(store, coll, id)
		if err != nil {
			if errors.Is(err, ErrDocumentNotFound) {
				writeError(w, http.StatusNotFound, "item not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, doc)
	}
}

// handleListDocuments: GET /collections/{name}/items?limit=100&offset=0
// Any other query param filters by a top-level field of data: "campo=valor"
// (equals) or "campo__op=valor" with op in eq/ne/lt/lte/gt/gte/like.
func handleListDocuments(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		coll, err := getCollection(store, r.PathValue("name"))
		if err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				writeError(w, http.StatusNotFound, "collection not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		limit, offset, ok := parseLimitOffset(w, r)
		if !ok {
			return
		}

		filters, err := parseFilters(r.URL.Query())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		sort, err := parseSort(r.URL.Query())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		docs, err := listDocuments(store, coll, limit, offset, filters, sort)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, docs)
	}
}

// handleUpdateDocument: PUT /collections/{name}/items/{id} {"text":"..."?, "data":{...}?}
func handleUpdateDocument(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		coll, err := getCollection(store, r.PathValue("name"))
		if err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				writeError(w, http.StatusNotFound, "collection not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "id must be an integer")
			return
		}

		var req documentRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if coll.HasVector && req.Text == "" {
			writeError(w, http.StatusBadRequest, "text is required for vector collections")
			return
		}

		found, err := updateDocument(store, coll, id, req.Text, req.Data)
		if err != nil {
			writeDocumentError(w, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		writeJSON(w, http.StatusOK, insertResponse{ID: id})
	}
}

// handleDeleteDocument: DELETE /collections/{name}/items/{id}
func handleDeleteDocument(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		coll, err := getCollection(store, r.PathValue("name"))
		if err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				writeError(w, http.StatusNotFound, "collection not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "id must be an integer")
			return
		}

		found, err := deleteDocument(store, coll, id)
		if err != nil {
			writeDocumentError(w, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "item not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleAggregate: GET /collections/{name}/aggregate?op=sum&field=precio&group_by=categoria
// op is required: count, sum, avg, min, max. field is required except for
// count (bare count counts all matching rows; count with field counts
// non-null occurrences of it). group_by is optional. Any other query param
// filters rows before aggregating, same syntax as GET .../items.
func handleAggregate(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		coll, err := getCollection(store, r.PathValue("name"))
		if err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				writeError(w, http.StatusNotFound, "collection not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		op := r.URL.Query().Get("op")
		if op == "" {
			writeError(w, http.StatusBadRequest, "op is required")
			return
		}
		field := r.URL.Query().Get("field")
		groupBy := r.URL.Query().Get("group_by")

		filters, err := parseFilters(r.URL.Query())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		results, err := aggregateDocuments(store, coll, op, field, groupBy, filters)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, results)
	}
}

// handleFullTextSearch: GET /collections/{name}/fulltext?q=texto&limit=10&offset=0
// q is FTS5 query syntax as-is: AND/OR/NOT, "exact phrase", prefix* etc.
// Only valid for vector collections (only those have a text field) — 400
// otherwise. Accepts the same filters/sort as GET /collections/{name}/items.
func handleFullTextSearch(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		coll, err := getCollection(store, r.PathValue("name"))
		if err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				writeError(w, http.StatusNotFound, "collection not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		q := r.URL.Query().Get("q")
		if q == "" {
			writeError(w, http.StatusBadRequest, "q is required")
			return
		}

		limit, offset, ok := parseLimitOffset(w, r)
		if !ok {
			return
		}

		filters, err := parseFilters(r.URL.Query())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		sort, err := parseSort(r.URL.Query())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		docs, err := fullTextSearchDocuments(store, coll, q, limit, offset, filters, sort)
		if err != nil {
			if errors.Is(err, ErrCollectionNoFullText) {
				writeError(w, http.StatusBadRequest, "collection has no text field, nothing to search")
				return
			}
			// Any other error here is almost certainly malformed FTS5 query
			// syntax from the client (unbalanced quotes, bad operator), not
			// a server-side problem.
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, docs)
	}
}

// handleSearchDocuments: GET /collections/{name}/search?q=texto&limit=10&rerank=true
// Any other query param filters by a top-level field of data, same syntax
// as GET /collections/{name}/items. Combined with vector search, this is
// best-effort, not exact — see searchDocuments' doc comment.
func handleSearchDocuments(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		coll, err := getCollection(store, r.PathValue("name"))
		if err != nil {
			if errors.Is(err, ErrCollectionNotFound) {
				writeError(w, http.StatusNotFound, "collection not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

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

		filters, err := parseFilters(r.URL.Query())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		sort, err := parseSort(r.URL.Query())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		docs, err := searchDocuments(store, coll, q, limit, rerank, filters, sort)
		if err != nil {
			if errors.Is(err, ErrCollectionNotVector) {
				writeError(w, http.StatusBadRequest, "collection has no vectors, nothing to search")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, docs)
	}
}

// writeDocumentError maps the errors that insert/update/delete can return
// to the right HTTP status: bad reference input is a 400, a blocked delete
// (restrict) is a 409, everything else is a 500.
func writeDocumentError(w http.ResponseWriter, err error) {
	var valErr *ReferenceValidationError
	if errors.As(err, &valErr) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var constraintErr *ReferentialConstraintError
	if errors.As(err, &constraintErr) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func parseLimitOffset(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	limit = defaultListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > maxListLimit {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer up to 1000")
			return 0, 0, false
		}
		limit = n
	}
	offset = 0
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}
