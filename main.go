package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	checkpointInterval = 5 * time.Minute
	backupInterval     = 1 * time.Hour
	defaultHTTPAddr    = "127.0.0.1:8080"

	// generalRateLimit applies to every request (except /login, which has
	// its own stricter limiter — see newRouter). Generous for legitimate
	// use: a single client naturally can't exceed a few req/s anyway since
	// every embed call takes ~200ms, so this exists to bound abuse/bugs,
	// not to throttle normal traffic.
	generalRateLimitPerSecond = 20
	generalRateLimitBurst     = 40
	rateLimiterPruneInterval  = 10 * time.Minute
)

// backupDir and backupKeep are vars, not consts, so tests exercising the
// admin backup HTTP handlers (handleTriggerBackup, handleListBackups,
// handleDownloadBackup — see http_admin.go) can redirect them to a temp
// directory instead of writing real backup files into the working
// directory that `go test` runs in.
var (
	backupDir  = "backups"
	backupKeep = 7
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	auth, err := loadAuthConfig()
	if err != nil {
		slog.Error("loading auth config", "error", err)
		os.Exit(1)
	}

	// Bound to loopback by default: this process is meant to sit behind a
	// TLS-terminating reverse proxy (see README), not face the internet
	// directly. Override via HTTP_ADDR only if the proxy reaches it over a
	// network (e.g. a separate container) rather than localhost.
	httpAddr := defaultHTTPAddr
	if v := os.Getenv("HTTP_ADDR"); v != "" {
		httpAddr = v
	}

	db, err := openVecDB("vec.db")
	if err != nil {
		slog.Error("opening database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Only takes effect the very first time the server runs against an
	// empty users table — seeds the initial admin from AUTH_USERNAME/
	// AUTH_PASSWORD. Every later startup is a no-op here regardless of
	// what those env vars contain; manage users via /users after that.
	if err := ensureBootstrapAdmin(db, auth.BootstrapUsername, auth.BootstrapPassword); err != nil {
		slog.Error("bootstrapping admin user", "error", err)
		os.Exit(1)
	}

	stop := make(chan struct{})
	db.StartCheckpointLoop(checkpointInterval, stop)
	db.StartBackupLoop(backupDir, backupInterval, backupKeep, stop)
	slog.Info("maintenance running",
		"checkpoint_interval", checkpointInterval.String(),
		"backup_interval", backupInterval.String(),
		"backup_dir", backupDir,
		"backup_keep", backupKeep,
	)

	generalLimiter := newRateLimiter(generalRateLimitPerSecond, generalRateLimitBurst)
	generalLimiter.startPruneLoop(rateLimiterPruneInterval, stop)

	cors := loadCORSConfig()
	if cors.Enabled {
		slog.Info("cors enabled", "allow_all", cors.AllowAll, "origins", cors.AllowedOrigins)
	}

	metrics := newMetricsCollector()

	// /metrics lives outside the rate limit / body size chain — a scrape
	// interval shouldn't risk tripping the general limiter, and a GET has
	// no body to cap anyway. metricsMiddleware wraps everything, including
	// /metrics itself and /health, so a scrape of /metrics also shows up
	// in its own counters (ordinary self-monitoring, not a bug).
	outerMux := http.NewServeMux()
	outerMux.Handle("GET /metrics", handleMetrics(metrics))
	outerMux.Handle("/", limitBodySize(maxRequestBodyBytes, rateLimitMiddleware(generalLimiter, newRouter(db, auth))))
	handler := corsMiddleware(cors, metricsMiddleware(metrics, gzipMiddleware(outerMux)))

	srv := &http.Server{Addr: httpAddr, Handler: handler}
	go func() {
		slog.Info("listening", "addr", httpAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "error", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	slog.Info("shutting down", "reason", "signal received")
	close(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("http shutdown", "error", err)
	}

	if err := db.Checkpoint(); err != nil {
		slog.Error("final checkpoint failed", "error", err)
	}
	if _, err := BackupRotate(db, backupDir, backupKeep); err != nil {
		slog.Error("final backup failed", "error", err)
	}
}
