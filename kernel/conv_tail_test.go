package kernel

import (
	"errors"
	"strings"
	"testing"
)

// Shapes follow the web conversation JSON in the 1019_sci exports: 1.md ends in
// a human message with no reply (exported while the reply was still generating);
// 3.md, ~100s later, ends in a finished assistant message.
const (
	tailDangling = `{"chat_messages":[{"sender":"human","index":0},{"sender":"assistant","stop_reason":"end_turn","index":1},{"sender":"human","index":2}]}`
	tailSettled  = `{"chat_messages":[{"sender":"human","index":0},{"sender":"assistant","stop_reason":"end_turn","index":1}]}`
	tailCancel   = `{"chat_messages":[{"sender":"human"},{"sender":"assistant","stop_reason":"user_canceled"}]}`
	tailNoStop   = `{"chat_messages":[{"sender":"human"},{"sender":"assistant"}]}`
	tailEmpty    = `{"chat_messages":[]}`
)

func TestConvTail_States(t *testing.T) {
	cases := []struct {
		name, js                       string
		settled, working, neverStarted bool
	}{
		{"dangling human = turn in flight", tailDangling, false, true, false},
		{"finished assistant = settled", tailSettled, true, false, false},
		{"cancelled assistant is finished", tailCancel, true, false, false},
		{"assistant without stop_reason is not proven finished", tailNoStop, false, true, false},
		{"no messages = never started", tailEmpty, false, false, true},
	}
	for _, c := range cases {
		tl, err := ConvTailFromJSON([]byte(c.js))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if tl.Settled() != c.settled || tl.Working() != c.working || tl.NeverStarted() != c.neverStarted {
			t.Errorf("%s: settled=%v working=%v never=%v, want %v/%v/%v", c.name,
				tl.Settled(), tl.Working(), tl.NeverStarted(), c.settled, c.working, c.neverStarted)
		}
	}
	if _, err := ConvTailFromJSON([]byte("<html>")); err == nil {
		t.Fatal("garbage must be an error, not an empty conversation")
	}
}

func withTail(js string, err error, f func()) {
	SetConvTailFn(func(string) ([]byte, error) { return []byte(js), err })
	defer SetConvTailFn(nil)
	f()
}

func TestRemoteBusy_IsReadOnlyAndHonest(t *testing.T) {
	withTail(tailDangling, nil, func() {
		if busy, err := remoteBusy("c"); !busy || err != nil {
			t.Fatalf("dangling human must read as busy: %v %v", busy, err)
		}
	})
	withTail(tailSettled, nil, func() {
		if busy, _ := remoteBusy("c"); busy {
			t.Fatal("settled must not be busy")
		}
	})
	// account/rate-limit failures store nothing: waiting on them would stall for the cap
	withTail(tailEmpty, nil, func() {
		if busy, _ := remoteBusy("c"); busy {
			t.Fatal("a conversation with no messages never started; it is not busy")
		}
	})
	withTail("", errors.New("network"), func() {
		if busy, err := remoteBusy("c"); busy || err == nil {
			t.Fatalf("errors are inconclusive, not busy: %v %v", busy, err)
		}
	})
	SetConvTailFn(nil)
	if _, err := remoteBusy("c"); err == nil {
		t.Fatal("no hook configured must be an error")
	}
}

func TestContinueAllowed_OnlyWhenSettled(t *testing.T) {
	for js, want := range map[string]bool{tailSettled: true, tailCancel: true, tailDangling: false, tailNoStop: false, tailEmpty: false} {
		withTail(js, nil, func() {
			if got := continueAllowed("c"); got != want {
				t.Errorf("%s: continueAllowed=%v want %v", js, got, want)
			}
		})
	}
	withTail("", errors.New("boom"), func() {
		if continueAllowed("c") {
			t.Fatal("cannot tell => must not send")
		}
	})
}

func TestToolCallsCounter(t *testing.T) {
	c := NewToolSignalCollector()
	if c.ToolCalls() != 0 {
		t.Fatal("fresh collector has no calls")
	}
	c.Observe("bash", `{"command":"ls"}`, "ok", 1)
	c.Observe("view", `{"path":"/x"}`, "ok", 1)
	if c.ToolCalls() != 2 {
		t.Fatalf("want 2, got %d", c.ToolCalls())
	}
}

// ---- chunk_fill_missing: the only place theory chunks are sent a Continue ----

func setHooks(tailJS string, tailErr error, cont func(prompt string) string, download func() string) func() {
	SetConvTailFn(func(string) ([]byte, error) { return []byte(tailJS), tailErr })
	SetContinueConvFn(func(_, prompt string) (string, string) { return cont(prompt), "" })
	SetDownloadConvFn(func(string) (string, error) { return download(), nil })
	return func() { SetConvTailFn(nil); SetContinueConvFn(nil); SetDownloadConvFn(nil) }
}

func TestFillMissing_NeverSendsIntoAnUnsettledConversation(t *testing.T) {
	sent := 0
	defer setHooks(tailDangling, nil, func(string) string { sent++; return "x" }, func() string { return "" })()
	files := []string{"src/expert_cache.cpp", "src/long_module_name.cpp"}
	res, cov := chunk_fill_missing("c", files, "covers expert_cache.cpp only", "t")
	if sent != 0 {
		t.Fatalf("sent %d Continue(s) into a conversation whose last turn is unpersisted", sent)
	}
	if cov.CoveredCount() != 1 || res != "covers expert_cache.cpp only" {
		t.Fatalf("result must be untouched: %q %+v", res, cov)
	}
	// and when we cannot tell
	defer setHooks("", errors.New("net"), func(string) string { sent++; return "x" }, func() string { return "" })()
	chunk_fill_missing("c", files, "covers expert_cache.cpp only", "t")
	if sent != 0 {
		t.Fatalf("sent %d Continue(s) while unable to read the tail", sent)
	}
}

func TestFillMissing_AsksOnlyForMissingFilesAndStopsWhenSatisfied(t *testing.T) {
	var prompts []string
	grown := "covers expert_cache.cpp and also long_module_name.cpp"
	defer setHooks(tailSettled, nil, func(p string) string { prompts = append(prompts, p); return "ok" }, func() string { return grown })()
	files := []string{"src/expert_cache.cpp", "src/long_module_name.cpp"}
	res, cov := chunk_fill_missing("c", files, "covers expert_cache.cpp only", "t")
	if len(prompts) != 1 {
		t.Fatalf("want exactly one Continue, got %d", len(prompts))
	}
	if !strings.Contains(prompts[0], "src/long_module_name.cpp") || strings.Contains(prompts[0], "expert_cache") {
		t.Fatalf("prompt must list only the missing file: %q", prompts[0])
	}
	if cov.CoveredCount() != 2 || res != grown {
		t.Fatalf("coverage must be recomputed after the Continue: %+v", cov)
	}
}

func TestFillMissing_StopsWhenContinueMakesNoProgress(t *testing.T) {
	sent := 0
	defer setHooks(tailSettled, nil, func(string) string { sent++; return "ok" }, func() string { return "still only expert_cache.cpp, plus filler text" })()
	files := []string{"src/expert_cache.cpp", "src/long_module_name.cpp"}
	chunk_fill_missing("c", files, "covers expert_cache.cpp only", "t")
	if sent != 1 {
		t.Fatalf("no progress after the first Continue must stop the loop, sent %d", sent)
	}
}

func TestFillMissing_ZeroCoverageIsNotRepairedByContinue(t *testing.T) {
	sent := 0
	defer setHooks(tailSettled, nil, func(string) string { sent++; return "x" }, func() string { return "" })()
	chunk_fill_missing("c", []string{"src/expert_cache.cpp"}, chunk1Junk, "t")
	if sent != 0 {
		t.Fatalf("a chunk that covers nothing needs a fresh conversation, not a Continue (sent %d)", sent)
	}
}
