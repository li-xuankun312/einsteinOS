package claudeweb

// stream_progress.go — a completion stream is cut when it stops moving, not when it is old.
//
// Completion's request context was context.WithTimeout(10*time.Minute). On 2026/11/03 the
// first round of dev units 1 and 2 was cut exactly 10:00 after the request (08:59:46 →
// 09:09:46, 09:44:51 → 09:54:51) while the server was still sending: unit 1's round had 26
// tool calls and the kernel had received 18. Each cut needed an SSE recovery (44 s for unit 2,
// 11 m 42 s for unit 1 during the site's outage), and the round-length budget the prompts now
// give makes long rounds the normal case. The stream now lives as long as bytes arrive; it is
// cancelled only after streamIdleMax with none — before the response headers, or between reads.

import (
	"context"
	"io"
	"log"
	"sync"
	"time"
)

// streamIdleMax: no byte for this long and the stream is stuck. Rounds show gaps of 60-75 s
// while the model thinks ("[remote] streaming... (66s since last output)").
var streamIdleMax = 5 * time.Minute

const streamIdleLogFmt = "claudeweb: no byte from the stream for %s — cancelling it (it is judged by progress, not by age)"

// progressWatch cancels its context when touch is not called for streamIdleMax.
type progressWatch struct {
	mu     sync.Mutex
	timer  *time.Timer
	cancel context.CancelFunc
	idle   time.Duration
}

// newProgressContext is the request context of a completion: alive while bytes arrive.
func newProgressContext() (context.Context, *progressWatch) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &progressWatch{cancel: cancel, idle: streamIdleMax}
	w.timer = time.AfterFunc(w.idle, func() {
		log.Printf(streamIdleLogFmt, w.idle)
		cancel()
	})
	return ctx, w
}

func (w *progressWatch) touch() {
	w.mu.Lock()
	w.timer.Reset(w.idle)
	w.mu.Unlock()
}

// stop releases the context (the caller is done with the request or the stream).
func (w *progressWatch) stop() {
	w.mu.Lock()
	w.timer.Stop()
	w.mu.Unlock()
	w.cancel()
}

// progressBody is the stream handed to the caller: every read that brings bytes is progress,
// and closing it releases the context.
type progressBody struct {
	io.ReadCloser
	w *progressWatch
}

func (b *progressBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.w.touch()
	}
	return n, err
}

func (b *progressBody) Close() error {
	err := b.ReadCloser.Close()
	b.w.stop()
	return err
}
