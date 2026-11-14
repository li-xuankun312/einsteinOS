package kernel

import "testing"

func TestAPRWithoutARepoHasNoDotInItsTask(t *testing.T) {
	if got := prPrompt(".", "项目在 zeng-bin3123/baloon"); got != "[PR] 项目在 zeng-bin3123/baloon" {
		t.Fatalf("%q", got)
	}
	if got := prPrompt("o/r", "t"); got != "[PR] o/r: t" {
		t.Fatalf("%q", got)
	}
}
