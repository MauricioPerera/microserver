package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	checkpointInterval = 5 * time.Minute
	backupInterval     = 1 * time.Hour
	backupDir          = "backups"
	backupKeep         = 7
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

func main() {
	auth, err := loadAuthConfig()
	if err != nil {
		log.Fatal(err)
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
		log.Fatal(err)
	}
	defer db.Close()

	stop := make(chan struct{})
	db.StartCheckpointLoop(checkpointInterval, stop)
	db.StartBackupLoop(backupDir, backupInterval, backupKeep, stop)
	log.Printf("maintenance running: checkpoint every %s, backup every %s to %q (keep %d)\n",
		checkpointInterval, backupInterval, backupDir, backupKeep)

	generalLimiter := newRateLimiter(generalRateLimitPerSecond, generalRateLimitBurst)
	generalLimiter.startPruneLoop(rateLimiterPruneInterval, stop)
	handler := limitBodySize(maxRequestBodyBytes, rateLimitMiddleware(generalLimiter, newRouter(db, auth)))

	srv := &http.Server{Addr: httpAddr, Handler: handler}
	go func() {
		log.Printf("listening on %s\n", httpAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down: final checkpoint and backup")
	close(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}

	if err := db.Checkpoint(); err != nil {
		log.Printf("final checkpoint failed: %v", err)
	}
	if _, err := BackupRotate(db, backupDir, backupKeep); err != nil {
		log.Printf("final backup failed: %v", err)
	}
}
