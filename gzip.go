package main

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// gzipResponseWriter wraps http.ResponseWriter so a handler's writes
// transparently produce a gzip-compressed body. It tracks its own
// headerWritten/skip/wrote state rather than relying on the handler always
// calling WriteHeader explicitly (handleMetrics writes straight to Write,
// relying on the implicit 200 Go's server normally supplies) or on
// gzip.Writer's own state (constructing it doesn't emit any bytes, but
// Close() unconditionally writes a gzip header+trailer even with nothing
// ever written to it — calling Close() on a 204/304 response, which must
// carry no body, would smuggle ~20 bytes of empty-stream framing into it).
type gzipResponseWriter struct {
	http.ResponseWriter
	gz            *gzip.Writer
	headerWritten bool
	skip          bool // true for 204/304: no body allowed, leave it alone
	wrote         bool // true once real body bytes went through gz — only then is gz.Close() safe/necessary
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.headerWritten {
		return
	}
	w.headerWritten = true
	if status == http.StatusNoContent || status == http.StatusNotModified {
		w.skip = true
	} else {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.headerWritten {
		w.WriteHeader(http.StatusOK) // mirrors http.ResponseWriter's own implicit-200 behavior
	}
	if w.skip {
		return w.ResponseWriter.Write(b)
	}
	w.wrote = true
	return w.gz.Write(b)
}

// gzipMiddleware compresses response bodies for clients that advertise
// gzip support via Accept-Encoding — a plain substring check, not a full
// parse of quality values, which is enough for the two encodings any real
// client sends here. A no-op passthrough for everyone else.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		gzw := &gzipResponseWriter{ResponseWriter: w, gz: gzip.NewWriter(w)}
		next.ServeHTTP(gzw, r)
		if gzw.wrote {
			gzw.gz.Close()
		}
	})
}
