package main

import (
	"net/http"
	"os"
	"strings"
)

// CORSConfig controls which browser origins get CORS headers. Disabled
// (zero value) unless CORS_ALLOWED_ORIGINS is set — no headers, no
// preflight short-circuit, identical to how the server behaved before CORS
// existed. This is the safe default: without it, only same-origin or
// non-browser clients (curl, server-to-server) can call the API anyway,
// since without CORS headers a browser blocks cross-origin responses.
type CORSConfig struct {
	Enabled        bool
	AllowAll       bool // CORS_ALLOWED_ORIGINS == "*"
	AllowedOrigins []string
}

// loadCORSConfig reads CORS_ALLOWED_ORIGINS: unset/empty disables CORS
// entirely, "*" allows any origin, otherwise a comma-separated list of
// exact origins (e.g. "https://app.example.com,https://admin.example.com")
// to echo back.
func loadCORSConfig() CORSConfig {
	v := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if v == "" {
		return CORSConfig{}
	}
	if v == "*" {
		return CORSConfig{Enabled: true, AllowAll: true}
	}
	parts := strings.Split(v, ",")
	origins := make([]string, len(parts))
	for i, p := range parts {
		origins[i] = strings.TrimSpace(p)
	}
	return CORSConfig{Enabled: true, AllowedOrigins: origins}
}

func (c CORSConfig) allows(origin string) bool {
	if !c.Enabled || origin == "" {
		return false
	}
	if c.AllowAll {
		return true
	}
	for _, o := range c.AllowedOrigins {
		if o == origin {
			return true
		}
	}
	return false
}

// corsMiddleware adds CORS headers when the request's Origin is allowed,
// and short-circuits preflight OPTIONS requests with 204 — the API has no
// OPTIONS handlers registered, so without this a preflight would otherwise
// hit the mux and get a 404/405, which browsers treat as a failed
// preflight regardless of what the real request would have returned.
// A no-op when cfg is disabled: never touches headers, never intercepts
// OPTIONS, so a deployment that doesn't set CORS_ALLOWED_ORIGINS sees
// exactly the same behavior as before this middleware existed.
func corsMiddleware(cfg CORSConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		if origin := r.Header.Get("Origin"); cfg.allows(origin) {
			if cfg.AllowAll {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
