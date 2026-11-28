package claudeweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/kernel"
	"google.golang.org/adk/v2/model"
)

// A conversation whose role answers in text is stopped when it starts a tool, and its answer
// is what it wrote before. 11/12 the dev-repository question went on to collect 6319 commits
// and push nine commits to the dev repository's main.
func TestAConversationThatAnswersInTextIsStoppedAtItsFirstTool(t *testing.T) {
	var mu sync.Mutex
	completions, stops := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/stop_response"):
			stops++
			w.WriteHeader(200)
		case strings.HasSuffix(r.URL.Path, "/completion"):
			completions++
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ev := range []struct{ name, data string }{
				{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"dev=zeng-bin3123/baloon\nbranch=main\nnow collecting the commits"}}`},
				{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01","name":"bash_tool"}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"git push\"}"}}`},
				{"content_block_stop", `{"type":"content_block_stop","index":1}`},
				{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`},
				{"message_stop", `{"type":"message_stop"}`},
			} {
				w.Write([]byte("event: " + ev.name + "\ndata: " + ev.data + "\n\n"))
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	oldWin := apiWin
	apiWin = newAPIWindow(1000, time.Minute)
	t.Cleanup(func() { apiWin = oldWin })

	m := NewModel(NewClient(ClientConfig{BaseURL: srv.URL, OrgID: "org1", Cookie: "c=1"}), "claude-opus-5-5", "medium")
	kernel.SetForkRole("detect_dev")
	var got strings.Builder
	m.completionLoop(context.Background(), "conv-detect-1", m.newCompletionRequest("which is dev"), false,
		func(r *model.LLMResponse, err error) bool {
			if r != nil && r.Content != nil {
				for _, p := range r.Content.Parts {
					got.WriteString(p.Text)
				}
			}
			return true
		}, true)

	mu.Lock()
	defer mu.Unlock()
	if stops != 1 || completions != 1 {
		t.Fatalf("stopped at its first tool and not continued: stops=%d completions=%d", stops, completions)
	}
	if !strings.Contains(got.String(), "dev=zeng-bin3123/baloon\nbranch=main") {
		t.Fatalf("its answer is what it wrote before the tool: %q", got.String())
	}
}
