package claudeweb

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeConv scripts what the server shows at each poll. Time is virtual.
type fakeConv struct {
	now      time.Time
	polls    int
	download func(poll int) ([]RecoveredTool, error)
	status   func(poll int) (pending, isErr bool, err error)
	statusN  int
}

func (f *fakeConv) DownloadLastTools(string) ([]RecoveredTool, error) {
	f.polls++
	return f.download(f.polls)
}
func (f *fakeConv) CompletionStatus(string) (bool, bool, error) {
	f.statusN++
	if f.status == nil {
		return false, false, nil
	}
	return f.status(f.statusN)
}

func (f *fakeConv) cfg(window time.Duration) recoverCfg {
	return recoverCfg{
		Interval: 10 * time.Second, Window: window, Grace: 60 * time.Second,
		Now:   func() time.Time { return f.now },
		Sleep: func(d time.Duration) { f.now = f.now.Add(d) },
		Logf:  func(string, ...any) {},
	}
}

func tools(ids ...string) []RecoveredTool {
	var out []RecoveredTool
	for _, id := range ids {
		out = append(out, RecoveredTool{ID: id, Name: "bash_tool", Input: `{"command":"echo ` + id + `"}`})
	}
	return out
}

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// The dev_unit#1 bug: after an SSE break the last assistant message in the
// download was the PREVIOUS round's (50 tools). It must not be accepted; the
// wait continues until this round's ids show up.
func TestWait_IgnoresPreviousRoundsMessage(t *testing.T) {
	f := &fakeConv{now: t0}
	f.download = func(p int) ([]RecoveredTool, error) {
		if p < 8 {
			return tools("old1", "old2", "old3"), nil // previous round, already delivered
		}
		return tools("new1", "new2"), nil
	}
	got, err := waitForRoundTools(f, "c", []string{"new1", "new2"}, f.cfg(30*time.Minute))
	if err != nil || len(got) != 2 || got[0].ID != "new1" {
		t.Fatalf("must return this round's message, got %v err=%v", got, err)
	}
	if f.polls != 8 {
		t.Fatalf("previous round's message must not end the wait: polls=%d", f.polls)
	}
}

// Chunk 1/3/4: "no assistant message found" for as long as the turn is still
// generating. The old code gave up after 3 tries (~45s). Here the message only
// appears 16 minutes in (chunk 4's real timing) and must still be recovered.
func TestWait_SurvivesSixteenMinutesOfNothing(t *testing.T) {
	f := &fakeConv{now: t0}
	appear := t0.Add(16 * time.Minute)
	f.download = func(int) ([]RecoveredTool, error) {
		if f.now.Before(appear) {
			return nil, errors.New("no assistant message found")
		}
		return tools("a", "b", "c"), nil
	}
	got, err := waitForRoundTools(f, "c", []string{"a", "b", "c"}, f.cfg(30*time.Minute))
	if err != nil || len(got) != 3 {
		t.Fatalf("should recover after 16 min: %v %v", got, err)
	}
	if el := f.now.Sub(t0); el < 16*time.Minute || el > 17*time.Minute {
		t.Fatalf("recovered at unexpected virtual time %s", el)
	}
}

// 985237ce's lesson, which cc5390cd forgot: is_pending=false at the first probe
// does not mean the generation is over.
func TestWait_IdleStatusDoesNotEndTheWait(t *testing.T) {
	f := &fakeConv{now: t0}
	f.status = func(int) (bool, bool, error) { return false, false, nil } // "idle" from poll 1
	f.download = func(p int) ([]RecoveredTool, error) {
		if p < 40 {
			return nil, errors.New("no assistant message found")
		}
		return tools("x"), nil
	}
	got, err := waitForRoundTools(f, "c", []string{"x"}, f.cfg(30*time.Minute))
	if err != nil || len(got) != 1 {
		t.Fatalf("idle status must not stop polling: %v %v", got, err)
	}
	if f.polls != 40 {
		t.Fatalf("expected 40 polls, got %d", f.polls)
	}
}

func TestWait_GivesUpAtTheWindowAndSaysSo(t *testing.T) {
	f := &fakeConv{now: t0}
	f.download = func(int) ([]RecoveredTool, error) { return nil, errors.New("no assistant message found") }
	_, err := waitForRoundTools(f, "c", []string{"x"}, f.cfg(5*time.Minute))
	if !errors.Is(err, errRecoverWindow) {
		t.Fatalf("want window error, got %v", err)
	}
	if f.polls < 30 || f.polls > 32 {
		t.Fatalf("5 min at 10s interval is ~31 polls, got %d", f.polls)
	}
}

func TestWait_ServerErrorEndsTheWaitEarly(t *testing.T) {
	f := &fakeConv{now: t0}
	f.status = func(n int) (bool, bool, error) { return false, n >= 3, nil }
	f.download = func(int) ([]RecoveredTool, error) { return nil, errors.New("no assistant message found") }
	_, err := waitForRoundTools(f, "c", []string{"x"}, f.cfg(30*time.Minute))
	if !errors.Is(err, errRecoverServer) || f.polls != 3 {
		t.Fatalf("want early server error after 3 polls, got %v after %d", err, f.polls)
	}
}

func TestWait_PartialMatchGetsGraceThenIsAccepted(t *testing.T) {
	f := &fakeConv{now: t0}
	f.download = func(int) ([]RecoveredTool, error) { return tools("a", "b"), nil } // "c" never appears
	got, err := waitForRoundTools(f, "c", []string{"a", "b", "c"}, f.cfg(30*time.Minute))
	if err != nil || len(got) != 2 {
		t.Fatalf("partial message should be accepted after grace: %v %v", got, err)
	}
	if el := f.now.Sub(t0); el < 60*time.Second || el > 80*time.Second {
		t.Fatalf("grace should be ~60s, waited %s", el)
	}
}

func TestWait_PartialMatchCompletesInsideGrace(t *testing.T) {
	f := &fakeConv{now: t0}
	f.download = func(p int) ([]RecoveredTool, error) {
		if p < 3 {
			return tools("a"), nil
		}
		return tools("a", "b"), nil
	}
	got, _ := waitForRoundTools(f, "c", []string{"a", "b"}, f.cfg(30*time.Minute))
	if len(got) != 2 || f.polls != 3 {
		t.Fatalf("should return as soon as all ids are present: polls=%d got=%v", f.polls, got)
	}
}

func TestWait_NoIdsIsAnError(t *testing.T) {
	f := &fakeConv{now: t0}
	f.download = func(int) ([]RecoveredTool, error) { return tools("a"), nil }
	if _, err := waitForRoundTools(f, "c", nil, f.cfg(time.Minute)); !errors.Is(err, errRecoverNoIDs) {
		t.Fatalf("want errRecoverNoIDs, got %v", err)
	}
}

// ---- toolLedger / recoveredResults ----

func TestRecoveredResults_NoReplayNoLossNoLocalGuessing(t *testing.T) {
	l := newToolLedger()
	// earlier round: executed AND delivered — must never be sent again
	l.record("old1", "old-out")
	l.markDelivered("old1")
	// this round: executed in the main loop before the SSE branch, not yet delivered
	l.record("cached1", "cached-out")
	// this round: shadow skipped it (git/curl/lean): cached as "" — the remote's own result stands
	l.record("skipped1", "")

	execCalls := map[string]int{}
	exec := func(name, input string) string {
		execCalls[input]++
		return "ran:" + input
	}
	rt := []RecoveredTool{
		{ID: "old1", Name: "bash_tool", Input: "i-old1"},
		{ID: "cached1", Name: "bash_tool", Input: "i-cached1"},
		{ID: "skipped1", Name: "bash_tool", Input: "i-skipped1"},
		{ID: "fresh1", Name: "bash_tool", Input: "i-fresh1"}, // generated after we lost the stream
	}
	got := recoveredResults(l, rt, exec)

	ids := []string{}
	for _, r := range got {
		ids = append(ids, r.ToolUseID)
	}
	if strings.Join(ids, ",") != "cached1,fresh1" {
		t.Fatalf("want cached1,fresh1 got %v", ids)
	}
	if got[0].Content != "cached-out" {
		t.Fatalf("cached result must be reused verbatim, got %q", got[0].Content)
	}
	if len(execCalls) != 1 || execCalls["i-fresh1"] != 1 {
		t.Fatalf("only the never-seen tool may be executed, once: %v", execCalls)
	}
	// calling again must not re-run anything either
	recoveredResults(l, rt, exec)
	if execCalls["i-fresh1"] != 1 {
		t.Fatalf("second pass replayed a tool")
	}
}

func TestLedger_EmptyIDsAreIgnored(t *testing.T) {
	l := newToolLedger()
	l.record("", "x")
	l.markDelivered("")
	if _, ok := l.cached(""); ok || l.wasDelivered("") {
		t.Fatalf("empty id must never be tracked")
	}
}
