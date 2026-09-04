package logindex

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nswl-ui/internal/nswl"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

const (
	ingestBatchSize  = 500
	ingestBatchPause = 100 * time.Millisecond
)

type Overview struct {
	Requests int64     `json:"requests"`
	Errors   int64     `json:"errors"`
	Bytes    int64     `json:"bytes"`
	AvgMS    float64   `json:"avg_ms"`
	Updated  time.Time `json:"updated"`
}

type TimePoint struct {
	Timestamp time.Time `json:"timestamp"`
	Count     int64     `json:"count"`
}

type TimeSeries struct {
	Points        []TimePoint `json:"points"`
	BucketSeconds int         `json:"bucket_seconds"`
	Total         int64       `json:"total"`
	Peak          int64       `json:"peak"`
}

type SearchResult struct {
	Items []nswl.Entry `json:"items"`
	Count int64        `json:"count"`
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single connection keeps PRAGMAs consistent. Ingestion commits small
	// batches, so interactive reads are only paused for a few milliseconds.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init() error {
	_, err := s.db.Exec(`
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA busy_timeout=5000;
PRAGMA temp_store=MEMORY;
CREATE TABLE IF NOT EXISTS requests (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  client_ip TEXT, proxy_ip TEXT, server_ip TEXT, host TEXT,
  method TEXT, uri TEXT, status INTEGER, bytes INTEGER,
  duration_ms REAL, user_agent TEXT
);
CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts DESC);
CREATE INDEX IF NOT EXISTS idx_requests_status_ts ON requests(status, ts DESC);
CREATE VIRTUAL TABLE IF NOT EXISTS request_search USING fts5(
  client_ip, proxy_ip, server_ip, method, uri,
  content='requests', content_rowid='id', tokenize='trigram'
);
CREATE TRIGGER IF NOT EXISTS requests_ai AFTER INSERT ON requests BEGIN
  INSERT INTO request_search(rowid, client_ip, proxy_ip, server_ip, method, uri)
  VALUES (new.id, new.client_ip, new.proxy_ip, new.server_ip, new.method, new.uri);
END;
CREATE TRIGGER IF NOT EXISTS requests_ad AFTER DELETE ON requests BEGIN
  INSERT INTO request_search(request_search, rowid, client_ip, proxy_ip, server_ip, method, uri)
  VALUES ('delete', old.id, old.client_ip, old.proxy_ip, old.server_ip, old.method, old.uri);
END;
CREATE TABLE IF NOT EXISTS sources (
  fingerprint TEXT PRIMARY KEY, path TEXT NOT NULL, offset INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS totals (
  id INTEGER PRIMARY KEY CHECK (id=1), requests INTEGER NOT NULL, errors INTEGER NOT NULL,
  bytes INTEGER NOT NULL, duration_total REAL NOT NULL, updated_at INTEGER NOT NULL
);
INSERT OR IGNORE INTO totals VALUES (1, 0, 0, 0, 0, 0);
CREATE TABLE IF NOT EXISTS minute_stats (
  bucket INTEGER PRIMARY KEY, requests INTEGER NOT NULL, errors INTEGER NOT NULL,
  bytes INTEGER NOT NULL, duration_total REAL NOT NULL
);
`)
	return err
}

func (s *Store) sourceOffset(ctx context.Context, fingerprint string) (int64, error) {
	var offset int64
	err := s.db.QueryRowContext(ctx, `SELECT offset FROM sources WHERE fingerprint=?`, fingerprint).Scan(&offset)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return offset, err
}

// IngestFile reads only complete lines appended since the previous sync.
func (s *Store) IngestFile(ctx context.Context, path string) (int, error) {
	fingerprint, err := fileFingerprint(path)
	if err != nil {
		return 0, err
	}
	if fingerprint == "" {
		return 0, nil
	}
	offset, err := s.sourceOffset(ctx, fingerprint)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() < offset {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}

	reader := bufio.NewReaderSize(f, 256*1024)
	batch := make([]nswl.Entry, 0, ingestBatchSize)
	batchOffset, ingested := offset, 0
	for {
		line, readErr := reader.ReadString('\n')
		if readErr == io.EOF {
			break
		} // keep an incomplete final line for the next sync
		if readErr != nil {
			return ingested, readErr
		}
		batchOffset += int64(len(line))
		if entry, ok := nswl.ParseLine(line); ok {
			batch = append(batch, entry)
		}
		if len(batch) >= ingestBatchSize {
			if err := s.insertBatch(ctx, fingerprint, path, batchOffset, batch); err != nil {
				return ingested, err
			}
			ingested += len(batch)
			batch = batch[:0]
			// Historical imports are deliberately paced. Live traffic normally
			// does not fill a batch between syncs and is therefore not delayed.
			timer := time.NewTimer(ingestBatchPause)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ingested, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if len(batch) > 0 || batchOffset != offset {
		if err := s.insertBatch(ctx, fingerprint, path, batchOffset, batch); err != nil {
			return ingested, err
		}
		ingested += len(batch)
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO sources(fingerprint,path,offset,updated_at) VALUES(?,?,?,?)
ON CONFLICT(fingerprint) DO UPDATE SET path=excluded.path, updated_at=excluded.updated_at`, fingerprint, path, offset, time.Now().Unix())
	}
	return ingested, err
}

func (s *Store) insertBatch(ctx context.Context, fingerprint, path string, offset int64, entries []nswl.Entry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO requests(ts,client_ip,proxy_ip,server_ip,host,method,uri,status,bytes,duration_ms,user_agent) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	type aggregate struct {
		requests, errors, bytes int64
		duration                float64
	}
	minutes := make(map[int64]aggregate)
	var total aggregate
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.Timestamp.UnixMilli(), e.ClientIP, e.ProxyIP, e.ServerIP, e.Host, e.Method, e.URI, e.Status, e.Bytes, e.Duration, e.UserAgent); err != nil {
			return err
		}
		a := minutes[e.Timestamp.Truncate(time.Minute).Unix()]
		a.requests++
		a.bytes += e.Bytes
		a.duration += e.Duration
		if e.Status >= 400 {
			a.errors++
		}
		minutes[e.Timestamp.Truncate(time.Minute).Unix()] = a
		total.requests++
		total.bytes += e.Bytes
		total.duration += e.Duration
		if e.Status >= 400 {
			total.errors++
		}
	}
	for bucket, a := range minutes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO minute_stats(bucket,requests,errors,bytes,duration_total) VALUES(?,?,?,?,?)
ON CONFLICT(bucket) DO UPDATE SET requests=requests+excluded.requests, errors=errors+excluded.errors, bytes=bytes+excluded.bytes, duration_total=duration_total+excluded.duration_total`, bucket, a.requests, a.errors, a.bytes, a.duration); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE totals SET requests=requests+?, errors=errors+?, bytes=bytes+?, duration_total=duration_total+?, updated_at=? WHERE id=1`, total.requests, total.errors, total.bytes, total.duration, time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sources(fingerprint,path,offset,updated_at) VALUES(?,?,?,?)
ON CONFLICT(fingerprint) DO UPDATE SET path=excluded.path, offset=excluded.offset, updated_at=excluded.updated_at`, fingerprint, path, offset, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func fileFingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 256*1024).ReadString('\n')
	if err == io.EOF && line == "" {
		return "", nil
	}
	if err != nil && err != io.EOF {
		return "", err
	}
	sum := sha256.Sum256([]byte(line))
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) Overview(ctx context.Context) (Overview, error) {
	var o Overview
	var duration float64
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT requests,errors,bytes,duration_total,updated_at FROM totals WHERE id=1`).Scan(&o.Requests, &o.Errors, &o.Bytes, &duration, &updated)
	if o.Requests > 0 {
		o.AvgMS = duration / float64(o.Requests)
	}
	o.Updated = time.Unix(updated, 0)
	return o, err
}

func (s *Store) Search(ctx context.Context, query, status string, limit int) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	from, where := `requests r`, []string{"1=1"}
	args := []any{}
	if query != "" {
		if len([]rune(query)) < 3 {
			return SearchResult{}, fmt.Errorf("search requires at least 3 characters")
		}
		from = `request_search JOIN requests r ON r.id=request_search.rowid`
		where = append(where, `request_search MATCH ?`)
		args = append(args, `"`+strings.ReplaceAll(query, `"`, `""`)+`"`)
	}
	switch status {
	case "errors":
		where = append(where, `r.status>=400`)
	case "2xx":
		where = append(where, `r.status>=200 AND r.status<300`)
	}
	condition := strings.Join(where, " AND ")
	result := SearchResult{Items: make([]nswl.Entry, 0)}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+from+` WHERE `+condition, args...).Scan(&result.Count); err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.ts,r.client_ip,r.proxy_ip,r.server_ip,r.host,r.method,r.uri,r.status,r.bytes,r.duration_ms,r.user_agent FROM `+from+` WHERE `+condition+` ORDER BY r.ts DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var e nswl.Entry
		var ts int64
		if err := rows.Scan(&ts, &e.ClientIP, &e.ProxyIP, &e.ServerIP, &e.Host, &e.Method, &e.URI, &e.Status, &e.Bytes, &e.Duration, &e.UserAgent); err != nil {
			return result, err
		}
		e.Timestamp = time.UnixMilli(ts)
		result.Items = append(result.Items, e)
	}
	return result, rows.Err()
}

func (s *Store) Series(ctx context.Context, now time.Time, span, bucket time.Duration) (TimeSeries, error) {
	end := now.Truncate(bucket).Add(bucket)
	start := end.Add(-span)
	series := TimeSeries{Points: make([]TimePoint, int(span/bucket)), BucketSeconds: int(bucket.Seconds())}
	for i := range series.Points {
		series.Points[i].Timestamp = start.Add(time.Duration(i) * bucket)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT bucket,requests FROM minute_stats WHERE bucket>=? AND bucket<? ORDER BY bucket`, start.Unix(), end.Unix())
	if err != nil {
		return series, err
	}
	defer rows.Close()
	for rows.Next() {
		var minute, requests int64
		if err := rows.Scan(&minute, &requests); err != nil {
			return series, err
		}
		index := int(time.Unix(minute, 0).Sub(start) / bucket)
		if index < 0 || index >= len(series.Points) {
			continue
		}
		series.Points[index].Count += requests
		series.Total += requests
		if series.Points[index].Count > series.Peak {
			series.Peak = series.Points[index].Count
		}
	}
	return series, rows.Err()
}
