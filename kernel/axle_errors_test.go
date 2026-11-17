package kernel

import (
	"strings"
	"testing"
)

// 11/17 dev_unit_8's fix conversation (08aaed5a) was told one error per verification — the
// first of the file Axle had answered on. Every error of the answer is read.
func TestAxleErrorsReadsEveryErrorOfTheAnswer(t *testing.T) {
	ans := `{"okay":false,"lean_messages":{"errors":[` +
		`"-:28:38-28:43: error: omega could not prove the goal",` +
		`"-:29:24-29:40: error: Tactic ` + "`split_ifs`" + ` failed: no if-then-else conditions to split",` +
		`{"message":"-:41:2-41:8: error: No goals to be solved"}]},"tool_messages":{"errors":[]}}`
	got := AxleErrors("[pid 0 exit=0] " + ans)
	if len(got) != 3 || !strings.Contains(got[0], "omega") || !strings.Contains(got[1], "split_ifs") || !strings.Contains(got[2], "No goals") {
		t.Fatalf("%q", got)
	}
	if _, first, _ := AxleVerdict(ans); !strings.Contains(first, "omega") {
		t.Fatalf("AxleVerdict still names the first: %q", first)
	}
	if got := AxleErrors(`{"okay":false,"lean_messages":{"errors":[]},"tool_messages":{"errors":["timeout"]}}`); len(got) != 1 || got[0] != "timeout" {
		t.Fatalf("tool errors when Lean gave none: %q", got)
	}
	if got := AxleErrors(`{"okay":true,"lean_messages":{"errors":[]}}`); len(got) != 0 {
		t.Fatalf("a passing answer has errors: %q", got)
	}
	if got := AxleErrors("<html>gateway</html>"); got != nil {
		t.Fatalf("not an Axle answer: %q", got)
	}
	long := strings.Repeat("x", 900)
	if got := AxleErrors(`{"okay":false,"lean_messages":{"errors":["` + long + `"]}}`); len(got) != 1 || len(got[0]) > 450 {
		t.Fatalf("an error is cut at 400: %d", len(got[0]))
	}
}
