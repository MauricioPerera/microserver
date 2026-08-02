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

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword: PUT /users/me/password. Any authenticated user, not
// just admins — identifies the target user from the token (r.Context()),
// never from a path parameter, so there's no way to change someone else's
// password through this endpoint.
func handleChangePassword(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ac, ok := r.Context().Value(authContextKey).(authContext)
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}

		var req changePasswordRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		if err := changePassword(store, ac.Username, req.CurrentPassword, req.NewPassword); err != nil {
			switch {
			case errors.Is(err, ErrWrongPassword):
				writeError(w, http.StatusUnauthorized, err.Error())
			case errors.Is(err, ErrPasswordTooShort):
				writeError(w, http.StatusBadRequest, err.Error())
			default:
				writeError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type resetPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// handleResetPassword: PUT /users/{username}/password. Admin-only — sets a
// new password for another user without requiring their old one, for a
// user who's locked out and can't use PUT /users/me/password themselves.
func handleResetPassword(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.PathValue("username")

		var req resetPasswordRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		if err := resetPassword(store, username, req.NewPassword); err != nil {
			switch {
			case errors.Is(err, ErrUserNotFound):
				writeError(w, http.StatusNotFound, err.Error())
			case errors.Is(err, ErrPasswordTooShort):
				writeError(w, http.StatusBadRequest, err.Error())
			default:
				writeError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
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
