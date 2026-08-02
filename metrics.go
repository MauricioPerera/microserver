package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// metricsCollector accumulates per-route request counts, error counts, and
// duration sums in memory, exposed in Prometheus text exposition format at
// GET /metrics. No external dependency (no client_golang) — the format is
// simple enough to render by hand, and this is the only metric consumer
// this project needs today.
type metricsCollector struct {
	mu sync.Mutex
	// counts keyed by "method\x00pattern\x00status"
	counts map[string]int64
	// duration sum/count keyed by "method\x00pattern"
	durationSum   map[string]float64
	durationCount map[string]int64
}

func newMetricsCollector() *metricsCollector {
	return &metricsCollector{
		counts:        make(map[string]int64),
		durationSum:   make(map[string]float64),
		durationCount: make(map[string]int64),
	}
}

func metricsCountKey(method, pattern string, status int) string {
	return fmt.Sprintf("%s\x00%s\x00%d", method, pattern, status)
}

func metricsDurationKey(method, pattern string) string {
	return method + "\x00" + pattern
}

func (m *metricsCollector) record(method, pattern string, status int, dur time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[metricsCountKey(method, pattern, status)]++
	dk := metricsDurationKey(method, pattern)
	m.durationSum[dk] += dur.Seconds()
	m.durationCount[dk]++
}

// render writes the current metrics in Prometheus text exposition format
// (https://prometheus.io/docs/instrumenting/exposition_formats/). Keys are
// sorted for stable, diffable output.
func (m *metricsCollector) render() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder

	b.WriteString("# HELP http_requests_total Total HTTP requests by method, route pattern, and status code.\n")
	b.WriteString("# TYPE http_requests_total counter\n")
	countKeys := make([]string, 0, len(m.counts))
	for k := range m.counts {
		countKeys = append(countKeys, k)
	}
	sort.Strings(countKeys)
	for _, k := range countKeys {
		parts := strings.SplitN(k, "\x00", 3)
		method, pattern, status := parts[0], parts[1], parts[2]
		fmt.Fprintf(&b, "http_requests_total{method=%q,path=%q,status=%q} %d\n", method, pattern, status, m.counts[k])
	}

	b.WriteString("# HELP http_request_duration_seconds_sum Sum of request durations in seconds by method and route pattern.\n")
	b.WriteString("# TYPE http_request_duration_seconds_sum counter\n")
	durKeys := make([]string, 0, len(m.durationSum))
	for k := range m.durationSum {
		durKeys = append(durKeys, k)
	}
	sort.Strings(durKeys)
	for _, k := range durKeys {
		parts := strings.SplitN(k, "\x00", 2)
		method, pattern := parts[0], parts[1]
		fmt.Fprintf(&b, "http_request_duration_seconds_sum{method=%q,path=%q} %f\n", method, pattern, m.durationSum[k])
	}

	b.WriteString("# HELP http_request_duration_seconds_count Count of requests contributing to the duration sum, by method and route pattern.\n")
	b.WriteString("# TYPE http_request_duration_seconds_count counter\n")
	for _, k := range durKeys {
		parts := strings.SplitN(k, "\x00", 2)
		method, pattern := parts[0], parts[1]
		fmt.Fprintf(&b, "http_request_duration_seconds_count{method=%q,path=%q} %d\n", method, pattern, m.durationCount[k])
	}

	return b.String()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// metricsMiddleware wraps the whole handler chain, recording request count/
// duration/status after each request completes and emitting a structured
// access log line. Labeled by r.Pattern — the ServeMux pattern that
// matched (e.g. "GET /collections/{name}/items/{id}"), populated by the
// mux itself during its dispatch, which happens somewhere inside the call
// to next.ServeHTTP below — not r.URL.Path, which would blow up metric
// cardinality with one series per distinct collection name / item id ever
// requested.
//
// Applied once, outside newRouter (same pattern as the rate limiter and
// body size limiter) so it doesn't require touching newRouter's signature
// or its many test call sites.
func metricsMiddleware(m *metricsCollector, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		dur := time.Since(start)

		// r.Pattern is the full "METHOD /path" registration string (e.g.
		// "GET /items/{id}"), not just the path — strip the method prefix
		// since it's already recorded separately as its own label/attr.
		pattern := r.Pattern
		if pattern == "" {
			pattern = "(unmatched)"
		} else if idx := strings.IndexByte(pattern, ' '); idx != -1 {
			pattern = pattern[idx+1:]
		}
		m.record(r.Method, pattern, rec.status, dur)

		attrs := []any{"method", r.Method, "path", pattern, "status", rec.status, "duration_ms", dur.Milliseconds()}
		if rec.status >= 400 {
			slog.Info("request", attrs...)
		} else {
			slog.Debug("request", attrs...)
		}
	})
}

// handleMetrics: GET /metrics, Prometheus text exposition format. Public,
// same as /health — Prometheus scraping typically doesn't want to manage a
// rotating bearer token. Route counts/latencies aren't secrets, but if
// that's a concern in your deployment, restrict access at the reverse
// proxy or network layer rather than expecting auth here.
func handleMetrics(m *metricsCollector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Write([]byte(m.render()))
	}
}
