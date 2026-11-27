package kernel

import (
	"testing"
	"time"
)

// 11/27 16:08:02 four gates passed for uintegration01529aaa82b9 and the tree differed from the
// one judged before it in its port map alone, which the log did not say
func TestGatesPassingWithOnlyThePortMapChangedAreSaidToBe(t *testing.T) {
	m := ".einsteinOS/port/uint.tsv"
	prev := &preVerifyResult{at: time.Date(2026, 11, 27, 15, 34, 20, 0, time.UTC), portMap: "a\tb\tc\td\nx\ty\tz\tw\n",
		out:   UnitVerifyOutcome{Verified: true, Result: VerifyResult{Compile: "pass", Gates: []string{"kernel_unported", "port_extra"}}},
		files: map[string]string{m: "h1", "src/ix_gdn.cu": "h2"}}
	now := UnitVerifyOutcome{Verified: true, Result: VerifyResult{Compile: "pass", Gates: []string{"lean_unproved"}}}
	got := gatesPassedOnMap(prev, now, map[string]string{m: "h9", "src/ix_gdn.cu": "h2"}, "a\tb\tc\td\nn\to\tp\tq\nr\ts\tt\tu\n", m)
	want := "kernel_unported, port_extra failed at the verification that started at 15:34:20 and pass now, and of the unit's files only .einsteinOS/port/uint.tsv differs between the two: 2 line(s) in, 1 out"
	if got != want {
		t.Fatalf("%q", got)
	}
	if got := gatesPassedOnMap(prev, now, map[string]string{m: "h9", "src/ix_gdn.cu": "h3"}, "", m); got != "" {
		t.Fatalf("a source changed too: %q", got)
	}
	if got := gatesPassedOnMap(prev, now, map[string]string{m: "h1", "src/ix_gdn.cu": "h2"}, "", m); got != "" {
		t.Fatalf("nothing changed: %q", got)
	}
}
