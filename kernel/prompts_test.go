package kernel

import (
	"strings"
	"testing"
)

// A format string with the wrong number of arguments does not fail to compile: it
// prints "%!s(MISSING)" into the prompt a conversation then tries to follow.
func noFormatDamage(t *testing.T, name, prompt string) {
	t.Helper()
	for _, bad := range []string{"%!", "(MISSING)", "(EXTRA", "%[", "%s", "%d"} {
		if strings.Contains(prompt, bad) {
			t.Errorf("%s: format damage %q in the prompt", name, bad)
		}
	}
}

func TestTheoryPromptIsFormattedAndTeachesBatching(t *testing.T) {
	p := formatTheorizeInstruction()
	noFormatDamage(t, "theory", p)
	for _, want := range []string{
		TheoryOutputsDir + "/<CamelCaseName>.lean", // the outputs dir came through as an arg
		"write ALL your .lean files first",
		"ONE command",
		"ls " + TheoryOutputsDir, // the manifest step that lets the kernel find bash-written files
		DoneMarker,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("theory prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "after each .lean file") {
		t.Error("the per-file verification instruction is back")
	}
	if strings.Contains(p, "write a brief summary and stop") {
		t.Error("'stop' without the marker is the contract the kernel did not honour")
	}
}

func TestDevUnitPromptIsFormattedAndAsksForTheMarker(t *testing.T) {
	r := &devRun{pid: 1, repo: "o/r", devRepo: "o/d", branch: "b", task: "t", gpuAll: "0,1", devRepoBlock: "[DEV REPO]\n"}
	plan := newDevPlan("o/r", "s", []DevModule{{Name: "A.lean", Hash: "h", Bytes: 10}}, [][]string{{"A.lean"}}, "p")
	r.plan = plan
	p := r.unitPrompt(plan.Leaves()[0], 0, 1, 1, DevFailure{}, "b")
	noFormatDamage(t, "dev unit", strings.ReplaceAll(p, "%s", "")) // einsteinHelp legitimately shows none; keep the check strict anyway
	if !strings.Contains(p, DoneMarker) || !strings.Contains(p, "einsteinos spawn work") {
		t.Fatal("dev prompt lacks the marker instruction or the command help")
	}
}

func TestEveryConversationPromptAsksForTheMarker(t *testing.T) {
	if !strings.Contains(hwProbePrompt(), DoneMarker) {
		t.Error("hw_probe")
	}
	if !strings.Contains(BuildJudgePrompt("", []JudgeItem{{ID: "1", Question: "q"}}), DoneMarker) {
		t.Error("judge")
	}
	if !strings.Contains(spawnPreamble("p", "work", ""), DoneMarker) {
		t.Error("spawned work")
	}
	if !strings.Contains(buildSurveyPlanPrompt("o", "r", "abc", "t", nil, nil, nil, true), DoneMarker) {
		t.Error("survey+plan")
	}
}
