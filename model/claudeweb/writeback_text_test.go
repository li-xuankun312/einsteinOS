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
	if !doneInToolRound("dev_unit_3", ok, 0) || !doneInToolRound("dev_fix_1", ok, 0) {
		t.Fatal("dev unit, every result exit 0: end")
	}
	if doneInToolRound("dev_unit_3", bad, 0) || doneInToolRound("dev_unit_3", axle, 0) || doneInToolRound("dev_unit_3", nil, 0) {
		t.Fatal("a failure, an unseen Axle answer or no local result: the round that reads them is needed")
	}
	for _, role := range []string{"review_conv", "hw_probe", "theory_planner", "theorize_chunk_2"} {
		if doneInToolRound(role, ok, 0) {
			t.Fatalf("%s writes its product as text: it must read the results first", role)
		}
	}
}

// 11/04 unit 1 ran build.sh five times; in its done round the verify scripts are left to the kernel
func TestTheDoneRoundLeavesTheVerifyScriptsToTheKernel(t *testing.T) {
	run := `{"command":"cd /home/claude/baloon && bash .einsteinOS/verify/u89be81de0b/build.sh >/tmp/b.log 2>&1; echo \"build rc=$?\"; tail -2 /tmp/b.log"}`
	if !runsOwnVerifyScript(run) {
		t.Fatal("running the unit's own build.sh")
	}
	write := `{"command":"cat > .einsteinOS/verify/u1/build.sh <<'EOF'\nmake\nEOF\nbash .einsteinOS/verify/u1/build.sh"}`
	if runsOwnVerifyScript(write) {
		t.Fatal("a command that writes the script must run: skipping it would lose the file")
	}
	if !doneInToolRound("dev_unit_1", nil, 2) {
		t.Fatal("only verify scripts in the done round: it ends")
	}
}
