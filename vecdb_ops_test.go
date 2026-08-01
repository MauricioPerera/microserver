package main

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	s, err := openVecDB(path)
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	const n = 20
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			if err := insertText(s, id, fmt.Sprintf("documento numero %d", id)); err != nil {
				errCh <- err
			}
		}(int64(i))
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent insert failed: %v", err)
	}

	var count int
	if err := s.read.QueryRow(`SELECT count(*) FROM vec_items`).Scan(&count); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if count != n {
		t.Fatalf("expected %d rows after concurrent writes, got %d", n, count)
	}
}

func TestBackup(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")

	s, err := openVecDB(srcPath)
	if err != nil {
		t.Fatalf("openVecDB: %v", err)
	}
	defer s.Close()

	docs := map[int64]string{
		1: "el gato duerme en el sofá",
		2: "un felino descansa sobre el mueble",
		3: "la bolsa de valores subió hoy",
	}
	for id, text := range docs {
		if err := insertText(s, id, text); err != nil {
			t.Fatalf("insertText id=%d: %v", id, err)
		}
	}

	backupPath := filepath.Join(dir, "backup.db")
	if err := backupTo(s, backupPath); err != nil {
		t.Fatalf("backupTo: %v", err)
	}

	restored, err := openVecDB(backupPath)
	if err != nil {
		t.Fatalf("opening backup: %v", err)
	}
	defer restored.Close()

	rows, err := queryText(restored, "un gato tomando una siesta", 1, true)
	if err != nil {
		t.Fatalf("queryText on restored backup: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("expected at least one result from restored backup")
	}
	var id int64
	var text string
	var distance float64
	if err := rows.Scan(&id, &text, &distance); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if id != 1 && id != 2 {
		t.Fatalf("expected cat sentence (id=1 or 2) from backup, got id=%d", id)
	}
	if text != docs[id] {
		t.Fatalf("expected restored text to match, got %q want %q", text, docs[id])
	}
	t.Logf("backup restored correctly: nearest neighbor id=%d text=%q distance=%f", id, text, distance)
}
