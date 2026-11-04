package claudeweb

import (
	"strings"
	"testing"
)

func TestAWritebackWithNoResultsIsJustContinue(t *testing.T) {
	if got := writebackText(nil); got != "Continue." {
		t.Fatalf("%q", got)
	}
	got := writebackText([]ToolResult{{ToolUseID: "toolu_1", Content: "exit=0"}})
	if !strings.Contains(got, "use these") || !strings.Contains(got, "[toolu_1] exit=0") {
		t.Fatalf("%q", got)
	}
}

func TestADevUnitMayEndOnADoneMarkerInItsToolRoundOnlyWhenEveryResultExitedZero(t *testing.T) {
	ok := []ToolResult{{ToolUseID: "a", Content: "[LOCAL MACHINE RESULT]\n[pid 7 exit=0] ALL TESTS PASSED"}}
	bad := []ToolResult{{ToolUseID: "a", Content: "[pid 7 exit=0] x"}, {ToolUseID: "b", Content: "[pid 8 exit=1] error"}}
	axle := []ToolResult{{ToolUseID: "a", Content: `{"okay": false}`}}
	if !doneInToolRound("dev_unit_3", ok) || !doneInToolRound("dev_fix_1", ok) {
		t.Fatal("dev unit, every result exit 0: end")
	}
	if doneInToolRound("dev_unit_3", bad) || doneInToolRound("dev_unit_3", axle) || doneInToolRound("dev_unit_3", nil) {
		t.Fatal("a failure, an unseen Axle answer or no local result: the round that reads them is needed")
	}
	for _, role := range []string{"review_conv", "hw_probe", "theory_planner", "theorize_chunk_2"} {
		if doneInToolRound(role, ok) {
			t.Fatalf("%s writes its product as text: it must read the results first", role)
		}
	}
}
