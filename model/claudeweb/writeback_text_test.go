package claudeweb

import (
	"encoding/json"
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
	if !doneInToolRound("dev_unit_3", ok, true) || !doneInToolRound("dev_fix_1", ok, true) {
		t.Fatal("dev unit, every result exit 0: end")
	}
	if doneInToolRound("dev_unit_3", bad, true) || doneInToolRound("dev_unit_3", axle, true) || doneInToolRound("dev_unit_3", nil, true) {
		t.Fatal("a failure, an unseen Axle answer or no local result: the round that reads them is needed")
	}
	for _, role := range []string{"review_conv", "hw_probe", "theory_planner", "theorize_chunk_2"} {
		if doneInToolRound(role, ok, true) {
			t.Fatalf("%s writes its product as text: it must read the results first", role)
		}
	}
}

// 11/05: the fix conversations that skipped their verify scripts never saw the real machine;
// a dev unit ends in its done round only after running both here with exit 0
func TestADevUnitEndsInItsToolRoundOnlyAfterItsVerifyScriptsRanHere(t *testing.T) {
	ok := []ToolResult{{ToolUseID: "a", Content: "[pid 7 exit=0] ALL TESTS PASSED"}}
	if doneInToolRound("dev_unit_1", ok, false) {
		t.Fatal("the scripts did not run here: the round that reads their results is needed")
	}
	if !doneInToolRound("dev_unit_1", ok, true) {
		t.Fatal("both ran here, exit 0: end")
	}
	b, tt := ownVerifyScripts(`{"command":"cd /home/claude/baloon && bash .einsteinOS/verify/u1/build.sh >/tmp/b.log 2>&1 && bash .einsteinOS/verify/u1/test.sh"}`)
	if !b || !tt {
		t.Fatal("both scripts recognised")
	}
}

// 11/04 review_1 and review_2 gave PASS in round 1 after four reads; round 2 restated it
func TestAReviewThatOnlyReadAndGaveItsVerdictEndsInThatRound(t *testing.T) {
	reads := []string{
		`{"command":"cd /home/claude && git clone https://github.com/o/r.git 2>&1 | tail -5; cd r && git checkout -q b"}`,
		`{"command":"cd /home/claude/r && head -40 .einsteinOS/convs/u1.md; grep -rl X --include=*.lean . | head"}`,
		`{"command":"cd /home/claude/r && sed -n 25,75p tests/unit5/test_ple_io.cpp"}`,
	}
	text := "analysis…\nPASS\nEINSTEINOS_DONE"
	if !reviewDoneInToolRound("review_conv", reads, text) {
		t.Fatal("reads only, verdict and marker: end")
	}
	if reviewDoneInToolRound("review_conv", append(reads, `{"command":"bash .einsteinOS/verify/u1/test.sh"}`), text) {
		t.Fatal("it ran something: it needs the real result")
	}
	// 11/05 review 077eea45: "…/build.sh; …" in a read command was taken for running sh
	if !reviewDoneInToolRound("review_conv", append(reads, `{"command":"cd /home/claude/r && cat include/x.hpp; ls .einsteinOS/verify/u1/build.sh tests | head"}`), text) {
		t.Fatal("a path ending in .sh is not a program run")
	}
	if reviewDoneInToolRound("review_conv", reads, "analysis…\nEINSTEINOS_DONE") {
		t.Fatal("no verdict yet")
	}
	if reviewDoneInToolRound("dev_unit_1", reads, text) {
		t.Fatal("reviews only")
	}
}

func TestAReadWhoseOutputIsTheSandboxsIsSummarisedNotResent(t *testing.T) {
	sb, ok := sandboxStdout([]byte(`[{"type":"text","text":"{\"returncode\":0,\"stdout\":\"abc\\n\",\"stderr\":\"\"}"}]`))
	if !ok || sb != "abc\n" {
		t.Fatalf("%q %v", sb, ok)
	}
	in := `{"command":"cd /home/claude/r && cat tests/unit5/test_ple_hash.cpp"}`
	got, ok := sameAsSandbox(in, "[pid 85 exit=0] abc\n", sb)
	if !ok || !strings.Contains(got, "same output as your sandbox (4 bytes") {
		t.Fatalf("%q", got)
	}
	if _, ok := sameAsSandbox(in, "[pid 85 exit=0] abd\n", sb); ok {
		t.Fatal("different output: sent in full")
	}
	if _, ok := sameAsSandbox(`{"command":"bash build.sh"}`, "[pid 85 exit=0] abc\n", sb); ok {
		t.Fatal("not a read: sent in full")
	}
}

// 11/05 02:09:34: a python edit naming $V/build.sh was counted as a build run
func TestOnlyRunningAVerifyScriptCountsAsRunningIt(t *testing.T) {
	ran := map[string][2]bool{
		`cd /home/claude/baloon && bash .einsteinOS/verify/u1/build.sh >/tmp/b.log 2>&1; echo build_exit=$?`:  {true, false},
		`cd x && V=.einsteinOS/verify/u1 && bash $V/test.sh 2>&1 | tail -30`:                                  {false, true},
		`cd x && ./.einsteinOS/verify/u1/build.sh && .einsteinOS/verify/u1/test.sh`:                          {true, true},
		"cd x && V=.einsteinOS/verify/u1 && python3 - <<'EOF'\np=open('$V/build.sh').read()\nEOF":             {false, false},
		`cd x && cat > .einsteinOS/verify/u1/build.sh <<'EOF'` + "\nmake\nEOF":                                {false, false},
		`sed -i 's/a/b/' .einsteinOS/verify/u1/test.sh; cat .einsteinOS/verify/u1/build.sh`:                    {false, false},
	}
	for cmd, want := range ran {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		b, tt := ownVerifyScripts(string(in))
		if b != want[0] || tt != want[1] {
			t.Errorf("%q: build=%v test=%v, want %v", cmd, b, tt, want)
		}
	}
}
