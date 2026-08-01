package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

type createCollectionRequest struct {
	Name       string `json:"name"`
	Vector     bool   `json:"vector"`
	Dimensions int    `json:"dimensions"`
}

// handleCreateCollection: POST /collections {"name":"...", "vector":bool, "dimensions":N}
// dimensions is required (and must be 1..8192) when vector is true.
func handleCreateCollection(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createCollectionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}

		if err := createCollection(store, req.Name, req.Vector, req.Dimensions); err != nil {
			switch {
			case errors.Is(err, ErrCollectionExists):
				writeError(w, http.StatusConflict, err.Error())
			case errors.Is(err, ErrInvalidCollectionName):
				writeError(w, http.StatusBadRequest, err.Error())
			default:
				writeError(w, http.StatusBadRequest, err.Error())
			}
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if coll.HasVector && req.Text == "" {
			writeError(w, http.StatusBadRequest, "text is required for vector collections")
			return
		}

		id, err := insertDocument(store, coll, req.ID, req.Text, req.Data)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, insertResponse{ID: id})
	}
}

// handleListDocuments: GET /collections/{name}/items?limit=100&offset=0
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

		docs, err := listDocuments(store, coll, limit, offset)
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if coll.HasVector && req.Text == "" {
			writeError(w, http.StatusBadRequest, "text is required for vector collections")
			return
		}

		found, err := updateDocument(store, coll, id, req.Text, req.Data)
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

// handleSearchDocuments: GET /collections/{name}/search?q=texto&limit=10&rerank=true
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

		docs, err := searchDocuments(store, coll, q, limit, rerank)
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
