package claudeweb

// conn_trace.go — what the connections to the Claude proxy are doing.
//
// Nearly every request in the logs begins with "TLS handshake timeout" and a retry, and
// the Axle probe showed the same thing on that machine (14 of 34 new connections hung in
// the handshake). Nothing here changes behaviour. It records, per process: how many
// requests opened a new connection and how many reused one, and every handshake that
// failed or took 2 s or more — the numbers needed to set the 8 s handshake timeout
// from data instead of from a guess.

import (
	"crypto/tls"
	"log"
	"net/http/httptrace"
	"sync/atomic"
	"time"
)

type connStats struct {
	fresh, reused, slow atomic.Int64
}

const connStatsEvery = 20 // one summary line per this many requests

func (st *connStats) trace() *httptrace.ClientTrace {
	var start time.Time
	return &httptrace.ClientTrace{
		GotConn: func(i httptrace.GotConnInfo) {
			if i.Reused {
				st.reused.Add(1)
			} else {
				st.fresh.Add(1)
			}
			if n := st.fresh.Load() + st.reused.Load(); n%connStatsEvery == 0 {
				log.Printf("claudeweb: connections: %d new, %d reused, %d slow or failed handshakes (of %d requests)",
					st.fresh.Load(), st.reused.Load(), st.slow.Load(), n)
			}
		},
		TLSHandshakeStart: func() { start = time.Now() },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if d := time.Since(start); err != nil || d >= 2*time.Second {
				st.slow.Add(1)
				log.Printf("claudeweb: tls handshake %s err=%v", d.Round(time.Millisecond), err)
			}
		},
	}
}
