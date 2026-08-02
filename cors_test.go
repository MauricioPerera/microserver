package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestLoadCORSConfig(t *testing.T) {
	t.Run("unset disables", func(t *testing.T) {
		os.Unsetenv("CORS_ALLOWED_ORIGINS")
		cfg := loadCORSConfig()
		if cfg.Enabled {
			t.Fatal("expected CORS to be disabled when env var is unset")
		}
	})

	t.Run("star allows all", func(t *testing.T) {
		os.Setenv("CORS_ALLOWED_ORIGINS", "*")
		defer os.Unsetenv("CORS_ALLOWED_ORIGINS")
		cfg := loadCORSConfig()
		if !cfg.Enabled || !cfg.AllowAll {
			t.Fatalf("expected enabled+allow-all, got %+v", cfg)
		}
		if !cfg.allows("https://anything.example.com") {
			t.Fatal("expected any origin to be allowed")
		}
	})

	t.Run("comma-separated list", func(t *testing.T) {
		os.Setenv("CORS_ALLOWED_ORIGINS", "https://a.example.com, https://b.example.com")
		defer os.Unsetenv("CORS_ALLOWED_ORIGINS")
		cfg := loadCORSConfig()
		if !cfg.allows("https://a.example.com") || !cfg.allows("https://b.example.com") {
			t.Fatalf("expected both listed origins to be allowed, got %+v", cfg)
		}
		if cfg.allows("https://evil.example.com") {
			t.Fatal("expected an unlisted origin to be rejected")
		}
	})
}

func TestCORSMiddlewareDisabledIsNoOp(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := corsMiddleware(CORSConfig{}, next)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected disabled CORS middleware to pass through to next")
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("expected no CORS headers when disabled")
	}
}

func TestCORSMiddlewareAllowedOrigin(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	cfg := CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.com"}}
	h := corsMiddleware(cfg, next)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("expected origin to be echoed back, got %q", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected request to reach next handler, got status %d", rec.Code)
	}
}

func TestCORSMiddlewareRejectsUnlistedOrigin(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	cfg := CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.com"}}
	h := corsMiddleware(cfg, next)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no CORS header for an unlisted origin, got %q", got)
	}
}

func TestCORSMiddlewareShortCircuitsPreflight(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	cfg := CORSConfig{Enabled: true, AllowAll: true}
	h := corsMiddleware(cfg, next)

	req := httptest.NewRequest(http.MethodOptions, "/items", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called {
		t.Fatal("expected preflight OPTIONS to be short-circuited, not passed to next")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for preflight, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected wildcard CORS header on preflight response, got %q", got)
	}
}
