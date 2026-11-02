package claudeweb

import (
	"strings"
	"testing"
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
