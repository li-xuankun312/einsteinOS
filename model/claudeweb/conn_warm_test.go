package claudeweb

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stallListener holds the first n connections without answering: their TLS handshake
// never completes, like the proxy path after an idle gap.
type stallListener struct {
	net.Listener
	n    int32
	seen atomic.Int32
	mu   sync.Mutex
	held []net.Conn
}

func (l *stallListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.seen.Add(1) <= l.n {
			l.mu.Lock()
			l.held = append(l.held, c)
			l.mu.Unlock()
			continue
		}
		return c, nil
	}
}

func TestWarmRacesHandshakesAndLeavesALiveConnection(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	sl := &stallListener{Listener: srv.Listener, n: 2}
	srv.Listener = sl
	srv.StartTLS()
	defer srv.Close()

	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSHandshakeTimeout = 2 * time.Second
	c := NewClient(ClientConfig{BaseURL: srv.URL, HTTPClient: &http.Client{Transport: tr}})

	if !c.coldPool() {
		t.Fatal("a new client has a cold pool")
	}
	start := time.Now()
	if !c.warm("test") {
		t.Fatal("one of three racers should have completed")
	}
	// two handshakes stall; the third completes at once — not 2 x 2 s of retries first
	if d := time.Since(start); d > time.Second {
		t.Fatalf("warm took %s: the racers did not run at the same time", d)
	}
	// the next request reuses the connection the winner left
	var reused bool
	req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused },
	}), http.MethodGet, srv.URL+"/x", nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !reused {
		t.Fatal("the request opened a new connection instead of using the warm one")
	}

	// all racers stalled: warm says so, within one racer's budget
	sl2 := &stallListener{n: 100}
	srv2 := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	sl2.Listener = srv2.Listener
	srv2.Listener = sl2
	srv2.StartTLS()
	defer srv2.Close()
	tr2 := srv2.Client().Transport.(*http.Transport).Clone()
	tr2.TLSHandshakeTimeout = 500 * time.Millisecond
	c2 := NewClient(ClientConfig{BaseURL: srv2.URL, HTTPClient: &http.Client{Transport: tr2}})
	if c2.warm("test") {
		t.Fatal("no racer could complete")
	}
	if !errIsHandshake(&net.OpError{Err: errString("net/http: TLS handshake timeout")}) {
		t.Fatal("errIsHandshake")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
