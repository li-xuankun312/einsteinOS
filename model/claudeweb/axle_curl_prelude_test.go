package claudeweb

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCurlPreludeIsBuiltFromNumbers(t *testing.T) {
	t.Setenv("EINSTEIN_AXLE_CONNECT_TIMEOUT", "")
	t.Setenv("EINSTEIN_AXLE_CURL_RETRIES", "")
	want := "curl() { command curl --connect-timeout 8 --retry 5 --retry-delay 1 \"$@\"; }\n"
	if got := axleCurlPrelude(); got != want {
		t.Fatalf("defaults:\n got %q\nwant %q", got, want)
	}
	t.Setenv("EINSTEIN_AXLE_CONNECT_TIMEOUT", "3")
	t.Setenv("EINSTEIN_AXLE_CURL_RETRIES", "9")
	if got := axleCurlPrelude(); !strings.Contains(got, "--connect-timeout 3 --retry 9 ") {
		t.Fatalf("env: %q", got)
	}
	t.Setenv("EINSTEIN_AXLE_CONNECT_TIMEOUT", "garbage")
	t.Setenv("EINSTEIN_AXLE_CURL_RETRIES", "-2")
	if got := axleCurlPrelude(); !strings.Contains(got, "--connect-timeout 8 --retry 5 ") || strings.Contains(got, "%!") {
		t.Fatalf("bad values fall back to the defaults, and the format is undamaged: %q", got)
	}
}

// accepts TCP and holds the first n connections without ever speaking TLS:
// "conn=0.001 tls=0.000000", the way 14 of 34 requests failed in the Axle probe
type dropFirst struct {
	net.Listener
	mu   sync.Mutex
	n    int
	seen int
	held []net.Conn
}

func (l *dropFirst) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		l.seen++
		drop := l.seen <= l.n
		if drop {
			l.held = append(l.held, c)
		}
		l.mu.Unlock()
		if !drop {
			return c, nil
		}
	}
}

func hungHandshakeServer(t *testing.T, drop int) (url string, l *dropFirst) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hello") }))
	l = &dropFirst{Listener: srv.Listener, n: drop}
	srv.Listener = l
	srv.StartTLS()
	t.Cleanup(func() {
		srv.Close()
		l.mu.Lock()
		for _, c := range l.held {
			c.Close()
		}
		l.mu.Unlock()
	})
	return srv.URL, l
}

func TestAHungHandshakeIsRetriedByCurlAndTheLoopCompletes(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	t.Setenv("EINSTEIN_AXLE_CONNECT_TIMEOUT", "1")
	t.Setenv("EINSTEIN_AXLE_CURL_RETRIES", "5")
	t.Setenv("EINSTEIN_AXLE_IDLE", "60s")
	url, l := hungHandshakeServer(t, 2) // the first two connections never answer TLS
	cmd := fmt.Sprintf(`for f in a b; do echo "== $f"; curl -sk %s; echo; done`, url)

	t0 := time.Now()
	out := axleExecMulti(cmd, "")
	took := time.Since(t0)
	if strings.Count(out, "hello") != 2 || !strings.Contains(out, "== b") || !strings.HasPrefix(out, "[pid 0 exit=0]") {
		t.Fatalf("both requests must come back, the first after two hung handshakes: %q", out)
	}
	if took > 15*time.Second {
		t.Fatalf("took %s: each hung handshake should cost about the 1 s start window", took)
	}
	l.mu.Lock()
	seen := l.seen
	l.mu.Unlock()
	if seen < 4 { // 2 hung + 1 good for the first request + 1 for the second (curl may reuse nothing across invocations)
		t.Fatalf("expected curl to have reconnected after the hung handshakes, connections seen: %d", seen)
	}
}

// what the child's own curl does today: nothing ends it but the idle guard
func TestWithoutTheStartWindowTheSameLoopHangsUntilTheIdleGuard(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	url, _ := hungHandshakeServer(t, 1000) // every handshake hangs
	cmd := fmt.Sprintf(`for f in a b; do echo "== $f"; curl -sk %s; echo; done`, url)
	t0 := time.Now()
	run := axleRunGuarded(cmd, "", false, 60*time.Second, 2*time.Second)
	if !run.TimedOut || !strings.Contains(run.Stdout, "== a") || strings.Contains(run.Stdout, "== b") {
		t.Fatalf("the loop must be stuck on its first request: %+v", run)
	}
	if time.Since(t0) > 8*time.Second {
		t.Fatalf("the idle guard took %s", time.Since(t0))
	}
}

func TestSlowButHealthyRequestsAreNotCutByTheStartWindow(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	t.Setenv("EINSTEIN_AXLE_CONNECT_TIMEOUT", "1")
	// the handshake is instant, the ANSWER takes 2.5 s (a proof that really takes a while): longer than the start window
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2500 * time.Millisecond)
		fmt.Fprint(w, "slow-but-fine")
	}))
	defer srv.Close()
	run := axleRunGuarded(axleCurlPrelude()+fmt.Sprintf(`curl -sk %s; echo " rc=$?"`, srv.URL), "", false, 30*time.Second, 20*time.Second)
	if run.TimedOut || !strings.Contains(run.Stdout, "slow-but-fine rc=0") {
		t.Fatalf("the start window limits the CONNECTION only; a slow answer must still arrive: %+v", run)
	}
}

func TestConnectionTraceCountsNewAndReusedAndLogsSlowHandshakes(t *testing.T) {
	var st connStats
	tr := st.trace()
	tr.GotConn(httptrace.GotConnInfo{Reused: false})
	tr.GotConn(httptrace.GotConnInfo{Reused: true})
	tr.GotConn(httptrace.GotConnInfo{Reused: true})
	if st.fresh.Load() != 1 || st.reused.Load() != 2 {
		t.Fatalf("fresh=%d reused=%d", st.fresh.Load(), st.reused.Load())
	}
	tr.TLSHandshakeStart()
	tr.TLSHandshakeDone(tls.ConnectionState{}, nil) // instant and fine: not counted
	if st.slow.Load() != 0 {
		t.Fatal("a fast, successful handshake is not an event")
	}
	tr.TLSHandshakeStart()
	tr.TLSHandshakeDone(tls.ConnectionState{}, fmt.Errorf("net/http: TLS handshake timeout"))
	if st.slow.Load() != 1 {
		t.Fatal("a failed handshake is counted")
	}
}
