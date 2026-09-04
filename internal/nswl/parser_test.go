package nswl

import (
	"strings"
	"testing"
	"time"
)

func TestParseCustomizedW3C(t *testing.T) {
	in := `#Version: 1.0
#Fields: date time c-ip cs-method cs-uri-stem cs-uri-query sc-status sc-bytes time-taken cs-user-agent
2026-09-04 12:34:56 10.0.0.8 GET /api/health verbose=1 200 512 0.042 "Mozilla/5.0 Test"
`
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries", len(got))
	}
	e := got[0]
	if e.URI != "/api/health?verbose=1" || e.Duration != 42 || e.Status != 200 || e.UserAgent != "Mozilla/5.0 Test" {
		t.Fatalf("unexpected entry: %#v", e)
	}
}

func TestParseCustomTimestampInConfiguredLocation(t *testing.T) {
	location, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Fatal(err)
	}
	line := `"2026-09-04 18:04:31|104.23.239.69|443|55438|-|151.250.12.216|HTTP/1.1|172.22.62.88|80|GET|/health|-|200|0|83|40371|Mozilla/5.0|-|-"`
	entry, ok := ParseLineInLocation(line, location)
	if !ok {
		t.Fatal("line was not parsed")
	}
	if got := entry.Timestamp.UTC().Format("15:04:05"); got != "15:04:31" {
		t.Fatalf("UTC time=%s, want 15:04:31", got)
	}
}

func TestParseCustomPipeFormat(t *testing.T) {
	in := `"2026-09-04 15:28:31|104.23.239.69|443|55438|-|151.250.12.216|HTTP/1.1|172.22.62.88|80|GET|/v1/banner/landing-orta|lang=tr|200|0|83|40371|Mozilla/5.0|-|-"`
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries", len(got))
	}
	e := got[0]
	if e.ClientIP != "151.250.12.216" || e.ProxyIP != "104.23.239.69" || e.ServerIP != "172.22.62.88" {
		t.Fatalf("unexpected addresses: %#v", e)
	}
	if e.URI != "/v1/banner/landing-orta?lang=tr" || e.Duration != 40.371 || e.Bytes != 83 {
		t.Fatalf("unexpected request: %#v", e)
	}
}
