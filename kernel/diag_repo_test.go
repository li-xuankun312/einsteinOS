package kernel

import "testing"

func TestDiagRepoIsWhatWasSet(t *testing.T) {
	old := DiagRepo()
	defer SetDiagRepo(old)
	SetDiagRepo("zeng-bin3123/baloon")
	if DiagRepo() != "zeng-bin3123/baloon" {
		t.Fatal(DiagRepo())
	}
}
