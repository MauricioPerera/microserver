package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Checkpoint forces SQLite to fold the WAL file back into the main database
// file and truncate it. SQLite auto-checkpoints passively every ~1000 pages
// on its own, but a passive checkpoint can be blocked indefinitely by a
// long-running reader and silently make no progress. TRUNCATE mode here
// actively waits for the lock it needs, so call it from a background loop,
// never from a request path.
func (s *VecStore) Checkpoint() error {
	if _, err := s.write.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	return nil
}

// StartCheckpointLoop runs Checkpoint on a fixed interval until stop is
// closed. Non-blocking; runs in its own goroutine.
func (s *VecStore) StartCheckpointLoop(interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.Checkpoint(); err != nil {
					slog.Error("wal checkpoint failed", "error", err)
				}
			case <-stop:
				return
			}
		}
	}()
}

const backupPrefix = "backup-"
const backupSuffix = ".db"

// BackupRotate writes a timestamped snapshot into dir via VACUUM INTO, then
// deletes the oldest backups beyond keep. Returns the path of the new
// backup even if pruning afterward fails.
func BackupRotate(s *VecStore, dir string, keep int) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating backup dir: %w", err)
	}
	name := backupPrefix + time.Now().UTC().Format("20060102-150405.000000000") + backupSuffix
	path := filepath.Join(dir, name)
	if err := backupTo(s, path); err != nil {
		return "", err
	}
	if err := pruneBackups(dir, keep); err != nil {
		return path, fmt.Errorf("backup succeeded but pruning old backups failed: %w", err)
	}
	return path, nil
}

// pruneBackups keeps the newest `keep` files matching the backup naming
// scheme in dir and deletes the rest. Filenames use a zero-padded timestamp
// so lexicographic sort order matches chronological order.
func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading backup dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasPrefix(n, backupPrefix) && strings.HasSuffix(n, backupSuffix) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) <= keep {
		return nil
	}
	for _, n := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return fmt.Errorf("removing old backup %s: %w", n, err)
		}
	}
	return nil
}

// StartBackupLoop runs BackupRotate on a fixed interval until stop is
// closed. Non-blocking; runs in its own goroutine.
func (s *VecStore) StartBackupLoop(dir string, interval time.Duration, keep int, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := BackupRotate(s, dir, keep); err != nil {
					slog.Error("backup failed", "error", err)
				}
			case <-stop:
				return
			}
		}
	}()
}
