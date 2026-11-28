package claudeweb

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/kernel"
)

// 11/21 06:26:50 the evaluation's search a07300a2, told about this machine and its registered
// services, tried pr_setup and sync_repo and then built on the machine. A conversation whose
// answer is text or the web's results is told neither; a dev conversation still is.
func TestASearchIsNotToldAboutTheMachineOrItsServices(t *testing.T) {
	kernelInitOnce.Do(kernel.Init)
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/completion") {
			mu.Lock()
			bodies = append(bodies, string(b))
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ev := range []string{
				`event: content_block_start` + "\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta` + "\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"vllm-project/vllm\n[DONE]"}}`,
				`event: message_delta` + "\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
				`event: message_stop` + "\ndata: " + `{"type":"message_stop"}`,
			} {
				w.Write([]byte(ev + "\n\n"))
			}
			return
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	oldWin := apiWin
	apiWin = newAPIWindow(1000, time.Minute)
	defer func() { apiWin = oldWin }()
	oldPrefix := localExecPrefix
	localExecPrefix = "[THIS MACHINE] x\n[REGISTERED SERVICES]\n- pr_setup {}\n"
	defer func() { localExecPrefix = oldPrefix }()

	m := NewModel(NewClient(ClientConfig{BaseURL: srv.URL, OrgID: "org1", Cookie: "c=1"}), "claude-opus-5-5", "medium")
	for role, told := range map[string]bool{"eval_discover": false, "ref_discover": false, "detect_dev": false, "dev_unit_7": true} {
		mu.Lock()
		bodies = nil
		mu.Unlock()
		kernel.SetForkRole(role)
		m.RunConversation(0, "find three repositories")
		mu.Lock()
		first := ""
		if len(bodies) > 0 {
			first = bodies[0]
		}
		mu.Unlock()
		if first == "" {
			t.Fatalf("role %s: no completion was sent", role)
		}
		if got := strings.Contains(first, "REGISTERED SERVICES"); got != told {
			t.Fatalf("role %s: told about the machine and its services = %v, want %v", role, got, told)
		}
	}
}
