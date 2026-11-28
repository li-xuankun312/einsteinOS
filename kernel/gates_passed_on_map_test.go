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

// 11/28 rate_in_script and kernel_copied failed at five verifications in a row and each report
// gave them as new: a gate failing at two or more judged verifications in a row is said with how
// many and since when
func TestAGateFailingAtVerificationsInARowIsSaid(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 28, h, m, 0, 0, time.UTC) }
	out := func(gates ...string) UnitVerifyOutcome {
		return UnitVerifyOutcome{Verified: true, Result: VerifyResult{Compile: "pass", Gates: gates}}
	}
	var prev *preVerifyResult
	for i, m := range []int{55, 70, 78} {
		runs := gateRunsFrom(prev, out("kernel_unported", "rate_in_script"), at(5, m))
		prev = &preVerifyResult{out: out("kernel_unported", "rate_in_script"), gateRuns: runs}
		if i == 0 && gatesStillFailing(runs) != "" {
			t.Fatal("said at the first")
		}
	}
	runs := gateRunsFrom(prev, out("rate_in_script", "port_extra"), at(6, 36))
	got := gatesStillFailing(runs)
	want := "\n[GATES THAT FAILED AT EACH OF THE LAST VERIFICATIONS — measured]\nrate_in_script: 4 verifications in a row, the first started at 05:55:00\n"
	if got != want {
		t.Fatalf("%q", got)
	}
	if gateRunsFrom(prev, UnitVerifyOutcome{Verified: true, Result: VerifyResult{Compile: "fail"}}, at(7, 0)) != nil {
		t.Fatal("a compile failure judged gates")
	}
}
