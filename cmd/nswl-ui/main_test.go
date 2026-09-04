package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverRotatedLogFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Ex260904.log", "Ex260904.log.0", "Ex260904.log.1", "notes.txt", "ignore.json", "ignore.log.old"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := discoverLogFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 4 {
		t.Fatalf("got %d files: %v", len(paths), paths)
	}
}
