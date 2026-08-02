package main

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
)

// handleTriggerBackup: POST /admin/backup. Admin-only. Runs an immediate
// backup+rotation — same VACUUM INTO + prune BackupRotate does on its
// periodic schedule, useful right before a risky operation instead of
// waiting for the next tick.
func handleTriggerBackup(store *VecStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path, err := BackupRotate(store, backupDir, backupKeep)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		info, err := os.Stat(path)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, BackupInfo{
			Name:      filepath.Base(path),
			SizeBytes: info.Size(),
			CreatedAt: info.ModTime(),
		})
	}
}

// handleListBackups: GET /admin/backups. Admin-only — a backup is the full
// database, same sensitivity as the data itself.
func handleListBackups(w http.ResponseWriter, r *http.Request) {
	backups, err := listBackupFiles(backupDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, backups)
}

// handleDownloadBackup: GET /admin/backups/{name}. Admin-only. There's no
// restore endpoint on purpose — swapping the live database file out from
// under an open connection pool isn't safe without a much bigger change
// (an indirection layer plus draining in-flight requests). Restoring is a
// documented offline procedure: stop the process, copy the downloaded file
// over vec.db, start it again (see README).
func handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	path, err := backupFilePath(backupDir, r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "backup not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
	if _, err := io.Copy(w, f); err != nil {
		slog.Error("streaming backup download", "error", err)
	}
}
