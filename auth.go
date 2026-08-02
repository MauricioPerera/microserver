package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// AuthConfig holds the token-signing key/TTL, plus the bootstrap admin
// credentials used only to seed the very first user (see
// ensureBootstrapAdmin in users.go). Users themselves live in the
// database, not here — this is not "the one set of credentials" anymore.
type AuthConfig struct {
	BootstrapUsername string
	BootstrapPassword string
	Secret            []byte
	TTL               time.Duration
}

// loadAuthConfig reads bootstrap credentials from AUTH_USERNAME/
// AUTH_PASSWORD. They're only required the very first time the server runs
// (empty users table) — see ensureBootstrapAdmin — but read unconditionally
// here since main.go doesn't know yet whether the database has users.
// The signing secret is random and in-memory only, so tokens (for every
// user, not just one) stop validating across a restart.
func loadAuthConfig() (AuthConfig, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return AuthConfig{}, fmt.Errorf("generating auth secret: %w", err)
	}
	return AuthConfig{
		BootstrapUsername: os.Getenv("AUTH_USERNAME"),
		BootstrapPassword: os.Getenv("AUTH_PASSWORD"),
		Secret:            secret,
		TTL:               time.Hour,
	}, nil
}

func (cfg AuthConfig) sign(payload string) []byte {
	mac := hmac.New(sha256.New, cfg.Secret)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// generateToken produces a "base64(username:role:expiry).base64(hmac)"
// token. Stateless: validity is checked purely from the signature and the
// embedded expiry, no server-side session store. Username is validated
// against collectionNameRe at user-creation time specifically so it can
// never contain ':', which would break this format.
func (cfg AuthConfig) generateToken(username, role string) string {
	payload := fmt.Sprintf("%s:%s:%d", username, role, time.Now().Add(cfg.TTL).Unix())
	sig := cfg.sign(payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (cfg AuthConfig) validateToken(token string) (username, role string, err error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", "", errors.New("malformed token")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", errors.New("malformed token")
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", errors.New("malformed token")
	}
	if !hmac.Equal(sigBytes, cfg.sign(string(payloadBytes))) {
		return "", "", errors.New("invalid token signature")
	}

	fields := strings.Split(string(payloadBytes), ":")
	if len(fields) != 3 {
		return "", "", errors.New("malformed token payload")
	}
	username, role = fields[0], fields[1]
	expiry, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", "", errors.New("malformed token expiry")
	}
	if time.Now().Unix() > expiry {
		return "", "", errors.New("token expired")
	}
	return username, role, nil
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string `json:"token"`
	ExpiresIn int64  `json:"expires_in"`
}

// handleLogin: POST /login {"username":"...","password":"..."}
// Looks the user up in the database and checks their bcrypt hash — unlike
// the single-shared-credential version this replaced, there's no
// AuthConfig field to compare against anymore.
func handleLogin(cfg AuthConfig, store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		u, err := getUser(store, req.Username)
		if err != nil {
			if errors.Is(err, ErrUserNotFound) {
				writeError(w, http.StatusUnauthorized, "invalid credentials")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !checkPassword(u, req.Password) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

		writeJSON(w, http.StatusOK, loginResponse{
			Token:     cfg.generateToken(u.Username, u.Role),
			ExpiresIn: int64(cfg.TTL.Seconds()),
		})
	}
}

type authContextKeyType struct{}

var authContextKey authContextKeyType

// authContext is what requireAuth attaches to the request context after
// validating a token — everything downstream (requireAdmin, handlers that
// want to know who's calling) reads it from there instead of re-parsing
// the token.
type authContext struct {
	Username string
	Role     string
}

// requireAuth wraps a handler, rejecting requests without a valid
// "Authorization: Bearer <token>" header. Any authenticated user, of
// either role, passes this — use requireAdmin on top of it to further
// restrict to admins.
func requireAuth(cfg AuthConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, prefix) {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		username, role, err := cfg.validateToken(strings.TrimPrefix(authHeader, prefix))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		ctx := context.WithValue(r.Context(), authContextKey, authContext{Username: username, Role: role})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdmin further restricts an already-requireAuth-wrapped handler to
// admin-role users. Must be nested inside requireAuth (i.e.
// requireAuth(cfg, requireAdmin(handler))) — it only reads the context
// requireAuth sets, it doesn't validate the token itself.
func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac, ok := r.Context().Value(authContextKey).(authContext)
		if !ok || ac.Role != RoleAdmin {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
