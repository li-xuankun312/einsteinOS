package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A changed .cuh counts as compiled for the GPU only through a .cu that includes it and that
// a CUDA compile named — not through whichever existing src/*.cu the build compiled.
func TestAChangedCuhIsCompiledThroughItsIncluder(t *testing.T) {
	ws := t.TempDir()
	put := func(rel, text string) {
		os.MkdirAll(filepath.Join(ws, filepath.Dir(rel)), 0o755)
		os.WriteFile(filepath.Join(ws, rel), []byte(text), 0o644)
	}
	put("src/ilu_qsa_common.cuh", "__device__ int lane() { return 0; }\n")
	put("src/ilu_qsa_decode.cu", "#include \"ilu_qsa_common.cuh\"\n__global__ void k() {}\n")
	put("src/ix_gdn.cu", "__global__ void g() {}\n")
	changed := []string{"src/ilu_qsa_common.cuh"}
	inc := cuhIncluders(ws, changed)
	if got := inc["src/ilu_qsa_common.cuh"]; len(got) != 1 || got[0] != "src/ilu_qsa_decode.cu" {
		t.Fatalf("includers: %v", inc)
	}
	tc := "/usr/local/corex/bin/clang++ -x cuda --cuda-gpu-arch=ivcore10 -c "
	in := GateInput{Req: UnitRequirements{GPUSources: []string{"src/kernels/cuda/qsa.cu"}}, Changed: changed,
		Build: ProcTrace{Cmdlines: []string{tc + "src/ix_gdn.cu -o a.o"}}, Includers: inc, IncludersMeasured: true}
	if k, txt := unitGates(in); !strings.Contains(txt, "gpu_not_compiled") || !strings.Contains(txt, "ilu_qsa_common.cuh") {
		t.Fatalf("only an unrelated existing .cu compiled: %q %q", k, txt)
	}
	in.Build.Cmdlines = append(in.Build.Cmdlines, tc+"src/ilu_qsa_decode.cu -o b.o")
	if _, txt := unitGates(in); strings.Contains(txt, "gpu_not_compiled") {
		t.Fatalf("compiled through its includer: %q", txt)
	}
}
