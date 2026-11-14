package claudeweb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// a window with a controllable clock whose sleep advances the clock
func testWindow(limit int) (*apiWindow, *time.Time, *[]time.Duration) {
	clock := time.Unix(1_700_000_000, 0)
	var slept []time.Duration
	w := newAPIWindow(limit, 10*time.Minute)
	w.now = func() time.Time { return clock }
	w.sleep = func(d time.Duration) { slept = append(slept, d); clock = clock.Add(d) }
	return w, &clock, &slept
}

func TestWindowBlocksTheTenthRequestUntilTheOldestAgesOut(t *testing.T) {
	w, clock, slept := testWindow(3)
	for i := 0; i < 3; i++ {
		w.acquire()
		*clock = clock.Add(time.Minute)
	}
	if len(*slept) != 0 {
		t.Fatal("the first three must not wait")
	}
	w.acquire() // the oldest is 3 minutes old: wait ~7 minutes (+1s margin)
	if len(*slept) != 1 || (*slept)[0] < 7*time.Minute || (*slept)[0] > 7*time.Minute+5*time.Second {
		t.Fatalf("slept %v, want about 7m", *slept)
	}
}

func TestEveryAttemptThatReachedTheProxyHoldsASlotButOneThatNeverLeftIsRefunded(t *testing.T) {
	w, _, _ := testWindow(9)
	// a request that got a 429 and its retry that got a 5xx: two questions to the proxy
	w.acquire()
	w.acquire()
	// a TLS handshake timeout: nothing was sent
	slot := w.acquire()
	if !requestNeverSent(errors.New(`Post "https://x": net/http: TLS handshake timeout`)) {
		t.Fatal("a TLS handshake timeout never sent the request")
	}
	w.refund(slot)
	if got := w.used(); got != 2 {
		t.Fatalf("used=%d, want 2: retries that reached the server count, the unsent one does not", got)
	}
}

func TestRequestNeverSentIsConservative(t *testing.T) {
	for msg, want := range map[string]bool{
		"net/http: TLS handshake timeout":                                            true,
		"dial tcp 1.2.3.4:443: i/o timeout":                                          true,
		"dial tcp: lookup c.aimonkey.plus: no such host":                             true,
		"connect: connection refused":                                                true,
		"proxyconnect tcp: dial tcp 127.0.0.1:1080: connect: refused":                true,
		"context deadline exceeded (Client.Timeout exceeded while awaiting headers)": false, // sent, no answer yet
		"read tcp: connection reset by peer":                                         false,
		"unexpected EOF":                                                             false,
	} {
		if got := requestNeverSent(errors.New(msg)); got != want {
			t.Errorf("%q → %v want %v", msg, got, want)
		}
	}
	if requestNeverSent(nil) {
		t.Fatal("no error is not an unsent request")
	}
}

func TestWindowSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "api_window.json")
	w1, clock, _ := testWindow(3)
	w1.path = path
	for i := 0; i < 3; i++ {
		w1.acquire()
		*clock = clock.Add(30 * time.Second)
	}
	// "restart": a new process, same clock, empty memory
	w2, _, slept2 := testWindow(3)
	w2.now = func() time.Time { return *clock }
	w2.sleep = func(d time.Duration) { *slept2 = append(*slept2, d); *clock = clock.Add(d) }
	w2.path = path
	w2.acquire()
	if len(*slept2) != 1 {
		t.Fatalf("a fresh process must see the three requests the last one sent and wait; slept %v", *slept2)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("window file mode %v", st.Mode().Perm())
	}
}

func TestWindowDropsStaleEntriesOnLoadAndToleratesGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.json")
	w, clock, slept := testWindow(2)
	w.path = path
	old := clock.Add(-30 * time.Minute).UnixNano()
	os.WriteFile(path, []byte(fmt.Sprintf("[%d,%d]", old, old+1)), 0o600)
	w.acquire()
	w.acquire()
	if len(*slept) != 0 {
		t.Fatal("entries older than the window must not count")
	}
	os.WriteFile(path, []byte("not json"), 0o600)
	w2, _, _ := testWindow(2)
	w2.path = path
	w2.acquire() // must not panic or hang
}

func TestTwoProcessesShareOneWindowThroughTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.json")
	a, clock, _ := testWindow(2)
	a.path = path
	b, _, slept := testWindow(2)
	b.now = func() time.Time { return *clock }
	b.sleep = func(d time.Duration) { *slept = append(*slept, d); *clock = clock.Add(d) }
	b.path = path

	a.acquire()
	*clock = clock.Add(time.Second)
	a.acquire()
	*clock = clock.Add(time.Second)
	b.acquire() // b never sent anything itself, but a already used the budget
	if len(*slept) != 1 {
		t.Fatalf("the second process must wait for the first one's requests to age out; slept %v", *slept)
	}
}
