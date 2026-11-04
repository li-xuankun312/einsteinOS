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
