package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsCollectorRecordsAndRenders(t *testing.T) {
	m := newMetricsCollector()
	m.record("GET", "/health", 200, 0)
	m.record("GET", "/health", 200, 0)
	m.record("POST", "/items", 500, 0)

	out := m.render()

	if !strings.Contains(out, `http_requests_total{method="GET",path="/health",status="200"} 2`) {
		t.Fatalf("expected count=2 for GET /health 200, got:\n%s", out)
	}
	if !strings.Contains(out, `http_requests_total{method="POST",path="/items",status="500"} 1`) {
		t.Fatalf("expected count=1 for POST /items 500, got:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE http_requests_total counter") {
		t.Fatalf("expected Prometheus TYPE header, got:\n%s", out)
	}
}

func TestMetricsMiddlewareUsesPatternNotRawPath(t *testing.T) {
	m := newMetricsCollector()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := metricsMiddleware(m, mux)
	server := httptest.NewServer(handler)
	defer server.Close()

	// two different ids should collapse into the same pattern, not create
	// two separate metric series
	for _, id := range []string{"1", "2", "3"} {
		resp, err := http.Get(server.URL + "/items/" + id)
		if err != nil {
			t.Fatalf("GET /items/%s: %v", id, err)
		}
		resp.Body.Close()
	}

	out := m.render()
	if !strings.Contains(out, `http_requests_total{method="GET",path="/items/{id}",status="200"} 3`) {
		t.Fatalf("expected 3 requests collapsed under the pattern /items/{id}, got:\n%s", out)
	}
	if strings.Contains(out, "/items/1") || strings.Contains(out, "/items/2") {
		t.Fatalf("expected raw paths not to appear as separate series, got:\n%s", out)
	}
}

func TestMetricsMiddlewareRecordsErrorStatus(t *testing.T) {
	m := newMetricsCollector()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	handler := metricsMiddleware(m, mux)
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/boom")
	if err != nil {
		t.Fatalf("GET /boom: %v", err)
	}
	resp.Body.Close()

	out := m.render()
	if !strings.Contains(out, `http_requests_total{method="GET",path="/boom",status="500"} 1`) {
		t.Fatalf("expected 500 status recorded, got:\n%s", out)
	}
}

func TestHandleMetricsServesRenderedOutput(t *testing.T) {
	m := newMetricsCollector()
	m.record("GET", "/health", 200, 0)

	server := httptest.NewServer(handleMetrics(m))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var buf [4096]byte
	n, _ := resp.Body.Read(buf[:])
	body := string(buf[:n])
	if !strings.Contains(body, "http_requests_total") {
		t.Fatalf("expected metrics output, got: %s", body)
	}
}

func TestStatusRecorderDefaultsTo200(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	if rec.status != http.StatusOK {
		t.Fatalf("expected default status 200, got %d", rec.status)
	}
	rec.WriteHeader(http.StatusCreated)
	if rec.status != http.StatusCreated {
		t.Fatalf("expected status to update to 201, got %d", rec.status)
	}
}
