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

// searchStream serves one completion: some text, then a tool of the given name.
func searchStream(t *testing.T, tool string) (srv *httptest.Server, stops, completions func() int) {
	var mu sync.Mutex
	nStop, nComp := 0, 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/stop_response"):
			nStop++
			w.WriteHeader(200)
		case strings.HasSuffix(r.URL.Path, "/completion"):
			nComp++
			w.Header().Set("Content-Type", "text/event-stream")
			evs := []struct{ name, data string }{
				{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"vllm-project/vllm\n"}}`},
				{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01","name":"` + tool + `"}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"qwen moe tok/s\"}"}}`},
				{"content_block_stop", `{"type":"content_block_stop","index":1}`},
				{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`},
				{"message_stop", `{"type":"message_stop"}`},
			}
			for _, ev := range evs {
				w.Write([]byte("event: " + ev.name + "\ndata: " + ev.data + "\n\n"))
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	oldWin := apiWin
	apiWin = newAPIWindow(1000, time.Minute)
	t.Cleanup(func() { apiWin = oldWin })
	return srv,
		func() int { mu.Lock(); defer mu.Unlock(); return nStop },
		func() int { mu.Lock(); defer mu.Unlock(); return nComp }
}

func runSearchRole(t *testing.T, srv *httptest.Server, role string) string {
	m := NewModel(NewClient(ClientConfig{BaseURL: srv.URL, OrgID: "org1", Cookie: "c=1"}), "claude-opus-5-5", "medium")
	kernel.SetForkRole(role)
	var got strings.Builder
	m.completionLoop(context.Background(), "conv-search-"+role, m.newCompletionRequest("find repositories"), false,
		func(r *model.LLMResponse, err error) bool {
			if r != nil && r.Content != nil {
				for _, p := range r.Content.Parts {
					got.WriteString(p.Text)
				}
			}
			return true
		}, true)
	return got.String()
}

// The evaluation's search is stopped at a tool that is not the web's, and its answer is what
// it wrote before it — 11/21 06:26 a07300a2 ran bash_tool 50 times in its first round and went
// on to write and benchmark a project on this machine.
func TestASearchIsStoppedAtAToolThatIsNotTheWebs(t *testing.T) {
	for _, role := range []string{"eval_discover", "ref_discover"} {
		srv, stops, comps := searchStream(t, "bash_tool")
		got := runSearchRole(t, srv, role)
		if stops() != 1 || comps() != 1 {
			t.Fatalf("%s: stopped at bash_tool and not continued: stops=%d completions=%d", role, stops(), comps())
		}
		if !strings.Contains(got, "vllm-project/vllm") {
			t.Fatalf("%s: its answer is what it wrote before the tool: %q", role, got)
		}
	}
}

// A search's web_search is its work and is not stopped.
func TestASearchsWebSearchIsNotStopped(t *testing.T) {
	srv, stops, _ := searchStream(t, "web_search")
	runSearchRole(t, srv, "eval_discover")
	if stops() != 0 {
		t.Fatalf("web_search stopped the search: stops=%d", stops())
	}
}

func TestSearchRolesAndTools(t *testing.T) {
	for role, want := range map[string]bool{"eval_discover": true, "ref_discover": true, "hw_probe": false, "dev_unit_3": false, "detect_dev": false} {
		if kernel.RoleSearchesOnly(role) != want {
			t.Fatalf("RoleSearchesOnly(%q) = %v", role, !want)
		}
	}
	for name, want := range map[string]bool{"web_search": true, "web_fetch": true, "bash_tool": false, "create_file": false, "str_replace": false} {
		if kernel.SearchTool(name) != want {
			t.Fatalf("SearchTool(%q) = %v", name, !want)
		}
	}
}
