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
		"tests/k39_integration/embed_parity.cu": "// e2e_embed_main: strong symbol\nint e2e_embed_main() {\n  return 0;\n}\nint main() { return 1; }\n",
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

// 11/25 12:32:45 embed_parity.cu:133 wrote e2e_embed_main() itself beside the main build.sh's
// -Dmain=e2e_embed_main renamed into it: the line is given, not the comment naming it
func TestANameTheRenameMakesWrittenByHandIsGiven(t *testing.T) {
	ws := embedRedefTree(t)
	got := cmdlineMacroNear(ws, ".einsteinOS/verify/uint1/build.sh", embedRedefOut)
	want := "    tests/k39_integration/embed_parity.cu writes e2e_embed_main itself, the name -Dmain= makes of main:\n      2: int e2e_embed_main() {\n"
	if !strings.Contains(got, want) {
		t.Fatalf("lacks %q:\n%s", want, got)
	}
	if strings.Contains(got, "strong symbol") || strings.Count(got, "writes e2e_embed_main itself") != 1 {
		t.Fatalf("a comment given, or the file twice:\n%s", got)
	}
}

// 11/25 16:47:21 build.sh:26 and :28 were comments naming -Dmain=: a shell comment is no definition
func TestACommentOfTheBuildScriptIsNoDefinition(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/verify/uint1/build.sh": "# embed_parity.cu already defines e2e_embed_main; compiling it with -Dmain=e2e_embed_main\n" +
			"cuobj $T/embed_parity.cu \"$OBJ/e2e_embed.o\" -Dmain=e2e_embed_main\n",
	})
	got := cmdlineMacroNear(ws, ".einsteinOS/verify/uint1/build.sh", embedRedefOut)
	if strings.Contains(got, "build.sh:1:") || !strings.Contains(got, "build.sh:2: cuobj") {
		t.Fatalf("%s", got)
	}
}

// 11/25 16:47:21 -Dmain=e2e_embed_main (build.sh:147) and -De2e_embed_main=e2e_embed_main_
// (build.sh:31) made main e2e_embed_main_: what main becomes in the end is given, step by step
func TestWhatAMacroBecomesThroughAnotherIsGiven(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		".einsteinOS/verify/uint1/build.sh": "case \"$f\" in *embed_parity*) \"$CLANG\" -De2e_embed_main=e2e_embed_main_ \"$@\" -c \"$f\" -o \"$o\" ;; esac\n" +
			"cuobj $T/embed_parity.cu \"$OBJ/e2e_embed.o\" -Dmain=e2e_embed_main\n",
	})
	got := cmdlineMacroNear(ws, ".einsteinOS/verify/uint1/build.sh", embedRedefOut)
	want := "    main becomes e2e_embed_main_ in the end, each -D's name being itself a macro of the scripts: -Dmain=e2e_embed_main (.einsteinOS/verify/uint1/build.sh:2), then -De2e_embed_main=e2e_embed_main_ (.einsteinOS/verify/uint1/build.sh:1)\n"
	if !strings.Contains(got, want) {
		t.Fatalf("lacks %q:\n%s", want, got)
	}
	// one step: nothing said of a chain
	if got := cmdlineMacroNear(embedRedefTree(t), ".einsteinOS/verify/uint1/build.sh", embedRedefOut); strings.Contains(got, "in the end") {
		t.Fatalf("%s", got)
	}
}
