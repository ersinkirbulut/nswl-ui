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
