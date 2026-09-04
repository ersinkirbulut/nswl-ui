package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nswl-ui/internal/nswl"
)

//go:embed web/*
var assets embed.FS

type app struct {
	logPath string
	mu      sync.Mutex
	files   map[string]cachedFile
}

type cachedFile struct {
	size    int64
	modTime time.Time
	entries []nswl.Entry
}
type overview struct {
	Requests int       `json:"requests"`
	Errors   int       `json:"errors"`
	Bytes    int64     `json:"bytes"`
	AvgMS    float64   `json:"avg_ms"`
	Updated  time.Time `json:"updated"`
}

type config struct {
	Port    int    `json:"port"`
	LogPath string `json:"log_path"`
	Bind    string `json:"bind"`
}

func main() {
	configPath := flag.String("config", "config.json", "configuration file")
	flag.Parse()
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	a := &app{logPath: cfg.LogPath, files: make(map[string]cachedFile)}
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
	mux.HandleFunc("GET /api/logs", a.handleLogs)
	addr := fmt.Sprintf("%s:%d", cfg.Bind, cfg.Port)
	log.Printf("NSWL UI listening on %s (logs: %s)", addr, a.logPath)
	log.Fatal(http.ListenAndServe(addr, securityHeaders(mux)))
}

func loadConfig(path string) (config, error) {
	cfg := config{Bind: "127.0.0.1", Port: 8080, LogPath: "./logs"}
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
	if cfg.Bind == "" {
		cfg.Bind = "127.0.0.1"
	}
	return cfg, nil
}

func (a *app) load() ([]nswl.Entry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	paths, err := discoverLogFiles(a.logPath)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(paths))
	var all []nswl.Entry
	for _, path := range paths {
		seen[path] = true
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		cached, exists := a.files[path]
		if exists && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
			all = append(all, cached.entries...)
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		rows, parseErr := nswl.Parse(f)
		_ = f.Close()
		if parseErr != nil {
			log.Printf("skip %s: %v", path, parseErr)
			continue
		}
		a.files[path] = cachedFile{size: info.Size(), modTime: info.ModTime(), entries: rows}
		all = append(all, rows...)
	}
	for path := range a.files {
		if !seen[path] {
			delete(a.files, path)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Timestamp.After(all[j].Timestamp) })
	return all, nil
}

// discoverLogFiles accepts a directory, a single file, or a glob. Directories
// are scanned recursively because NSWL installations commonly rotate into
// date- or virtual-server-based subdirectories.
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
		if entry.IsDir() {
			return nil
		}
		if isLogFile(entry.Name()) {
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
	// NSWL size rotation produces names such as Ex260904.log.0 and .log.1.
	marker := strings.LastIndex(name, ".log.")
	if marker < 0 || marker+5 == len(name) {
		return false
	}
	_, err := strconv.ParseUint(name[marker+5:], 10, 32)
	return err == nil
}

func (a *app) handleOverview(w http.ResponseWriter, _ *http.Request) {
	rows, err := a.load()
	if err != nil {
		problem(w, err)
		return
	}
	o := overview{Requests: len(rows), Updated: time.Now()}
	for _, e := range rows {
		if e.Status >= 400 {
			o.Errors++
		}
		o.Bytes += e.Bytes
		o.AvgMS += e.Duration
	}
	if o.Requests > 0 {
		o.AvgMS /= float64(o.Requests)
	}
	respond(w, o)
}

func (a *app) handleLogs(w http.ResponseWriter, r *http.Request) {
	rows, err := a.load()
	if err != nil {
		problem(w, err)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	status := r.URL.Query().Get("status")
	filtered := rows[:0]
	for _, e := range rows {
		if q != "" && !strings.Contains(strings.ToLower(e.ClientIP+" "+e.Host+" "+e.Method+" "+e.URI+" "+e.UserAgent), q) {
			continue
		}
		if status == "errors" && e.Status < 400 {
			continue
		}
		if status == "2xx" && (e.Status < 200 || e.Status >= 300) {
			continue
		}
		filtered = append(filtered, e)
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	respond(w, map[string]any{"items": filtered, "count": len(filtered)})
}

func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, err error) {
	http.Error(w, fmt.Sprintf(`{"error":%q}`, err), http.StatusInternalServerError)
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
