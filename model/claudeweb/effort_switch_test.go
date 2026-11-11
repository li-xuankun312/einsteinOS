package claudeweb

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
)

// A rate limit on one effort moves the request to the other effort and changes nothing
// else: thinking_mode stays what claude.ai's client sends for Opus 5.5 ("auto"). 11/11 15:27
// the switch from medium to high also set thinking_mode "extended", which the proxy counted
// as claude-opus-5-5-high-thinking, a model whose quota was spent until 02:00.
func TestAnEffortSwitchLeavesThinkingAsTheClientSendsIt(t *testing.T) {
	var mu sync.Mutex
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/completion") {
			w.WriteHeader(404)
			return
		}
		var rd io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			if z, err := gzip.NewReader(r.Body); err == nil {
				rd = z
			}
		}
		b, _ := io.ReadAll(rd)
		var req map[string]any
		_ = json.Unmarshal(b, &req)
		mu.Lock()
		sent = append(sent, req)
		n := len(sent)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(500)
			io.WriteString(w, `{"error":{"details":{"error_visibility":"user_facing"},"message":"您当前账号的模型 claude-opus-5-5-medium 使用次数已达到限制: 已使用20，预占中0，合计占用20/20 (周期: 3h)，本次需要1，请2026-11-12 00:34:16后重试或购买更高使用量的套餐。"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	// a window of its own: the package's requests so far must not throttle this test's
	oldWin := apiWin
	apiWin = newAPIWindow(1000, time.Minute)
	t.Cleanup(func() { apiWin = oldWin })
	m := NewModel(NewClient(ClientConfig{BaseURL: srv.URL, OrgID: "org1", Cookie: "c=1"}), "claude-opus-5-5", "medium")
	req := m.newCompletionRequest("hi")
	m.completionLoop(context.Background(), "conv1", req, false, func(*model.LLMResponse, error) bool { return true })

	mu.Lock()
	defer mu.Unlock()
	if len(sent) < 2 {
		t.Fatalf("the request is sent again at the other effort: %d request(s)", len(sent))
	}
	if sent[0]["effort"] != "medium" || sent[1]["effort"] != "high" {
		t.Fatalf("efforts sent: %v then %v", sent[0]["effort"], sent[1]["effort"])
	}
	for i, r := range sent {
		if r["thinking_mode"] != "auto" {
			t.Fatalf("request %d thinking_mode = %v, want auto (as the claude.ai client sends it)", i, r["thinking_mode"])
		}
	}
}
