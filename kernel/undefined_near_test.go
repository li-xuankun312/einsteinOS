package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 11/25 04:34:25 "undefined reference to `e2e_embed_main()'": embed_parity.cu, compiled with
// -Dmain=e2e_embed_main, defined e2e_embed_main(int, char**) — what the objects define under
// that name is given, with the object
func TestWhatTheBuildDefinedUnderAnUndefinedNameIsGiven(t *testing.T) {
	if _, err := exec.LookPath("c++"); err != nil {
		t.Skip("no C++ compiler")
	}
	if _, err := exec.LookPath("nm"); err != nil {
		t.Skip("no nm")
	}
	ws := t.TempDir()
	obj := filepath.Join(ws, "build", ".obj")
	os.MkdirAll(obj, 0o755)
	src := filepath.Join(ws, "embed_parity.cu.cpp")
	os.WriteFile(src, []byte("int main(int argc, char** argv) { (void)argv; return argc; }\n"), 0o644)
	if out, err := exec.Command("c++", "-c", "-Dmain=e2e_embed_main", "-o", filepath.Join(obj, "e2e_embed.o"), src).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	out := "e2e_all_check.cu:(.text+0x58d): undefined reference to `e2e_embed_main()'\n" +
		"e2e_all_check.cu:(.text+0x58d): undefined reference to `e2e_embed_main()'\n" +
		"x.cu:(.text+0x9): undefined reference to `nowhere(int)'\n"
	got := undefinedRefNear(ws, out)
	for _, want := range []string{
		"  undefined e2e_embed_main() — e2e_embed_main(int, char**) is defined in build/.obj/e2e_embed.o\n",
		"  undefined nowhere(int) — no object file of the workspace defines a function of that name\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "undefined e2e_embed_main()") != 1 {
		t.Fatalf("a reference given twice:\n%s", got)
	}
	if undefinedRefNear(ws, "y.cu:3: error: z") != "" {
		t.Fatal("no undefined reference, something said")
	}
}
