package claudeweb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/kernel"
	"google.golang.org/adk/v2/model"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// 11/16 12:07:39: a conversation ended with do_exit(error) one second after a Continue and
// nothing in the log said what the server had answered. The server's error event is now in
// the record of the exit, the exit's log line names the branch the record goes to, and the
// request in it has no header and no cookie.
func TestAnErrorExitLeavesARecordAndTheLogNamesItsBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/completion") {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Another response is already running\"}}\n\n")
	}))
	defer srv.Close()

	oldWin := apiWin
	apiWin = newAPIWindow(1000, time.Minute)
	oldRepo, oldPub := kernel.DiagRepo(), exitDiagPublish
	kernel.SetDiagRepo("zeng-bin3123/baloon")
	type call struct {
		repo, branch string
		rec          kernel.DiagRecord
	}
	pushed := make(chan call, 1)
	exitDiagPublish = func(repo, branch string, rec kernel.DiagRecord) { pushed <- call{repo, branch, rec} }
	logs := &lockedBuf{}
	oldOut := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		apiWin, exitDiagPublish = oldWin, oldPub
		kernel.SetDiagRepo(oldRepo)
	})

	m := NewModel(NewClient(ClientConfig{BaseURL: srv.URL, OrgID: "org1", Cookie: "sessionKey=SECRET-COOKIE-VALUE-123"}), "claude-opus-5-5", "medium")
	var answered strings.Builder
	m.completionLoop(context.Background(), "conv1", m.newCompletionRequest("hello there"), false,
		func(r *model.LLMResponse, err error) bool {
			if r != nil && r.Content != nil {
				for _, p := range r.Content.Parts {
					answered.WriteString(p.Text)
				}
			}
			return true
		})
	if !strings.Contains(answered.String(), "overloaded_error") {
		t.Fatalf("the exit's text: %q", answered.String())
	}
	m1 := regexp.MustCompile(`do_exit\(error\)[^\n]* diag=(einsteinOS/diag_[0-9a-f]{8})`).FindStringSubmatch(logs.String())
	if m1 == nil {
		t.Fatalf("the exit's log line does not name the branch:\n%s", logs.String())
	}
	select {
	case c := <-pushed:
		if c.repo != "zeng-bin3123/baloon" || c.branch != m1[1] {
			t.Fatalf("pushed %s to %s; the log says %s", c.branch, c.repo, m1[1])
		}
		if !strings.Contains(c.rec.Text, "Another response is already running") || c.rec.Exit != "error" ||
			!strings.Contains(c.rec.Request.Prompt, "hello there") || c.rec.Request.Model != "claude-opus-5-5" ||
			c.rec.Request.Effort != "medium" || c.rec.ConvID != "conv1" {
			t.Fatalf("%+v", c.rec)
		}
		b, _ := json.Marshal(c.rec)
		for _, leak := range []string{"SECRET-COOKIE-VALUE-123", "sessionKey", "Cookie"} {
			if strings.Contains(string(b), leak) {
				t.Fatalf("the record holds %q: %s", leak, b)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no record was pushed")
	}
}

// The server's refusal for the account's concurrency reaches the kernel, which starts no more
// conversations than it took: 11/19 08:17:01 a2983f70's diag record, as the server sent it.
func TestARefusalForConcurrencyBoundsTheConversationsAtOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `event: error
data: {"type":"error","error":{"type":"rate_limit_error","message":"{\"type\":\"exceeded_limit\",\"resetsAt\":null,\"remaining\":null,\"perModelLimit\":null,\"representativeClaim\":\"concurrents\",\"overageInUse\":false,\"windows\":null}","details":{"error_code":"concurrent_limit_reached"}},"request_id":"req_011ChBxE3KGqVmbuMsqqbSXx"}

`)
	}))
	defer srv.Close()
	oldWin, oldPub := apiWin, exitDiagPublish
	apiWin = newAPIWindow(1000, time.Minute)
	exitDiagPublish = func(string, string, kernel.DiagRecord) {}
	kernel.ResetConversationRoom()
	t.Cleanup(func() { apiWin, exitDiagPublish = oldWin, oldPub; kernel.ResetConversationRoom() })

	m := NewModel(NewClient(ClientConfig{BaseURL: srv.URL, OrgID: "org1", Cookie: "c=1"}), "claude-sonnet-4-6", "medium")
	m.completionLoop(context.Background(), "a2983f70-c17b-4b70-af70-ee5ff1331748", m.newCompletionRequest("p"), false,
		func(*model.LLMResponse, error) bool { return true })
	if kernel.ConversationRoom() == 0 {
		t.Fatal("the refusal for concurrency did not bound the conversations at once")
	}
}
