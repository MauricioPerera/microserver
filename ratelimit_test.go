package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTokenBucketAllowsBurstThenThrottles(t *testing.T) {
	rl := newRateLimiter(1, 3) // 1 token/sec, burst 3

	for i := 0; i < 3; i++ {
		if !rl.allow("client-a") {
			t.Fatalf("request %d within burst should be allowed", i)
		}
	}
	if rl.allow("client-a") {
		t.Fatal("4th immediate request should be throttled, burst exhausted")
	}

	// a different key has its own independent bucket
	if !rl.allow("client-b") {
		t.Fatal("a different key should not be affected by client-a's bucket")
	}
}

func TestTokenBucketRefillsOverTime(t *testing.T) {
	rl := newRateLimiter(1000, 1) // fast refill so the test doesn't need to sleep long

	if !rl.allow("client") {
		t.Fatal("first request should be allowed")
	}
	if rl.allow("client") {
		t.Fatal("second immediate request should be throttled")
	}
	time.Sleep(5 * time.Millisecond) // at 1000 tokens/sec, ~5 tokens refill
	if !rl.allow("client") {
		t.Fatal("request after refill delay should be allowed")
	}
}

func TestPruneStaleRemovesOldBuckets(t *testing.T) {
	rl := newRateLimiter(1, 1)
	rl.allow("stale-client")
	rl.buckets["stale-client"].lastCheck = time.Now().Add(-time.Hour)

	rl.pruneStale(time.Minute)

	rl.mu.Lock()
	_, exists := rl.buckets["stale-client"]
	rl.mu.Unlock()
	if exists {
		t.Fatal("expected stale bucket to be pruned")
	}
}

func TestRateLimitMiddlewareReturns429(t *testing.T) {
	rl := newRateLimiter(0, 1) // rate 0: burst of 1, never refills
	handler := rateLimitMiddleware(rl, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	server := httptest.NewServer(handler)
	defer server.Close()

	resp1, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected first request to succeed, got %d", resp1.StatusCode)
	}

	resp2, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp2.StatusCode)
	}
	if resp2.Header.Get("Retry-After") == "" {
		t.Fatal("expected a Retry-After header on 429")
	}
}

func TestLimitBodySizeRejectsOversizedBody(t *testing.T) {
	handler := limitBodySize(10, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(r.Body); err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "body too large")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Post(server.URL, "application/octet-stream", bytes.NewReader(make([]byte, 100)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
}

func TestLimitBodySizeAllowsSmallBody(t *testing.T) {
	handler := limitBodySize(1024, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		buf.ReadFrom(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Post(server.URL, "application/json", bytes.NewReader([]byte(`{"ok":true}`)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDecodeJSONOversizedBodyReturns413(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	handler := limitBodySize(20, newRouter(store, testAuth()))
	server := httptest.NewServer(handler)
	defer server.Close()

	token := testAuth().generateToken("admin", RoleAdmin)
	body := []byte(`{"text":"this JSON body is deliberately longer than twenty bytes"}`)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/items", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /items: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
}

func TestLoginRateLimitBlocksRepeatedAttempts(t *testing.T) {
	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	body, _ := marshalLoginRequest("admin", "wrong")
	var lastStatus int
	for i := 0; i < loginRateLimitBurst+1; i++ {
		resp, err := http.Post(server.URL+"/login", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /login attempt %d: %v", i, err)
		}
		lastStatus = resp.StatusCode
		resp.Body.Close()
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Fatalf("expected the attempt past the burst to be rate limited (429), got %d", lastStatus)
	}
}

func marshalLoginRequest(username, password string) ([]byte, error) {
	return []byte(`{"username":"` + username + `","password":"` + password + `"}`), nil
}
