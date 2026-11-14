package claudeweb

// api_window.go — the request budget, counted the way the proxy counts it.
//
// The proxy in front of claude.ai answers 429 "最近10分钟内 提问10次" when ten
// questions arrived in ten minutes. The old accounting was a ring of nine slots
// filled once per Completion() call, in memory, and it went wrong in three ways:
//
//   - a retry is another POST. A 429 that is retried, a 5xx, a slow retry: each one
//     that reaches the server is a question to the proxy, and none of them took a slot;
//   - the ring was empty at every start. A restart gives the process nine free slots
//     while the proxy still remembers what the last process sent a minute ago, so a
//     fresh process can get 429 on its very first request (2026/11/02 06:53: the
//     first attempt of the first conversation after a restart);
//   - a second einsteinOS on the same machine could not see the first.
//
// Now: every attempt that reaches the server holds a slot until it is ten minutes
// old; an attempt that provably never left this machine (TLS handshake timeout,
// connection refused, DNS) gives its slot back; the slots are kept in a small file
// and re-read before each request, so a restart or a second process starts from
// what was really sent.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type apiWindow struct {
	mu    sync.Mutex
	times []time.Time // ascending
	path  string
	limit int
	dur   time.Duration
	now   func() time.Time
	sleep func(time.Duration)
}

func newAPIWindow(limit int, dur time.Duration) *apiWindow {
	return &apiWindow{limit: limit, dur: dur, now: time.Now, sleep: time.Sleep}
}

// the proxy says ten; one in reserve for a question that is not ours (a browser tab).
var apiWin = newAPIWindow(9, 10*time.Minute)

// SetAPIWindowPath turns on persistence: the window is loaded from path and kept
// there. An empty path leaves it in memory only.
func SetAPIWindowPath(path string) {
	apiWin.mu.Lock()
	defer apiWin.mu.Unlock()
	apiWin.path = path
	apiWin.mergeFromDiskLocked()
}

func (w *apiWindow) pruneLocked(now time.Time) {
	cut := 0
	for cut < len(w.times) && now.Sub(w.times[cut]) >= w.dur {
		cut++
	}
	w.times = w.times[cut:]
}

// mergeFromDiskLocked folds the slots another process (or an earlier run of this
// one) recorded into ours.
func (w *apiWindow) mergeFromDiskLocked() {
	if w.path == "" {
		return
	}
	b, err := os.ReadFile(w.path)
	if err != nil {
		return
	}
	var nanos []int64
	if json.Unmarshal(b, &nanos) != nil {
		return
	}
	have := make(map[int64]bool, len(w.times))
	for _, t := range w.times {
		have[t.UnixNano()] = true
	}
	for _, n := range nanos {
		if !have[n] {
			w.times = append(w.times, time.Unix(0, n))
			have[n] = true
		}
	}
	sort.Slice(w.times, func(i, j int) bool { return w.times[i].Before(w.times[j]) })
	w.pruneLocked(w.now())
}

func (w *apiWindow) saveLocked() {
	if w.path == "" {
		return
	}
	nanos := make([]int64, len(w.times))
	for i, t := range w.times {
		nanos[i] = t.UnixNano()
	}
	b, _ := json.Marshal(nanos)
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return
	}
	tmp := w.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, w.path)
	}
}

// acquire blocks until a slot is free, takes it, and returns its time (the handle
// refund needs).
func (w *apiWindow) acquire() time.Time {
	for {
		w.mu.Lock()
		w.mergeFromDiskLocked()
		now := w.now()
		w.pruneLocked(now)
		if len(w.times) < w.limit {
			w.times = append(w.times, now)
			w.saveLocked()
			w.mu.Unlock()
			return now
		}
		wait := w.times[0].Add(w.dur).Sub(now) + time.Second
		n := len(w.times)
		w.mu.Unlock()
		log.Printf("api_throttle: %d requests in the last %v, sleeping %v",
			n, w.dur, wait.Truncate(time.Second))
		w.sleep(wait)
	}
}

// refund gives back a slot whose request never left this machine.
func (w *apiWindow) refund(slot time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, t := range w.times {
		if t.Equal(slot) {
			w.times = append(w.times[:i], w.times[i+1:]...)
			break
		}
	}
	w.saveLocked()
}

// used reports how many slots are held right now.
func (w *apiWindow) used() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mergeFromDiskLocked()
	w.pruneLocked(w.now())
	return len(w.times)
}

// requestNeverSent reports whether an error from http.Client.Do means the request
// was not delivered: it failed while connecting, so the proxy cannot have counted
// it. Anything unclear (a reset, EOF, a timeout waiting for the response) is
// treated as sent: counting a question that was not asked costs a little waiting;
// not counting one that was costs a 429.
func requestNeverSent(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, m := range []string{
		"TLS handshake timeout", "dial tcp", "no such host", "connection refused",
		"proxyconnect", "network is unreachable", "x509:",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}
