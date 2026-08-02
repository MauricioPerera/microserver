package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// healthCheckTimeout bounds how long a single health check dependency call
// (DB ping, Ollama request) may take, so a hung Ollama or a stuck SQLite
// connection can't turn /health itself into a hang — the whole point of a
// health endpoint is to answer fast even when something behind it isn't.
const healthCheckTimeout = 3 * time.Second

// ollamaHealthURL derives Ollama's lightweight /api/tags endpoint (lists
// installed models, no embedding computation) from ollamaEmbedURL's
// scheme+host, so the health check works regardless of the exact path
// OLLAMA_URL was configured with.
func ollamaHealthURL() string {
	u, err := url.Parse(ollamaEmbedURL)
	if err != nil {
		return ollamaEmbedURL
	}
	u.Path = "/api/tags"
	u.RawQuery = ""
	return u.String()
}

type healthCheckResult struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// checkHealth pings the database and Ollama, each bounded by
// healthCheckTimeout. Status is "ok" only if both succeed; "degraded"
// otherwise, with the specific failure recorded per check so a caller
// doesn't have to guess which dependency is the problem.
func checkHealth(store *VecStore) healthCheckResult {
	checks := map[string]string{
		"database": checkDatabase(store),
		"ollama":   checkOllama(),
	}

	status := "ok"
	for _, v := range checks {
		if v != "ok" {
			status = "degraded"
			break
		}
	}
	return healthCheckResult{Status: status, Checks: checks}
}

func checkDatabase(store *VecStore) string {
	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()
	if err := store.read.PingContext(ctx); err != nil {
		return err.Error()
	}
	return "ok"
}

func checkOllama() string {
	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ollamaHealthURL(), nil)
	if err != nil {
		return err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("unexpected status %d", resp.StatusCode)
	}
	return "ok"
}

// handleHealth: GET /health. Public. 200 when every dependency check
// passes, 503 otherwise — a load balancer or orchestrator can use this to
// pull the instance out of rotation instead of routing traffic somewhere
// that can't actually serve it.
func handleHealth(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result := checkHealth(store)
		status := http.StatusOK
		if result.Status != "ok" {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, result)
	}
}
