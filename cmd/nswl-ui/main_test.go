package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestNewestRotatedFileIsPrioritized(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "Ex260904.log")
	livePath := filepath.Join(dir, "Ex260904.log.18")
	for _, path := range []string{oldPath, livePath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Date(2026, 9, 4, 15, 18, 0, 0, time.Local)
	liveTime := oldTime.Add(4*time.Hour + 22*time.Minute)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(livePath, liveTime, liveTime); err != nil {
		t.Fatal(err)
	}
	got := prioritizeLogFiles([]string{oldPath, livePath})
	if len(got) != 2 || got[0].path != livePath || !got[0].live || got[1].live {
		t.Fatalf("unexpected priority: %#v", got)
	}
}

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"bind":"0.0.0.0","port":9090,"log_path":"/var/log/nswl"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bind != "0.0.0.0" || cfg.Port != 9090 || cfg.LogPath != "/var/log/nswl" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}
