package main

import (
	"errors"
	"net/http"
)

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// handleCreateUser: POST /users {"username":"...","password":"...","role":"admin|read-only"}
// Admin-only.
func handleCreateUser(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createUserRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		if err := createUser(store, req.Username, req.Password, req.Role); err != nil {
			if errors.Is(err, ErrUserExists) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, UserInfo{Username: req.Username, Role: req.Role})
	}
}

// handleListUsers: GET /users. Admin-only. Never includes password hashes.
func handleListUsers(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := listUsers(store)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, users)
	}
}

// handleDeleteUser: DELETE /users/{username}. Admin-only. Refuses to
// remove the last admin (409) — there's no recovery path if that happens.
func handleDeleteUser(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.PathValue("username")
		found, err := deleteUser(store, username)
		if err != nil {
			if errors.Is(err, ErrLastAdmin) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
