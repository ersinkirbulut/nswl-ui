package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata"

	"nswl-ui/internal/logindex"
)

//go:embed web/*
var assets embed.FS

type app struct {
	logPath  string
	store    *logindex.Store
	indexing atomic.Bool
}

type logCandidate struct {
	path    string
	modTime time.Time
	live    bool
}

type config struct {
	Port                 int    `json:"port"`
	LogPath              string `json:"log_path"`
	Bind                 string `json:"bind"`
	DatabasePath         string `json:"database_path"`
	IndexIntervalSeconds int    `json:"index_interval_seconds"`
	LogTimezone          string `json:"log_timezone"`
}

func main() {
	configPath := flag.String("config", "config.json", "configuration file")
	flag.Parse()
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	location, err := time.LoadLocation(cfg.LogTimezone)
	if err != nil {
		log.Fatalf("load log timezone %q: %v", cfg.LogTimezone, err)
	}
	store, err := logindex.Open(cfg.DatabasePath, location)
	if err != nil {
		log.Fatalf("open index: %v", err)
	}
	defer store.Close()
	a := &app{logPath: cfg.LogPath, store: store}

	mux := http.NewServeMux()
	web, _ := fs.Sub(assets, "web")
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(web))))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := assets.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/overview", a.handleOverview)
	mux.HandleFunc("GET /api/timeseries", a.handleTimeSeries)
	mux.HandleFunc("GET /api/analytics", a.handleAnalytics)
	mux.HandleFunc("GET /api/logs", a.handleLogs)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go a.indexLoop(ctx, time.Duration(cfg.IndexIntervalSeconds)*time.Second)

	addr := fmt.Sprintf("%s:%d", cfg.Bind, cfg.Port)
	server := &http.Server{Addr: addr, Handler: securityHeaders(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("NSWL UI listening on %s (logs: %s, index: %s)", addr, a.logPath, cfg.DatabasePath)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
}

func loadConfig(path string) (config, error) {
	cfg := config{Bind: "127.0.0.1", Port: 8090, LogPath: "./logs", DatabasePath: "/var/lib/nswl-ui/nswl.db", IndexIntervalSeconds: 2, LogTimezone: "Europe/Istanbul"}
	b, err := os.ReadFile(path)
	if err != nil {
		return config{}, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return config{}, fmt.Errorf("port must be between 1 and 65535")
	}
	if strings.TrimSpace(cfg.LogPath) == "" {
		return config{}, fmt.Errorf("log_path cannot be empty")
	}
	if strings.TrimSpace(cfg.DatabasePath) == "" {
		return config{}, fmt.Errorf("database_path cannot be empty")
	}
	if cfg.IndexIntervalSeconds < 1 || cfg.IndexIntervalSeconds > 300 {
		return config{}, fmt.Errorf("index_interval_seconds must be between 1 and 300")
	}
	if strings.TrimSpace(cfg.LogTimezone) == "" {
		return config{}, fmt.Errorf("log_timezone cannot be empty")
	}
	if cfg.Bind == "" {
		cfg.Bind = "127.0.0.1"
	}
	return cfg, nil
}

func (a *app) indexLoop(ctx context.Context, interval time.Duration) {
	a.syncLogs(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.syncLogs(ctx)
		}
	}
}

func (a *app) syncLogs(ctx context.Context) {
	if !a.indexing.CompareAndSwap(false, true) {
		return
	}
	defer a.indexing.Store(false)
	paths, err := discoverLogFiles(a.logPath)
	if err != nil {
		log.Printf("index scan: %v", err)
		return
	}
	candidates := prioritizeLogFiles(paths)
	indexed := 0
	backfillBudget := 1000
	for _, candidate := range candidates {
		limit := 5000
		if !candidate.live {
			if backfillBudget <= 0 {
				break
			}
			limit = backfillBudget
		}
		count, err := a.store.IngestFileLimit(ctx, candidate.path, limit)
		if err != nil {
			log.Printf("index %s: %v", candidate.path, err)
			continue
		}
		indexed += count
		if !candidate.live {
			backfillBudget -= count
		}
	}
	if indexed > 0 {
		log.Printf("indexed %d new requests from %d files", indexed, len(candidates))
	}
}

func prioritizeLogFiles(paths []string) []logCandidate {
	candidates := make([]logCandidate, 0, len(paths))
	var newest time.Time
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		candidate := logCandidate{path: path, modTime: info.ModTime()}
		candidates = append(candidates, candidate)
		if candidate.modTime.After(newest) {
			newest = candidate.modTime
		}
	}
	// Multiple virtual servers may write concurrently. Files modified within
	// one minute of the newest file are all treated as live.
	cutoff := newest.Add(-time.Minute)
	for i := range candidates {
		candidates[i].live = !candidates[i].modTime.Before(cutoff)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].live != candidates[j].live {
			return candidates[i].live
		}
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	return candidates
}

func discoverLogFiles(path string) ([]string, error) {
	if strings.ContainsAny(path, "*?[") {
		return filepath.Glob(path)
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var paths []string
	err = filepath.WalkDir(path, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && isLogFile(entry.Name()) {
			paths = append(paths, filePath)
		}
		return nil
	})
	return paths, err
}

func isLogFile(name string) bool {
	name = strings.ToLower(name)
	if strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".log") {
		return true
	}
	marker := strings.LastIndex(name, ".log.")
	if marker < 0 || marker+5 == len(name) {
		return false
	}
	_, err := strconv.ParseUint(name[marker+5:], 10, 32)
	return err == nil
}

func (a *app) handleOverview(w http.ResponseWriter, r *http.Request) {
	span, _ := chartRange(r.URL.Query().Get("range"))
	o, err := a.store.Overview(r.Context(), time.Now(), span)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	respond(w, o)
}

func (a *app) handleTimeSeries(w http.ResponseWriter, r *http.Request) {
	span, bucket := chartRange(r.URL.Query().Get("range"))
	series, err := a.store.Series(r.Context(), time.Now(), span, bucket)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	respond(w, series)
}

func chartRange(value string) (time.Duration, time.Duration) {
	switch value {
	case "15m":
		return 15 * time.Minute, time.Minute
	case "1h":
		return time.Hour, time.Minute
	case "6h":
		return 6 * time.Hour, 5 * time.Minute
	case "24h":
		return 24 * time.Hour, 15 * time.Minute
	case "7d":
		return 7 * 24 * time.Hour, time.Hour
	default:
		return time.Hour, time.Minute
	}
}

func (a *app) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	span, _ := chartRange(r.URL.Query().Get("range"))
	result, err := a.store.Search(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("status"), limit, time.Now(), span)
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	respond(w, result)
}

func (a *app) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	span, _ := chartRange(r.URL.Query().Get("range"))
	result, err := a.store.Analytics(r.Context(), time.Now(), span)
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	respond(w, result)
}

func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func problem(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
