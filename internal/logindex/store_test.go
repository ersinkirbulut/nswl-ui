package logindex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nswl-ui/internal/nswl"
)

func TestIngestAndIndexedSearch(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "index.db"), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	logPath := filepath.Join(dir, "Ex260904.log")
	first := `"2026-09-04 15:28:31|104.23.239.69|443|55438|-|151.250.12.216|HTTP/1.1|172.22.62.88|80|GET|/v1/banner/landing-orta|-|200|0|83|40371|Mozilla/5.0|-|-"` + "\n"
	if err := os.WriteFile(logPath, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	count, err := store.IngestFile(ctx, logPath)
	if err != nil || count != 1 {
		t.Fatalf("first ingest count=%d err=%v", count, err)
	}
	count, err = store.IngestFile(ctx, logPath)
	if err != nil || count != 0 {
		t.Fatalf("duplicate ingest count=%d err=%v", count, err)
	}

	second := `"2026-09-04 15:28:32|172.68.0.1|443|1234|-|203.0.113.42|HTTP/1.1|172.22.62.93|443|POST|/v1/session/login|-|401|151|176|62500|Mobile Client|-|-"` + "\n"
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(second); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	count, err = store.IngestFile(ctx, logPath)
	if err != nil || count != 1 {
		t.Fatalf("append ingest count=%d err=%v", count, err)
	}

	now := time.Date(2026, 9, 4, 15, 29, 0, 0, time.Local)
	result, err := store.Search(ctx, "session/log", "errors", 200, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || len(result.Items) != 1 || result.Items[0].ClientIP != "203.0.113.42" {
		t.Fatalf("unexpected search: %#v", result)
	}
	overview, err := store.Overview(ctx, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Requests != 2 || overview.Errors != 1 || overview.Bytes != 259 {
		t.Fatalf("unexpected overview: %#v", overview)
	}
	analytics, err := store.Analytics(ctx, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(analytics.Endpoints) != 2 || len(analytics.Backends) != 2 || len(analytics.Statuses) != 2 {
		t.Fatalf("unexpected analytics: %#v", analytics)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	for input, want := range map[string]string{
		"/v3/player/coupon/7475fac9-1b3d-4aa2-9a91-b010275de0a0/detail?full=true": "/v3/player/coupon/:id/detail",
		"/v1/items/29187":         "/v1/items/:id",
		"/v1/banner/landing-orta": "/v1/banner/landing-orta",
	} {
		if got := normalizeEndpoint(input); got != want {
			t.Errorf("normalizeEndpoint(%q)=%q, want %q", input, got, want)
		}
	}
}

func BenchmarkIndexedSearch100K(b *testing.B) {
	store, err := Open(filepath.Join(b.TempDir(), "bench.db"), time.Local)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for start := 0; start < 100_000; start += 2000 {
		batch := make([]nswl.Entry, 2000)
		for i := range batch {
			n := start + i
			uri := fmt.Sprintf("/v1/items/%d", n)
			if n%1000 == 0 {
				uri = fmt.Sprintf("/v1/target-endpoint/%d", n)
			}
			batch[i] = nswl.Entry{Timestamp: time.Unix(int64(n), 0), ClientIP: fmt.Sprintf("10.0.%d.%d", n/256%256, n%256), Method: "GET", URI: uri, Status: 200, Bytes: 100, Duration: 12}
		}
		if err := store.insertBatch(ctx, "benchmark", "benchmark.log", int64(start+len(batch)), batch); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := store.Search(ctx, "target-endpoint", "", 200, time.Unix(100_000, 0), 48*time.Hour)
		if err != nil {
			b.Fatal(err)
		}
		if result.Count != 100 {
			b.Fatalf("got %d results", result.Count)
		}
	}
}

func TestSeriesUsesMinuteAggregates(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "index.db"), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	logPath := filepath.Join(dir, "Ex260904.log")
	line := `"2026-09-04 15:28:31|104.23.239.69|443|55438|-|151.250.12.216|HTTP/1.1|172.22.62.88|80|GET|/v1/banner|-|200|0|83|40371|Mozilla/5.0|-|-"` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestFile(context.Background(), logPath); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 15, 29, 0, 0, time.Local)
	series, err := store.Series(context.Background(), now, 15*time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if series.Total != 1 || series.Peak != 1 || len(series.Points) != 15 {
		t.Fatalf("unexpected series: %#v", series)
	}
}

func TestSeriesAnchorsToLatestHistoricalData(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "index.db"), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	logPath := filepath.Join(dir, "historical.log")
	line := `"2026-09-04 15:28:31|104.23.239.69|443|55438|-|151.250.12.216|HTTP/1.1|172.22.62.88|80|GET|/v1/banner|-|200|0|83|40371|Mozilla/5.0|-|-"` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestFile(context.Background(), logPath); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 18, 7, 0, 0, time.Local)
	series, err := store.Series(context.Background(), now, time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if series.Total != 1 || series.Peak != 1 {
		t.Fatalf("historical series should contain latest data: %#v", series)
	}
}

func TestIngestFileLimitContinuesFromSavedOffset(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "index.db"), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var content strings.Builder
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&content, "\"2026-09-04 15:28:31|104.23.239.69|443|%d|-|151.250.12.216|HTTP/1.1|172.22.62.88|80|GET|/v1/items/%d|-|200|0|83|40371|Mozilla/5.0|-|-\"\n", 10000+i, i)
	}
	logPath := filepath.Join(dir, "active.log")
	if err := os.WriteFile(logPath, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if count, err := store.IngestFileLimit(ctx, logPath, 500); err != nil || count != 500 {
		t.Fatalf("first chunk count=%d err=%v", count, err)
	}
	if count, err := store.IngestFileLimit(ctx, logPath, 500); err != nil || count != 100 {
		t.Fatalf("second chunk count=%d err=%v", count, err)
	}
	overview, err := store.Overview(ctx, time.Date(2026, 9, 4, 15, 29, 0, 0, time.Local), time.Hour)
	if err != nil || overview.Requests != 600 {
		t.Fatalf("overview=%#v err=%v", overview, err)
	}
}
