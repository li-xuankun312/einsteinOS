package kernel

import (
	"strings"
	"testing"
)

// 11/25 12:47:48 embed_parity.cu:147 "redefinition of 'e2e_embed_main'", the note saying the
// macro main came from the command line: build.sh's lines compiling that file with -Dmain= are
// given, not the -Dmain= of every other test
const embedRedefOut = "[cuda] tests/k39_integration/embed_parity.cu\n" +
	"tests/k39_integration/embed_parity.cu:147:5: error: redefinition of 'e2e_embed_main'\n" +
	"int main() {\n" +
	"    ^\n" +
	"<command line>:3:14: note: expanded from macro 'main'\n" +
	"#define main e2e_embed_main\n" +
	"             ^\n" +
	"tests/k39_integration/embed_parity.cu:133:5: note: previous definition is here\n" +
	"int e2e_embed_main() {\n" +
	"    ^\n" +
	"1 error generated when compiling for ivcore10.\n"

func embedRedefTree(t *testing.T) string {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/verify/uint1/build.sh": "#!/bin/bash\nT=tests/k39_integration\n" +
			"cuobj tests/test_shared_expert.cu \"$OBJ/e2e_se.o\" -Dmain=e2e_se_main\n" +
			"cuobj $T/embed_parity.cu \"$OBJ/e2e_embed.o\" -Dmain=e2e_embed_main\n" +
			"cuobj_pic $T/embed_parity.cu \"$OBJ/pic_e2e_embed.o\" -Dmain=e2e_embed_main\n" +
			"cuobj $T/x.cu \"$OBJ/x.o\" -Dmainline=1\n",
		"tests/k39_integration/embed_parity.cu": "int e2e_embed_main() {\n  return 0;\n}\nint main() { return 1; }\n",
	})
	return ws
}

func TestTheBuildLinesDefiningACommandLineMacroAreGiven(t *testing.T) {
	ws := embedRedefTree(t)
	got := cmdlineMacroNear(ws, ".einsteinOS/verify/uint1/build.sh", embedRedefOut)
	for _, want := range []string{
		"  main (the note after the error in tests/k39_integration/embed_parity.cu):\n",
		"    .einsteinOS/verify/uint1/build.sh:4: cuobj $T/embed_parity.cu \"$OBJ/e2e_embed.o\" -Dmain=e2e_embed_main\n",
		"    .einsteinOS/verify/uint1/build.sh:5: cuobj_pic $T/embed_parity.cu \"$OBJ/pic_e2e_embed.o\" -Dmain=e2e_embed_main\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "e2e_se_main") || strings.Contains(got, "-Dmainline") {
		t.Fatalf("a line not compiling the file, or another macro, given:\n%s", got)
	}
	if cmdlineMacroNear(ws, ".einsteinOS/verify/uint1/build.sh", "a.cu:3:1: error: x\n") != "" {
		t.Fatal("no command-line macro, something said")
	}
	if got := cmdlineMacroNear(t.TempDir(), "", embedRedefOut); !strings.Contains(got, "no build script of the workspace has -Dmain") {
		t.Fatalf("a macro no script defines, not said:\n%s", got)
	}
}
