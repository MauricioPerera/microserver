package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupFilePathRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()

	valid := "backup-20260101-120000.123456789.db"
	path, err := backupFilePath(dir, valid)
	if err != nil {
		t.Fatalf("expected a well-formed name to be accepted: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("expected the resolved path to stay inside %s, got %s", dir, path)
	}

	for _, bad := range []string{
		"../vec.db",
		"../../etc/passwd",
		"backup-20260101-120000.123456789.db/../../vec.db",
		"notabackup.db",
		"backup-1.db",
		"",
	} {
		if _, err := backupFilePath(dir, bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestCheckpointTruncatesWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.db")
	s, err := openVecDB(path)
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	for i := int64(0); i < 5; i++ {
		if err := insertText(s, i, "contenido de prueba para generar actividad en el wal"); err != nil {
			t.Fatalf("insertText: %v", err)
		}
	}

	walPath := path + "-wal"
	before, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("expected -wal file to exist before checkpoint: %v", err)
	}
	if before.Size() == 0 {
		t.Fatal("expected -wal file to have data before checkpoint")
	}

	if err := s.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	after, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat -wal after checkpoint: %v", err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("expected -wal file to shrink after checkpoint: before=%d after=%d", before.Size(), after.Size())
	}
	t.Logf("wal size before=%d after=%d", before.Size(), after.Size())
}

func TestBackupRotateKeepsOnlyN(t *testing.T) {
	srcPath := filepath.Join(t.TempDir(), "source.db")
	s, err := openVecDB(srcPath)
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	if err := insertText(s, 1, "el gato duerme en el sofá"); err != nil {
		t.Fatalf("insertText: %v", err)
	}

	backupDir := filepath.Join(t.TempDir(), "backups")
	const keep = 3
	const total = 6

	var lastPath string
	for i := 0; i < total; i++ {
		p, err := BackupRotate(s, backupDir, keep)
		if err != nil {
			t.Fatalf("BackupRotate iteration %d: %v", i, err)
		}
		lastPath = p
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("reading backup dir: %v", err)
	}
	var backups []string
	for _, e := range entries {
		if !e.IsDir() {
			backups = append(backups, e.Name())
		}
	}
	if len(backups) != keep {
		t.Fatalf("expected %d backups after rotation, got %d: %v", keep, len(backups), backups)
	}
	if _, err := os.Stat(lastPath); err != nil {
		t.Fatalf("expected most recent backup %s to survive rotation: %v", lastPath, err)
	}
	t.Logf("backups retained: %v", backups)
}
