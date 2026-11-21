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
