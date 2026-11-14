package claudeweb

import (
	"testing"
	"time"
)

// a paused watch does not cancel: a quota sleep has no stream to be silent
func TestAPausedStreamWatchDoesNotCancelDuringASleep(t *testing.T) {
	old := streamIdleMax
	streamIdleMax = 50 * time.Millisecond
	defer func() { streamIdleMax = old }()
	ctx, w := newProgressContext()
	w.pause()
	time.Sleep(150 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("cancelled while paused")
	}
	w.stop()
}
