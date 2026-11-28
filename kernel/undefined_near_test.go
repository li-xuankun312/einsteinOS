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

// 11/25 08:08:14 ix_se's kernels were defined in pic_e2e_moe.o and pic_e2e_se.o: the .cu file
// writing them is given, with the two sources that #include it
func TestWhereAKernelTheLinkFoundTwiceIsWrittenIsGiven(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		"src/ix_shared_expert.cu":             "namespace ix_se {\n__global__ void swiglu_kernel(const float* g, const float* u, float* y, int n) { }\n}\n",
		"tests/k39_integration/moe_parity.cu": "#include \"../../src/ix_shared_expert.cu\"\nint main() { return 0; }\n",
		"tests/test_shared_expert.cu":         "#include \"../src/ix_shared_expert.cu\"\nint main() { return 0; }\n",
	})
	out := "moe_parity.cu:(.text+0x0): multiple definition of `ix_se::__device_stub__swiglu_kernel(float const*, float const*, float*, int)'; x/pic_e2e_se.o:test_shared_expert.cu:(.text+0x0): first defined here\n" +
		"moe_parity.cu:(.text+0x90): multiple definition of `ix_se::__device_stub__nowhere_kernel(int)'; y\n"
	got := multipleDefNear(ws, out)
	for _, want := range []string{
		"  swiglu_kernel — written in src/ix_shared_expert.cu, which tests/k39_integration/moe_parity.cu and tests/test_shared_expert.cu #include\n",
		"  nowhere_kernel — no source of the workspace has a __global__ definition of it\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
	if multipleDefNear(ws, "a.cu:3: error: x") != "" {
		t.Fatal("no multiple definition, something said")
	}
}

// 11/26 02:56:03 the link failed on baloon::rt::fail, build.sh writing its objects to
// /tmp/baloon_integration_whole: with no object file in the workspace, where the sources write
// the name is given — the inline declaration with no body among them, as written
func TestWithNoObjectInTheWorkspaceTheSourcesAreGiven(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		"include/baloon/rt_layout.hpp": "namespace baloon { namespace rt {\n" +
			"inline bool fail(std::string* why, const std::string& msg);   // stores msg, returns false\n" +
			"} }\n",
		"src/rt/graph.cpp": "namespace baloon { namespace rt {\n" +
			"bool fail(std::string* why, const std::string& msg) { if (why) *why = msg; return false; }\n" +
			"} }\n",
	})
	out := "test_rt.cpp:(.text+0x6b8e): undefined reference to `baloon::rt::fail(std::__cxx11::basic_string<char>*, std::__cxx11::basic_string<char> const&)'\n"
	got := undefinedRefNear(ws, out)
	for _, want := range []string{
		"  baloon::rt::fail:\n",
		"include/baloon/rt_layout.hpp:2: inline bool fail(std::string* why, const std::string& msg);",
		"src/rt/graph.cpp:2: bool fail(std::string* why, const std::string& msg) {",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
}
