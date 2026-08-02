package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGzipMiddlewareCompressesWhenAccepted(t *testing.T) {
	const body = `{"hello":"world"}`
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	})
	h := gzipMiddleware(next)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("expected Content-Encoding: gzip, got %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("expected Vary: Accept-Encoding, got %q", got)
	}

	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("response body is not valid gzip: %v", err)
	}
	defer gz.Close()
	decompressed, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("reading decompressed body: %v", err)
	}
	if string(decompressed) != body {
		t.Fatalf("expected decompressed body %q, got %q", body, decompressed)
	}
}

func TestGzipMiddlewareSkipsWithoutAcceptEncoding(t *testing.T) {
	const body = `{"hello":"world"}`
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	})
	h := gzipMiddleware(next)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("expected no Content-Encoding header, got %q", got)
	}
	if rec.Body.String() != body {
		t.Fatalf("expected uncompressed body %q, got %q", body, rec.Body.String())
	}
}

// TestGzipMiddlewareHandlesImplicitWriteHeader covers handleMetrics' style
// of writing straight to Write() without ever calling WriteHeader — Go's
// server implicitly sends 200 in that case, and the gzip wrapper needs to
// replicate that so Content-Encoding still gets set (not just gzip-compress
// the bytes without labeling them).
func TestGzipMiddlewareHandlesImplicitWriteHeader(t *testing.T) {
	const body = "metric_line 1\n"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	h := gzipMiddleware(next)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("expected Content-Encoding: gzip, got %q", got)
	}
	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("response body is not valid gzip: %v", err)
	}
	defer gz.Close()
	decompressed, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("reading decompressed body: %v", err)
	}
	if string(decompressed) != body {
		t.Fatalf("expected decompressed body %q, got %q", body, decompressed)
	}
}

func TestGzipMiddlewareDoesNotMarkNoContentResponses(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := gzipMiddleware(next)

	req := httptest.NewRequest(http.MethodDelete, "/items/1", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("expected no Content-Encoding header on a 204 response, got %q", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected an empty body on 204, got %d bytes", rec.Body.Len())
	}
}
