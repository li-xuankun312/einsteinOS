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
	"crypto/sha256"
	"encoding/hex"
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
	// cap: how many requests the window lets out now. It is limit until the proxy answers
	// 429 with fewer than limit of ours in its ten minutes — its count is shared with whoever
	// else uses the account, which this process cannot see. Then cap is what the proxy did
	// take (throttled); every full window without a 429 gives one back, up to limit.
	cap      int
	capSince time.Time // when cap last changed
	// took: the most of ours the proxy answered within one window (answered), measured, and
	// after a refusal what it took then. A refusal with fewer of ours than that is the shared
	// count spent by others on the same car, not a smaller count for us.
	took int
	// account: whose requests the slots are (windowAccount): the proxy counts per account, and
	// slots in the file of another account — or of none — are not counted
	account   string
	otherSaid bool
}

func newAPIWindow(limit int, dur time.Duration) *apiWindow {
	return &apiWindow{limit: limit, cap: limit, dur: dur, now: time.Now, sleep: time.Sleep}
}

// throttled: the proxy answered 429 to a request of ours. The window lets out no more than the
// proxy took in its ten minutes: the requests of ours it holds, less the one it refused. 11/19
// 06:23–06:33 sixteen units side by side met a 429 at 06:23:49, 06:28:04, 06:29:14, 06:30:08 and
// 06:32:59 while this window still let nine out — each a 2 minute wait and one of a request's
// six tries.
func (w *apiWindow) throttled() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mergeFromDiskLocked()
	now := w.now()
	w.pruneLocked(now)
	n := len(w.times)
	if n < w.took {
		// 11/19 the proxy took nine of ours before 12:00, then refused at 12:21:39 with five and
		// at 12:25:48 with four: "当前车队模型…提问频率过快", the car's count, spent by others
		log.Printf("api_throttle: the proxy refused a request with %d of ours in the last %v, fewer than the %d it has taken — the car's shared count was spent by others; the window stays at %d",
			n, w.dur, w.took, w.cap)
		return
	}
	took := n - 1
	if took < 1 {
		took = 1
	}
	w.took = took
	if took < w.cap {
		log.Printf("api_throttle: the proxy refused a request with %d of ours in the last %v — the window lets %d out until %v pass without a refusal",
			len(w.times), w.dur, took, w.dur)
		w.cap = took
	}
	w.capSince = now
}

// answered: the proxy answered a request of ours; the requests of ours in the window now are
// that many it takes.
func (w *apiWindow) answered() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked(w.now())
	if n := len(w.times); n > w.took {
		w.took = n
	}
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

// windowAccount: the account a cookie is, as the proxy tells accounts apart — its share-session
// (claude.ai's sessionKey where there is none), hashed: the window file never holds the
// session. 11/19 the account changed with share-session while lastActiveOrg stayed the same.
func windowAccount(cookie string) string {
	for _, name := range []string{"share-session", "sessionKey"} {
		for _, part := range strings.Split(cookie, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if ok && k == name && v != "" {
				sum := sha256.Sum256([]byte(name + "=" + v))
				return hex.EncodeToString(sum[:6])
			}
		}
	}
	return ""
}

// setAccount: the slots are this account's; what the window held of another is let go.
func (w *apiWindow) setAccount(account string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if account == w.account {
		return
	}
	w.account = account
	w.times = nil
	w.mergeFromDiskLocked()
}

// windowFile: the window as kept on disk — whose slots, and when each was taken.
type windowFile struct {
	Account string  `json:"account"`
	Times   []int64 `json:"times"`
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
	var file windowFile
	if json.Unmarshal(b, &file) != nil {
		// before the account was kept the file was a bare list: no account's
		if json.Unmarshal(b, &file.Times) != nil {
			return
		}
	}
	if file.Account != w.account {
		if !w.otherSaid {
			w.otherSaid = true
			log.Printf("api_throttle: the %d request(s) in %s are another account's — not counted for this one", len(file.Times), w.path)
		}
		return
	}
	nanos := file.Times
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
	b, _ := json.Marshal(windowFile{Account: w.account, Times: nanos})
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
		// a full window without a refusal gives one request back
		for w.cap < w.limit && !w.capSince.IsZero() && now.Sub(w.capSince) >= w.dur {
			w.cap++
			w.capSince = w.capSince.Add(w.dur)
			log.Printf("api_throttle: %v without a refusal from the proxy — the window lets %d out", w.dur, w.cap)
		}
		if len(w.times) < w.cap {
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
