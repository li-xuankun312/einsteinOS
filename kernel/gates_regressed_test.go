package kernel

import (
	"strings"
	"testing"
	"time"
)

// 11/26 09:41:43 kernel_unported and port_incomplete passed; at 09:41:51 the conversation removed
// 156 rows, the next verification failed kernel_unported, and nothing said which lines had gone
func TestAGateThatPassedAndFailsNowIsGivenTheLinesTheMapLost(t *testing.T) {
	prev := &preVerifyResult{
		out:     UnitVerifyOutcome{Verified: true, Result: VerifyResult{Gates: []string{"port_ref_file_unknown"}}},
		portMap: "a\trow\tsrc/x.cu\tk\nb\trow\tsrc/y.cu\tk\n",
		at:      time.Date(2026, 11, 26, 9, 34, 37, 0, time.UTC),
	}
	now := UnitVerifyOutcome{Verified: true, Result: VerifyResult{Gates: []string{"kernel_unported"}}}
	got := gatesRegressed(prev, now, "b\trow\tsrc/y.cu\tk\n", ".einsteinOS/port/u.tsv")
	for _, w := range []string{"kernel_unported passed at the verification that started at 09:34:37", "held then 1 line(s)", "a\trow\tsrc/x.cu\tk"} {
		if !strings.Contains(got, w) {
			t.Fatalf("lacks %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "b\trow") {
		t.Fatalf("a line the map still holds is given:\n%s", got)
	}
	// no gate newly failing, or no line lost: nothing
	if s := gatesRegressed(prev, UnitVerifyOutcome{Verified: true, Result: VerifyResult{Gates: []string{"port_ref_file_unknown"}}}, "", "m"); s != "" {
		t.Fatalf("no gate went from pass to fail: %q", s)
	}
	if s := gatesRegressed(prev, now, prev.portMap, "m"); s != "" {
		t.Fatalf("the map lost nothing: %q", s)
	}
	if s := gatesRegressed(nil, now, "", "m"); s != "" {
		t.Fatalf("no last verification: %q", s)
	}
}

// 11/26 09:41:43 kernel_shadowed_in_test was sent for a file the conversation had rewritten at
// 09:41:31, the verification having taken the mirror at 09:34:37: the report says when it took
// the mirror and what changed it after
func TestTheReportSaysWhatChangedTheMirrorAfterItWasTaken(t *testing.T) {
	start := time.Now()
	mirrorUnitsMu.Lock()
	mirrorUnits["usince"] = &mirrorUnit{unit: "usince", part: map[string]string{"6c315f57aaaa": ""}, writes: []mirrorWrite{
		{File: "old.cu", Conv: "6c315f57aaaa", At: start.Add(-time.Minute)},
		{File: "tests/k39_integration/ix_router_top10_wave.cu", Conv: "6c315f57aaaa", At: start.Add(time.Second)},
	}}
	mirrorUnitsMu.Unlock()
	t.Cleanup(func() { mirrorUnitsMu.Lock(); delete(mirrorUnits, "usince"); mirrorUnitsMu.Unlock() })
	since := mirrorWritesSince("usince", start)
	if len(since) != 1 || !strings.HasSuffix(since[0], "tests/k39_integration/ix_router_top10_wave.cu") {
		t.Fatalf("got %v", since)
	}
	got := preVerifySince(start, since)
	if !strings.Contains(got, "judged the mirror as it was at "+start.Format("15:04:05")) || !strings.Contains(got, "changed it 1 time(s)") {
		t.Fatalf("got %q", got)
	}
	if preVerifySince(start, nil) != "" {
		t.Fatal("nothing changed and something is said")
	}
}

// 11/26 15:16:22 port_incomplete failed on 147 of 168 and at 15:55:28 on all 168, every row of the
// reference's definitions taken out of the map, and nothing said which rows had gone
func TestPortIncompleteFailingOnMoreIsGivenTheRowsThatWent(t *testing.T) {
	prev := &preVerifyResult{
		out:     UnitVerifyOutcome{Verified: true, Result: VerifyResult{Gates: []string{"port_incomplete"}, GateText: "port_incomplete: 147 of the 168 definitions of the unit's reference files have no counterpart:\n  …"}},
		portMap: "src/kernels/bf16_gemv_parity.cpp\tcheck\tsrc/kernels/bf16_gemv_parity.cpp\tcheck\nsrc/x.cu\tk\tsrc/k.cu\tk\n",
		at:      time.Date(2026, 11, 26, 15, 9, 12, 0, time.UTC),
	}
	now := UnitVerifyOutcome{Verified: true, Result: VerifyResult{Gates: []string{"port_incomplete"}, GateText: "port_incomplete: 168 of the 168 definitions of the unit's reference files have no counterpart:\n  …"}}
	syms := []RefSymbol{{File: "src/kernels/bf16_gemv_parity.cpp", Name: "check"}}
	got := portIncompleteGrew(prev, now, "", ".einsteinOS/port/u.tsv", syms)
	for _, w := range []string{"147 of the 168 definitions had no counterpart at the verification that started at 15:09:12, 168 now", "held then 1 line(s)", "src/kernels/bf16_gemv_parity.cpp\tcheck\tsrc/kernels/bf16_gemv_parity.cpp\tcheck"} {
		if !strings.Contains(got, w) {
			t.Fatalf("lacks %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "src/k.cu") {
		t.Fatalf("a row of no reference definition is given:\n%s", got)
	}
	now.Result.GateText = "port_incomplete: 140 of the 168 definitions of the unit's reference files have no counterpart:"
	if s := portIncompleteGrew(prev, now, "", "m", syms); s != "" {
		t.Fatalf("it did not grow: %q", s)
	}
}
