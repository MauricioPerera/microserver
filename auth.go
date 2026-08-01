package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// AuthConfig holds the single configured user's credentials and the key
// used to sign issued tokens. There is no user table — this protects the
// API with one set of credentials, not multi-user accounts.
type AuthConfig struct {
	Username string
	Password string
	Secret   []byte
	TTL      time.Duration
}

// loadAuthConfig reads credentials from AUTH_USERNAME/AUTH_PASSWORD. Both
// are required — the server refuses to start without them rather than
// silently running unauthenticated. The signing secret is random and
// in-memory only, so tokens stop validating across a restart; that's fine
// since /login is cheap to call again.
func loadAuthConfig() (AuthConfig, error) {
	user := os.Getenv("AUTH_USERNAME")
	pass := os.Getenv("AUTH_PASSWORD")
	if user == "" || pass == "" {
		return AuthConfig{}, errors.New("AUTH_USERNAME and AUTH_PASSWORD environment variables must both be set")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return AuthConfig{}, fmt.Errorf("generating auth secret: %w", err)
	}
	return AuthConfig{Username: user, Password: pass, Secret: secret, TTL: time.Hour}, nil
}

func (cfg AuthConfig) sign(payload string) []byte {
	mac := hmac.New(sha256.New, cfg.Secret)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// generateToken produces a "base64(username:expiry).base64(hmac)" token.
// Stateless: validity is checked purely from the signature and the
// embedded expiry, no server-side session store.
func (cfg AuthConfig) generateToken(username string) string {
	payload := fmt.Sprintf("%s:%d", username, time.Now().Add(cfg.TTL).Unix())
	sig := cfg.sign(payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (cfg AuthConfig) validateToken(token string) (string, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", errors.New("malformed token")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("malformed token")
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("malformed token")
	}
	if !hmac.Equal(sigBytes, cfg.sign(string(payloadBytes))) {
		return "", errors.New("invalid token signature")
	}

	payload := string(payloadBytes)
	idx := strings.LastIndex(payload, ":")
	if idx < 0 {
		return "", errors.New("malformed token payload")
	}
	username := payload[:idx]
	expiry, err := strconv.ParseInt(payload[idx+1:], 10, 64)
	if err != nil {
		return "", errors.New("malformed token expiry")
	}
	if time.Now().Unix() > expiry {
		return "", errors.New("token expired")
	}
	return username, nil
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
func handleLogin(cfg AuthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(cfg.Username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(req.Password), []byte(cfg.Password)) == 1
		if !userOK || !passOK {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

		writeJSON(w, http.StatusOK, loginResponse{
			Token:     cfg.generateToken(req.Username),
			ExpiresIn: int64(cfg.TTL.Seconds()),
		})
	}
}

// requireAuth wraps a handler, rejecting requests without a valid
// "Authorization: Bearer <token>" header.
func requireAuth(cfg AuthConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, prefix) {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		if _, err := cfg.validateToken(strings.TrimPrefix(authHeader, prefix)); err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
