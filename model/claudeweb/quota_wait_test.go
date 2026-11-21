package claudeweb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/kernel"
	"google.golang.org/adk/v2/model"
)

// An account's quota refusal names when it resets; the conversation waits until then, and a
// refused attempt is not one of its rounds. 11/11 19:07-20:35: the reset was not read, each
// effort was retried every 3 minutes, and unit 3's two conversations ended at rounds=20 tools=0
// without one answer, judged to have written nothing, which stopped the run.
func TestARefusedAttemptWaitsForTheResetAndIsNotARound(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	reset := time.Now().Add(time.Hour).In(cst).Format("2006-01-02 15:04:05")
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/completion") {
			w.WriteHeader(404)
			return
		}
		mu.Lock()
		n++
		k := n
		mu.Unlock()
		if k <= 3 {
			tier := "medium"
			if k == 2 {
				tier = "high"
			}
			w.WriteHeader(500)
			io.WriteString(w, fmt.Sprintf(`{"error":{"details":{"error_visibility":"user_facing"},"message":"您当前账号的模型 claude-opus-5-5-%s-thinking 使用次数已达到限制: 已使用20，预占中0，合计占用20/20 (周期: 3h)，本次需要1，请%s后重试或购买更高使用量的套餐。"}}`, tier, reset))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	var waits []time.Duration
	oldSleep, oldCeil := sleepWithHeartbeat, kernel.ConvRoundCeiling
	rateLimitSleep = func(d time.Duration) { waits = append(waits, d) }
	kernel.ConvRoundCeiling = 2
	t.Cleanup(func() { rateLimitSleep, kernel.ConvRoundCeiling = oldSleep, oldCeil })

	// a window of its own: the package's requests so far must not throttle this test's
	oldWin := apiWin
	apiWin = newAPIWindow(1000, time.Minute)
	t.Cleanup(func() { apiWin = oldWin })
	m := NewModel(NewClient(ClientConfig{BaseURL: srv.URL, OrgID: "org1", Cookie: "c=1"}), "claude-opus-5-5", "medium")
	m.completionLoop(context.Background(), "conv1", m.newCompletionRequest("hi"), false, func(*model.LLMResponse, error) bool { return true })

	mu.Lock()
	defer mu.Unlock()
	if n < 4 {
		t.Fatalf("the conversation is answered after its refusals: %d request(s), want the 4th, answered", n)
	}
	if len(waits) == 0 {
		t.Fatal("both efforts refused: the conversation waits")
	}
	for _, w := range waits {
		if w < 50*time.Minute {
			t.Fatalf("waited %v: the reset the refusal names (an hour away) was not read", w)
		}
	}
}
