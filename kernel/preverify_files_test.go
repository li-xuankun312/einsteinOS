package kernel

import (
	"strings"
	"testing"
)

// The verification's report names the files it ran on, so a conversation can see whether what
// it wrote reached the kernel. 11/15 dev_unit_33 was told its test failed on a pattern missing
// from pytools/locate.py and could not see that locate.py was not among the files the kernel
// had; it spent 13 rounds concluding the kernel's checkout was stale.
func TestTheReportNamesTheFilesTheKernelVerified(t *testing.T) {
	r := preVerifyReport(UnitVerifyOutcome{Verified: true,
		Result:  VerifyResult{Compile: "pass", Smoke: "fail", TestExit: 1},
		Status:  map[string]string{"smoke_error": "mutant gguf_py: pattern not found\nTESTS FAILED"},
		Changed: []string{"pytools/mtp_quant.py", "tests/k37_pytools/test_pytools.py"},
		Removed: []string{"pytools/old.py"}})
	for _, want := range []string{"the 3 file(s) this was run on", "pytools/mtp_quant.py", "pytools/old.py (removed)"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in:\n%s", want, r)
		}
	}
	if strings.Contains(r, "pytools/locate.py") {
		t.Error("a file the kernel does not have is named")
	}
	none := preVerifyReport(UnitVerifyOutcome{Verified: true, Result: VerifyResult{Compile: "pass", Smoke: "fail"}})
	if !strings.Contains(none, "you have changed no file the kernel can see") {
		t.Errorf("nothing changed:\n%s", none)
	}
}
