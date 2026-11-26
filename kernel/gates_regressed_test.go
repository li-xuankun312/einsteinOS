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
