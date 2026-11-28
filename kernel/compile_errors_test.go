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

// 11/25 15:43:38 "no matching function for call to 'e2e_gdn_layer_main'" was given without its
// fourth line, "candidate function not viable: requires 2 arguments": the notes after an error
// go with it, each with its two lines, and the next error is not taken for one
func TestTheNotesAfterAnErrorGoWithIt(t *testing.T) {
	out := "[cuda syntax] src/baloon_e2e_shim.cu\n" +
		"src/baloon_e2e_shim.cu:62:5: error: no matching function for call to 'e2e_gdn_layer_main'\n" +
		"    e2e_gdn_layer_main();\n" +
		"    ^~~~~~~~~~~~~~~~~~\n" +
		"src/baloon_e2e_shim.cu:25:5: note: candidate function not viable: requires 2 arguments, but 0 were provided\n" +
		"int e2e_gdn_layer_main(int, char**);\n" +
		"    ^\n" +
		"src/baloon_e2e_shim.cu:68:5: error: no matching function for call to 'e2e_moe_main'\n" +
		"    e2e_moe_main();\n" +
		"    ^~~~~~~~~~~~\n" +
		"src/baloon_e2e_shim.cu:27:5: note: candidate function not viable: requires 2 arguments, but 0 were provided\n" +
		"int e2e_moe_main(int, char**);\n" +
		"    ^\n" +
		"2 errors generated.\n"
	got := compileErrorLines(out, compileErrorLinesMax)
	if strings.Count(got, "candidate function not viable: requires 2 arguments") != 2 ||
		strings.Count(got, "error: no matching function") != 2 ||
		!strings.Contains(got, "int e2e_moe_main(int, char**);\n") {
		t.Fatalf("%s", got)
	}
	// 12:47:48: the command line's macro note, the fourth line of the redefinition
	if got := compileErrorLines(embedRedefOut, compileErrorLinesMax); !strings.Contains(got, "<command line>:3:14: note: expanded from macro 'main'") ||
		!strings.Contains(got, "embed_parity.cu:133:5: note: previous definition is here") {
		t.Fatalf("%s", got)
	}
}
