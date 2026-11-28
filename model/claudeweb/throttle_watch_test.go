package claudeweb

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Waiting for a request slot is not a silent stream: the request is sent after the wait and
// answered. 11/12 03:42:46 a 5m7s wait for a slot ran into the 5-minute silence limit, and the
// request was cancelled before it was sent ("context canceled").
func TestWaitingForARequestSlotIsNotStreamSilence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	oldWin, oldIdle := apiWin, streamIdleMax
	apiWin = newAPIWindow(1, 100*time.Millisecond)
	apiWin.acquire() // the one slot is taken: the next request waits about 1.1s for it
	streamIdleMax = 200 * time.Millisecond
	t.Cleanup(func() { apiWin, streamIdleMax = oldWin, oldIdle })

	var logs bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldOut) })
	c := NewClient(ClientConfig{BaseURL: srv.URL, OrgID: "org1", Cookie: "c=1"})
	start := time.Now()
	body, err := c.Completion("conv1", &CompletionRequest{Prompt: "hi"})
	if err != nil {
		t.Fatalf("after %v: %v", time.Since(start), err)
	}
	defer body.Close()
	if b, _ := io.ReadAll(body); len(b) == 0 {
		t.Fatal("the answer is read")
	}
	if strings.Contains(logs.String(), "no byte from the stream") || strings.Contains(logs.String(), "retry") {
		t.Fatalf("the wait for a slot was judged a silent stream:\n%s", logs.String())
	}
	if time.Since(start) < time.Second {
		t.Fatalf("the request waited %v, the slot was not taken", time.Since(start))
	}
}
