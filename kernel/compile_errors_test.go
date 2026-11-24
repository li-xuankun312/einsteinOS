package kernel

import (
	"strings"
	"testing"
)

// 11/24 04:27:49 the one error of an 844-compile build sat among -Wformat warnings, and only the
// last 3000 bytes of the output were kept: every error line is kept, with the two lines after it
func TestACompilersErrorsAreKeptFromTheWholeOutput(t *testing.T) {
	out := "tests/a.cu:3:5: error: use of undeclared identifier 'x'\n    x();\n    ^\n" +
		strings.Repeat("tests/k32_gpu_kernels/test_kg.cu:28:144: warning: format specifies type 'float'\n", 400) +
		"ld: undefined reference to `kg::gdn_ssm_norm'\nclang: error: linker command failed\n"
	got := compileErrorLines(out, 80)
	for _, w := range []string{"tests/a.cu:3:5: error: use of undeclared identifier 'x'\n    x();\n    ^\n", "undefined reference to `kg::gdn_ssm_norm'", "linker command failed"} {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q in\n%s", w, got)
		}
	}
	if strings.Contains(got, "warning") {
		t.Fatalf("a warning kept: %s", got)
	}
}
