package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// maxRequestBodyBytes caps how large a single request body can be before
// it's rejected. Applied to every request, not just JSON ones — harmless
// for GETs, which have no body to begin with.
const maxRequestBodyBytes = 1 << 20 // 1 MiB

// tokenBucket is a simple per-key rate limiter: refills at rate tokens/sec
// up to burst, one token consumed per allowed request.
type tokenBucket struct {
	tokens    float64
	lastCheck time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64
	burst   float64
}

func newRateLimiter(ratePerSecond float64, burst int) *rateLimiter {
	return &rateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    ratePerSecond,
		burst:   float64(burst),
	}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		rl.buckets[key] = &tokenBucket{tokens: rl.burst - 1, lastCheck: now}
		return true
	}

	elapsed := now.Sub(b.lastCheck).Seconds()
	b.lastCheck = now
	b.tokens += elapsed * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// pruneStale drops buckets untouched for longer than maxAge, so memory
// doesn't grow unboundedly over a long-running process seeing many distinct
// client IPs.
func (rl *rateLimiter) pruneStale(maxAge time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for k, b := range rl.buckets {
		if b.lastCheck.Before(cutoff) {
			delete(rl.buckets, k)
		}
	}
}

// startPruneLoop runs pruneStale on an interval until stop is closed.
func (rl *rateLimiter) startPruneLoop(interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				rl.pruneStale(interval * 10)
			case <-stop:
				return
			}
		}
	}()
}

// clientIP extracts the request's IP, stripping the port from RemoteAddr.
// This is whatever TCP connection reached this process. Behind the Caddy
// reverse proxy documented in the TLS section, that's the proxy's own
// address for every client, not each visitor's real IP — every request
// looks like it comes from one "client" there, making this an effective
// global rate limit rather than a per-visitor one in that deployment.
// X-Forwarded-For isn't trusted here: honoring it without knowing the
// proxy topology would let any direct caller spoof their rate-limit key.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimitMiddleware rejects requests over the limit with 429 and a
// Retry-After hint.
func rateLimitMiddleware(rl *rateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limitBodySize caps request bodies so a client can't exhaust memory with
// an oversized payload.
func limitBodySize(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}

// decodeJSON decodes the request body into v. Writes the response and
// returns false on failure: 413 if the body exceeded limitBodySize's cap
// (detected via http.MaxBytesError, not string-matching), 400 for any
// other decode failure (malformed JSON, wrong shape, etc).
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body too large (max %d bytes)", maxErr.Limit))
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}
