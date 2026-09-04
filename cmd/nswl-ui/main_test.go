package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nswl-ui/internal/nswl"
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

func TestBuildTimeSeries(t *testing.T) {
	now := time.Date(2026, 9, 4, 15, 30, 15, 0, time.Local)
	rows := []nswl.Entry{
		{Timestamp: now.Add(-30 * time.Second)},
		{Timestamp: now.Add(-45 * time.Second)},
		{Timestamp: now.Add(-2 * time.Minute)},
		{Timestamp: now.Add(-2 * time.Hour)},
	}
	series := buildTimeSeries(rows, now, 15*time.Minute, time.Minute)
	if len(series.Points) != 15 || series.Total != 3 || series.Peak != 2 || series.BucketSeconds != 60 {
		t.Fatalf("unexpected series: %#v", series)
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
