package kernel

import (
	"strings"
	"testing"
)

func TestDoneMarkerEndsOnlyWhenPresentSettledAndTextOnly(t *testing.T) {
	c := &DoneMarkerCond{}
	done := "wrote 9 modules.\n" + DoneMarker
	cases := []struct {
		name string
		e    ToolEvent
		want bool
	}{
		{"marker, settled, text-only", ToolEvent{Text: done, Settled: true}, true},
		{"markdown around the marker", ToolEvent{Text: "ok\n**`" + DoneMarker + "`**", Settled: true}, true},
		{"marker but NOT settled (a response may still be generating)", ToolEvent{Text: done, Settled: false}, false},
		{"marker mid-text, not the last line", ToolEvent{Text: DoneMarker + "\nbut I will go on", Settled: true}, false},
		{"a round that called tools never ends on the marker", ToolEvent{Text: done, Settled: true, HasTools: true}, false},
		{"no marker", ToolEvent{Text: "all done!", Settled: true}, false},
		{"empty", ToolEvent{Settled: true}, false},
	}
	for _, tc := range cases {
		if got := c.Check(tc.e) != nil; got != tc.want {
			t.Errorf("%s: fired=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestWithDoneMarkerKeepsTheOldCounting(t *testing.T) {
	cond := WithDoneMarker(OrConditions(&ConsecutiveTextRounds{Threshold: 3}, &MaxRounds{Limit: 40}))
	// without a marker: the third text-only round still ends it, exactly as before
	for i := 1; i <= 2; i++ {
		if cond.Check(ToolEvent{Round: i, Text: "narration"}) != nil {
			t.Fatalf("fired early at text round %d", i)
		}
	}
	if sig := cond.Check(ToolEvent{Round: 3, Text: "narration"}); sig == nil || !strings.Contains(sig.Reason, "idle=3") {
		t.Fatalf("old counting lost: %v", sig)
	}
	// with the marker: the FIRST text round ends it
	cond.Reset()
	if sig := cond.Check(ToolEvent{Round: 1, Text: "done\n" + DoneMarker, Settled: true}); sig == nil || sig.Reason != "done_marker" {
		t.Fatalf("marker did not end the conversation: %v", sig)
	}
}

func TestEveryRoleCanEndOnTheMarker(t *testing.T) {
	for _, role := range []string{"theorize_chunk_1", "hw_probe", "dev_planner", "review_conv", "dev_unit_2", "spawn_work", "whatever"} {
		c := TerminationForRole(role)
		if c.Check(ToolEvent{Round: 1, Text: "x\n" + DoneMarker, Settled: true}) == nil {
			t.Errorf("role %s ignores the marker (%s)", role, c)
		}
	}
}

func TestStripDoneMarker(t *testing.T) {
	if got := StripDoneMarker("PASS\n" + DoneMarker + "\n"); got != "PASS" {
		t.Fatalf("%q", got)
	}
	if got := StripDoneMarker(DoneMarker); got != "" {
		t.Fatalf("%q", got)
	}
	if got := StripDoneMarker("FAIL: " + DoneMarker + " in the middle\nmore"); !strings.Contains(got, "more") {
		t.Fatalf("a marker that is not the last line must be left alone: %q", got)
	}
	if !strings.Contains(DoneInstruction(), DoneMarker) {
		t.Fatal("the instruction must be built from the marker")
	}
}

func TestNoProgressStopsAConversationThatProducedNothingButOnlyAfterItsRounds(t *testing.T) {
	produced := false
	looks := 0
	c := &NoProgress{Rounds: 10, Produced: func() bool { looks++; return produced }}
	for r := 1; r <= 9; r++ {
		if c.Check(ToolEvent{Round: r, HasTools: true}) != nil {
			t.Fatalf("fired at round %d, before its %d rounds", r, 10)
		}
	}
	if looks != 0 {
		t.Fatalf("the probe (a hash of the mirror) must not run before round 10: %d looks", looks)
	}
	sig := c.Check(ToolEvent{Round: 10, HasTools: true})
	if sig == nil || sig.Reason != "no_progress=10_rounds" {
		t.Fatalf("a conversation that wrote nothing in 10 rounds must stop: %v", sig)
	}
	// several tool events in one round: the probe runs once
	before := looks
	c.Check(ToolEvent{Round: 10})
	c.Check(ToolEvent{Round: 10})
	if looks != before {
		t.Fatalf("probe ran %d more times within one round", looks-before)
	}
	// a conversation that has produced something is never stopped by this
	produced = true
	if c.Check(ToolEvent{Round: 11}) != nil || c.Check(ToolEvent{Round: 25}) != nil {
		t.Fatal("a conversation that produced something must not be stopped")
	}
	// no probe, no opinion
	if (&NoProgress{Rounds: 3}).Check(ToolEvent{Round: 99}) != nil {
		t.Fatal("without a probe it cannot know")
	}
}

func TestADevUnitConversationEndsOnNoProgress(t *testing.T) {
	// the same wiring runUnit builds: the mirror and its base manifest
	mirror, tip := t.TempDir(), t.TempDir()
	mustWrite(tip, "src/a.cu", "a")
	base, err := primeMirror(tip, mirror)
	if err != nil {
		t.Fatal(err)
	}
	produced := func() bool {
		c, rm, err := treeDelta(mirror, base)
		return err != nil || len(c)+len(rm) > 0
	}
	cond := WithDoneMarker(OrConditions(&ConsecutiveTextRounds{Threshold: 4}, &MaxRounds{Limit: 30}, &NoProgress{Rounds: devNoProgressRounds, Produced: produced}))
	// alternating one tool call and one paragraph, as in the log: ConsecutiveTextRounds never reaches 4
	var sig *StopSignal
	for r := 1; r <= 12 && sig == nil; r++ {
		sig = cond.Check(ToolEvent{Round: r, HasTools: r%2 == 1, Text: "paragraph"})
	}
	if sig == nil || !strings.HasPrefix(sig.Reason, "no_progress") {
		t.Fatalf("an alternating silent conversation must stop at round %d on no_progress, got %v", devNoProgressRounds, sig)
	}
	// once a file appears in the mirror, the same conversation runs on
	mustWrite(mirror, "src/impl.cu", "x")
	cond.Reset()
	for r := 1; r <= 29; r++ {
		if s := cond.Check(ToolEvent{Round: r, HasTools: r%2 == 1, Text: "paragraph"}); s != nil {
			t.Fatalf("stopped at round %d although a file was written: %v", r, s)
		}
	}
}
