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
