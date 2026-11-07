package claudeweb

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A stream that keeps sending outlives streamIdleMax many times over; one that goes silent
// is cut after streamIdleMax. 11/03: rounds were cut at exactly 10:00 while still sending.
func TestAStreamIsCutWhenSilentNotWhenOld(t *testing.T) {
	old := streamIdleMax
	t.Cleanup(func() { streamIdleMax = old })
	streamIdleMax = 300 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		ticks := 10 // 10 x 100 ms = 1 s of steady output: 3x the idle limit
		if r.URL.Query().Get("silent") != "" {
			ticks = 1
		}
		for i := 0; i < ticks; i++ {
			io.WriteString(w, "data: {\"type\":\"ping\"}\n\n")
			fl.Flush()
			time.Sleep(100 * time.Millisecond)
		}
		if ticks == 1 {
			time.Sleep(2 * time.Second) // then nothing
		}
	}))
	defer srv.Close()

	read := func(path string) (string, error, time.Duration) {
		ctx, w := newProgressContext()
		req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.stop()
			return "", err, 0
		}
		body := &progressBody{ReadCloser: resp.Body, w: w}
		defer body.Close()
		start := time.Now()
		b, err := io.ReadAll(body)
		return string(b), err, time.Since(start)
	}
	got, err, took := read("/")
	if err != nil || strings.Count(got, "ping") != 10 || took < 900*time.Millisecond {
		t.Fatalf("steady stream cut: err=%v pings=%d after %s", err, strings.Count(got, "ping"), took)
	}
	got, err, took = read("/?silent=1")
	if err == nil || strings.Count(got, "ping") != 1 || took > time.Second {
		t.Fatalf("silent stream not cut: err=%v pings=%d after %s", err, strings.Count(got, "ping"), took)
	}
}
