package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The instructions a reference file's ISA flag stands for must be in the unit's counterparts
// of that file's definitions, not anywhere in the binary. 11/09 unit ube4f27df84 passed
// isa_path_missing with AVX-512 code placed in functions of unrelated files that nothing called
// ("isa_selfprobe.hpp"), and its review failed it for that. Real code, compiled here.
func TestTheISAInstructionsMustBeInThePortedFunctions(t *testing.T) {
	if _, err := exec.LookPath("objdump"); err != nil {
		t.Skip("no objdump")
	}
	cxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("no g++")
	}
	dir := t.TempDir()
	src := `#include <immintrin.h>
namespace baloon { namespace cpu {
// the unit's counterpart of the reference's q2_rows: scalar
float q2_rows(const float* x, int n) { float s = 0; for (int i = 0; i < n; i++) s += x[i]; return s; }
}}
// injected and never called
__attribute__((target("avx512f"))) float isa_selfprobe(const float* x) {
  __m512 v = _mm512_loadu_ps(x); return _mm512_reduce_add_ps(v);
}
int main() { float x[16] = {0}; return (int)baloon::cpu::q2_rows(x, 16); }
`
	os.WriteFile(filepath.Join(dir, "a.cpp"), []byte(src), 0o644)
	bin := filepath.Join(dir, "a")
	if out, err := exec.Command(cxx, "-O2", "-fno-inline", "-o", bin, filepath.Join(dir, "a.cpp")).CombinedOutput(); err != nil {
		t.Fatalf("g++: %v\n%s", err, out)
	}
	pm := map[string]portEntry{"src/cpu/q2.cpp\tq2_rows": {refFile: "src/cpu/q2.cpp", refName: "q2_rows", file: "src/cpu/q2.cpp", name: "baloon::cpu::q2_rows"}}
	funcs := isaPortFuncs(pm, map[string][]string{"src/cpu/q2.cpp": {"-mavx512f", "-O3"}})
	if len(funcs["-mavx512f"]) != 1 || funcs["-mavx512f"][0] != "q2_rows" {
		t.Fatalf("funcs: %v", funcs)
	}
	whole, _ := isaFound([]string{bin}, []string{"-mavx512f"})
	if !whole["-mavx512f"] {
		t.Fatal("the binary holds AVX-512 somewhere")
	}
	in, err := isaFoundIn([]string{bin}, []string{"-mavx512f"}, funcs)
	if err != nil || in["-mavx512f"] {
		t.Fatalf("q2_rows has no AVX-512: found=%v err=%v", in, err)
	}
	// a flag whose ported functions are not in the binaries is looked for anywhere
	other, _ := isaFoundIn([]string{bin}, []string{"-mavx512f"}, map[string][]string{"-mavx512f": {"inlined_away"}})
	if !other["-mavx512f"] {
		t.Fatal("absent functions fall back to the whole binary")
	}
	kind, _ := unitGates(GateInput{Req: UnitRequirements{ISAFlags: []string{"-mavx512f"}, FlagFiles: map[string][]string{"src/cpu/q2.cpp": {"-mavx512f"}}},
		Test: ProcTrace{Exes: []string{bin}}, ISAFuncs: funcs})
	if kind != "isa_path_missing" {
		t.Fatalf("gate: %q", kind)
	}
}

func TestAsmFuncBase(t *testing.T) {
	for in, want := range map[string]string{
		"baloon::cpu::q2_rows(float const*, int)": "q2_rows",
		"isa_selfprobe":                         "isa_selfprobe",
		"void foo<int>(int)":                    "foo",
		"baloon::(anonymous namespace)::k(int)": "k",
	} {
		if got := asmFuncBase(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
