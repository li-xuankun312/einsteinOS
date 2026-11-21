package claudeweb

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type watchLogBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *watchLogBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *watchLogBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// A completion the server answers with a 4xx has no stream, and its silence clock stops when it
// returns: 11/21 05:49:17 "no byte from the stream for 5m0s — cancelling it" was logged while
// the only stream, fe0b9e5d's round, went on for seven more minutes and ended with 26 tools.
func TestACompletionRefusedWithA4xxLeavesNoSilenceClockRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"conversation not found"}}`))
	}))
	defer srv.Close()
	oldWin := apiWin
	apiWin = newAPIWindow(1000, time.Minute)
	defer func() { apiWin = oldWin }()
	oldIdle := streamIdleMax
	streamIdleMax = 30 * time.Millisecond
	defer func() { streamIdleMax = oldIdle }()
	var logs watchLogBuf
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	c := NewClient(ClientConfig{BaseURL: srv.URL, OrgID: "org1", Cookie: "c=1"})
	if _, err := c.Completion("conv-404", &CompletionRequest{Prompt: "x"}); err == nil {
		t.Fatal("a 404 was not an error")
	}
	<-time.After(10 * streamIdleMax)
	if strings.Contains(logs.String(), "no byte from the stream") {
		t.Fatalf("a request refused with a 404 left its silence clock running: %s", logs.String())
	}
}
