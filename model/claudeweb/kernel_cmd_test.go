package claudeweb

import (
	"strings"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

func TestShadowExecuteAnswersEinsteinosCommandsItself(t *testing.T) {
	// a shadow that would REFUSE to run anything: if the command reached it, the answer would be ""
	m := &Model{Shadow: &ShadowExecutor{Enabled: false}}

	out := m.shadowExecute("aaaaaaaa-0000-0000-0000-000000000001", "bash_tool", `{"command":"einsteinos help"}`)
	if !strings.Contains(out, "einsteinos spawn work") {
		t.Fatalf("einsteinos command was not answered by the kernel: %q", out)
	}
	// a refused request comes back as JSON the child can read, not as a shell error
	out = m.shadowExecute("aaaaaaaa-0000-0000-0000-000000000001", "bash_tool", "{\"command\":\"einsteinos spawn work <<'EOF'\\ndo it\\nEOF\"}")
	if !strings.Contains(out, `"error"`) {
		t.Fatalf("an unknown parent must be refused with a JSON error: %q", out)
	}
	// anything else still goes to the shadow
	if got := m.shadowExecute("c", "bash_tool", `{"command":"ls"}`); got != "" {
		t.Fatalf("ordinary commands must go to the shadow (disabled here → empty), got %q", got)
	}
}

func TestLocalResultHeaderTellsTheChildWhenOutputWasCut(t *testing.T) {
	whole := localResultHeader("short output")
	if !strings.Contains(whole, "ignore sandbox") {
		t.Fatalf("an uncut result keeps its authority: %q", whole)
	}
	cut := localResultHeader(kernel.ShapeOutput(strings.Repeat("x", 50000), 1000))
	if strings.Contains(cut, "ignore sandbox") || !strings.Contains(cut, "cut to fit") {
		t.Fatalf("a cut result must say so, and must not claim to be the whole output: %q", cut)
	}
}

// the answer of a conversation that ends on the done marker is the text of THAT round
func TestTerminalTextKeepsTheAnswerAndDropsOnlyTheMarker(t *testing.T) {
	plan := "unit: A, B\nunit: C\n" + kernel.DoneMarker
	if got := terminalText(plan); got != "unit: A, B\nunit: C" {
		t.Fatalf("%q", got)
	}
	if got := terminalText(kernel.DoneMarker); got != "" {
		t.Fatalf("a round that is only the marker has no text: %q", got)
	}
	if got := terminalText("PASS"); got != "PASS" {
		t.Fatalf("%q", got)
	}
}

// the 2026/11/02 15:23 unit 3: three rounds, one git command each, no file written
func TestGitFromADevConversationIsAnsweredWithTheTruthFromThisMachine(t *testing.T) {
	m := &Model{Shadow: &ShadowExecutor{Enabled: false}}
	dev := "aaaaaaaa-1111-4111-8111-000000000003"
	other := "bbbbbbbb-1111-4111-8111-000000000004"
	kernel.RegisterConv(1, dev, "o/r", "dev_unit_3", 3)
	kernel.RegisterConv(1, other, "o/r", "theorize_chunk_2", 2)

	git := `{"command":"cd /home/claude && git clone https://github.com/zeng-bin3123/baloon.git"}`
	if got := m.shadowExecute(dev, "bash_tool", git); !strings.Contains(got, "git is not used in this phase") {
		t.Fatalf("a dev conversation's git must be answered by the kernel: %q", got)
	}
	// other roles, and other commands: untouched (the shadow is disabled here, so empty)
	if got := m.shadowExecute(other, "bash_tool", git); got != "" {
		t.Fatalf("the theory phase uses git for its own reasons: %q", got)
	}
	if got := m.shadowExecute(dev, "bash_tool", `{"command":"ls /home/claude/baloon"}`); got != "" {
		t.Fatalf("only git is answered this way: %q", got)
	}
	if got := m.shadowExecute(dev, "create_file", `{"path":"/x","file_text":"git clone"}`); got != "" {
		t.Fatalf("a file's content is not a command: %q", got)
	}
}
