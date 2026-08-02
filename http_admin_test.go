package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// withTempBackupDir redirects the package-level backupDir/backupKeep vars
// to a temp directory for the duration of a test, restoring the originals
// afterward — so these tests never touch the real "backups/" directory
// under the working directory go test runs in.
func withTempBackupDir(t *testing.T, keep int) {
	t.Helper()
	origDir, origKeep := backupDir, backupKeep
	backupDir = filepath.Join(t.TempDir(), "backups")
	backupKeep = keep
	t.Cleanup(func() {
		backupDir, backupKeep = origDir, origKeep
	})
}

func TestHTTPTriggerAndListBackups(t *testing.T) {
	withTempBackupDir(t, 7)

	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	resp := doAuthed(t, http.MethodPost, server.URL+"/admin/backup", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /admin/backup status=%d", resp.StatusCode)
	}
	var created BackupInfo
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decoding backup response: %v", err)
	}
	resp.Body.Close()
	if created.Name == "" || created.SizeBytes == 0 {
		t.Fatalf("expected a non-empty backup, got %+v", created)
	}

	resp = doAuthed(t, http.MethodGet, server.URL+"/admin/backups", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/backups status=%d", resp.StatusCode)
	}
	var backups []BackupInfo
	if err := json.NewDecoder(resp.Body).Decode(&backups); err != nil {
		t.Fatalf("decoding backups list: %v", err)
	}
	resp.Body.Close()
	if len(backups) != 1 || backups[0].Name != created.Name {
		t.Fatalf("expected the triggered backup to be listed, got %+v", backups)
	}
}

func TestHTTPDownloadBackup(t *testing.T) {
	withTempBackupDir(t, 7)

	store, err := openVecDB(":memory:")
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(newRouter(store, testAuth()))
	defer server.Close()

	resp := doAuthed(t, http.MethodPost, server.URL+"/admin/backup", nil)
	var created BackupInfo
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	resp = doAuthed(t, http.MethodGet, server.URL+"/admin/backups/"+created.Name, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/backups/{name} status=%d", resp.StatusCode)
	}
	body, err := os.ReadFile(filepath.Join(backupDir, created.Name))
	if err != nil {
		t.Fatalf("reading backup file directly: %v", err)
	}
	downloaded := make([]byte, 0, len(body))
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		downloaded = append(downloaded, buf[:n]...)
		if err != nil {
			break
		}
	}
	resp.Body.Close()
	if int64(len(downloaded)) != int64(len(body)) {
		t.Fatalf("expected downloaded backup to match the file on disk: got %d bytes, want %d", len(downloaded), len(body))
	}

	// unknown name -> 404
	resp = doAuthed(t, http.MethodGet, server.URL+"/admin/backups/backup-99999999-999999.999999999.db", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for a nonexistent backup, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// malformed name -> 400, never touches the filesystem — backupFilePath
	// rejects anything not matching the exact backup-<timestamp>.db shape,
	// which is also the entire path-traversal defense.
	for _, bad := range []string{"notabackup.db", "backup-1.db", "vec.db"} {
		resp = doAuthed(t, http.MethodGet, server.URL+"/admin/backups/"+bad, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for invalid backup name %q, got %d", bad, resp.StatusCode)
		}
		resp.Body.Close()
	}
}
