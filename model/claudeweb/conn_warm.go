package claudeweb

// conn_warm.go — have a live connection before a request that cannot afford a slow one.
//
// On 2026/11/03 the first request of chunks 2, 4 and 5 failed its TLS handshake 1, 4
// and 6 times in a row (8 s each, the last one on retry 6 of 6); within a chunk the
// requests reused a connection and went through. The failures come after the storage
// gap, when the pool has nothing live and every attempt is ONE new handshake, tried one
// after another. A handshake on this path either completes in about 2-3 s or never
// does, so trying several at once and keeping the first that completes turns "n tries
// in a row, 9 s each" into "n tries in 8 s".
//
// warm sends warmRacers HEAD requests to the proxy's root at the same moment, through
// the same transport. The first one that completes leaves its connection in the pool,
// and the completion request that follows reuses it. A HEAD to the root is not a
// question to the model and is not counted in the request window (api_window.go).

import (
	"context"
	"log"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"
)

var (
	warmRacers  = 3                // handshakes tried at once
	warmIdle    = 45 * time.Second // a pool idle this long is treated as cold
	warmTimeout = 10 * time.Second // one racer's whole budget
)

const warmLogFmt = "claudeweb: warm-up (%s): %d of %d handshakes completed, first in %s"

// warm races warmRacers fresh connections and returns when the first completes (true) or
// all have failed (false).
func (c *Client) warm(reason string) bool {
	start := time.Now()
	var (
		ok       atomic.Int32
		holding  atomic.Int32 // racers that have a connection and have not given it back
		firstAt  atomic.Int64 // nanoseconds from start to the first completed racer; 0: none yet
		finished atomic.Int32
	)
	for i := 0; i < warmRacers; i++ {
		go func() {
			defer finished.Add(1)
			// no racer is ever cancelled: a HEAD has no body, so the transport hands its
			// connection back the moment the response is read, often straight to another
			// racer still waiting for its own dial; cancelling that racer mid-request
			// closed the connection. Each one ends on its own: done, or its handshake
			// timed out. Its context only bounds it.
			ctx, cancel := context.WithTimeout(context.Background(), warmTimeout)
			got := false
			trace := c.stats.trace()
			gotConn := trace.GotConn
			trace.GotConn = func(g httptrace.GotConnInfo) {
				got = true
				holding.Add(1)
				gotConn(g)
			}
			req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodHead, c.baseURL+"/", nil)
			if err != nil {
				cancel()
				return
			}
			resp, err := c.httpClient.Do(req)
			if err == nil {
				time.AfterFunc(warmTimeout, cancel) // released later, not now (see above)
				resp.Body.Close()
				ok.Add(1)
				firstAt.CompareAndSwap(0, int64(time.Since(start)))
			} else {
				cancel()
			}
			if got {
				holding.Add(-1)
			}
		}()
	}
	// return once one racer has completed and no racer is still using a connection, so
	// the request this warm-up is for finds one idle in the pool; or when all are over
	deadline := time.Now().Add(warmTimeout + time.Second)
	for time.Now().Before(deadline) {
		if (firstAt.Load() > 0 && holding.Load() == 0) || finished.Load() == int32(warmRacers) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if d := firstAt.Load(); d > 0 {
		log.Printf(warmLogFmt, reason, ok.Load(), warmRacers, time.Duration(d).Round(time.Millisecond))
		return true
	}
	log.Printf(warmLogFmt, reason, 0, warmRacers, "-")
	return false
}

// coldPool: nothing went through for warmIdle, so no connection is likely to be live.
func (c *Client) coldPool() bool {
	last := c.lastOK.Load()
	return last == 0 || time.Since(time.Unix(0, last)) > warmIdle
}

func (c *Client) markOK() { c.lastOK.Store(time.Now().UnixNano()) }

// errIsHandshake: the request failed before a connection was up.
func errIsHandshake(err error) bool {
	return err != nil && strings.Contains(err.Error(), "TLS handshake timeout")
}
