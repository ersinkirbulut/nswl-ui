package nswl

import (
	"bufio"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Entry is the normalized subset of a NSWL W3C record used by the UI.
type Entry struct {
	Timestamp time.Time         `json:"timestamp"`
	ClientIP  string            `json:"client_ip"`
	ProxyIP   string            `json:"proxy_ip,omitempty"`
	ServerIP  string            `json:"server_ip"`
	Host      string            `json:"host"`
	Method    string            `json:"method"`
	URI       string            `json:"uri"`
	Status    int               `json:"status"`
	Bytes     int64             `json:"bytes"`
	Duration  float64           `json:"duration_ms"`
	UserAgent string            `json:"user_agent"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// Parse reads W3C Extended logs. The #Fields directive determines the layout,
// so customized NSWL configurations do not require code changes.
func Parse(r io.Reader) ([]Entry, error) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var fields []string
	var entries []Entry
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#Fields:") {
			fields = strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "#Fields:")))
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if len(fields) == 0 && strings.Contains(line, "|") {
			if entry, ok := parseNSWLPipe(line); ok {
				entries = append(entries, entry)
			}
			continue
		}
		if len(fields) == 0 {
			continue
		}
		values := splitW3C(line)
		if len(values) < len(fields) {
			continue
		}
		m := make(map[string]string, len(fields))
		for i, key := range fields {
			m[strings.ToLower(key)] = dash(values[i])
		}
		entries = append(entries, normalize(m))
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if len(fields) == 0 && len(entries) == 0 {
		return entries, errors.New("missing #Fields directive")
	}
	return entries, nil
}

// parseNSWLPipe handles the common custom format produced with:
// timestamp|ADC IP|server port|client port|user|forwarded client IP|protocol|
// origin IP|origin port|method|URI|query|status|request bytes|response bytes|
// duration usec|user agent/custom headers|referer|cookie.
func parseNSWLPipe(line string) (Entry, bool) {
	line = strings.TrimSpace(line)
	if len(line) >= 2 && line[0] == '"' && line[len(line)-1] == '"' {
		line = line[1 : len(line)-1]
	}
	parts := strings.Split(line, "|")
	if len(parts) != 19 {
		return Entry{}, false
	}
	for i := range parts {
		parts[i] = dash(strings.TrimSpace(parts[i]))
	}
	ts, err := time.Parse("2006-01-02 15:04:05", parts[0])
	if err != nil {
		return Entry{}, false
	}
	status, _ := strconv.Atoi(parts[12])
	bytes, _ := strconv.ParseInt(parts[14], 10, 64)
	usec, _ := strconv.ParseFloat(parts[15], 64)
	uri := parts[10]
	if parts[11] != "" {
		uri += "?" + strings.TrimPrefix(parts[11], "?")
	}
	return Entry{
		Timestamp: ts,
		ProxyIP:   parts[1],
		ClientIP:  parts[5],
		ServerIP:  parts[7],
		Method:    parts[9],
		URI:       uri,
		Status:    status,
		Bytes:     bytes,
		Duration:  usec / 1000,
		UserAgent: parts[16],
		Fields: map[string]string{
			"proxy-port":    parts[2],
			"client-port":   parts[3],
			"user":          parts[4],
			"protocol":      parts[6],
			"server-port":   parts[8],
			"request-bytes": parts[13],
			"referer":       parts[17],
			"cookie":        parts[18],
		},
	}, true
}

func splitW3C(line string) []string {
	var out []string
	var b strings.Builder
	quoted := false
	for _, r := range line {
		switch {
		case r == '"':
			quoted = !quoted
		case (r == ' ' || r == '\t') && !quoted:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func normalize(m map[string]string) Entry {
	e := Entry{Fields: m}
	e.ClientIP = first(m, "c-ip", "cs-ip", "client-ip")
	e.ServerIP = first(m, "s-ip", "server-ip")
	e.Host = first(m, "cs-host", "cs(host)", "host")
	e.Method = first(m, "cs-method", "method")
	stem := first(m, "cs-uri-stem", "cs-uri", "uri", "url-stem")
	query := first(m, "cs-uri-query", "url-query")
	e.URI = stem
	if query != "" {
		e.URI += "?" + strings.TrimPrefix(query, "?")
	}
	e.Status, _ = strconv.Atoi(first(m, "sc-status", "status"))
	e.Bytes, _ = strconv.ParseInt(first(m, "sc-bytes", "bytes"), 10, 64)
	if ms := first(m, "time-taken-ms", "duration-ms"); ms != "" {
		e.Duration, _ = strconv.ParseFloat(ms, 64)
	} else if sec := first(m, "time-taken"); sec != "" {
		v, _ := strconv.ParseFloat(sec, 64)
		e.Duration = v * 1000
	}
	e.UserAgent = decodePlus(first(m, "cs-user-agent", "cs(user-agent)", "user-agent"))
	d := first(m, "date")
	t := first(m, "time")
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04:05.000", time.RFC3339} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(d+" "+t)); err == nil {
			e.Timestamp = parsed
			break
		}
	}
	return e
}

func first(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}
func dash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}
func decodePlus(s string) string {
	if v, err := url.QueryUnescape(s); err == nil {
		return v
	}
	return s
}
