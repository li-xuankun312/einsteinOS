package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// strataLikeRef builds a bare repo shaped like Strata@01529aa: CUDA kernels, CPU SIMD
// kernels with per-source flags (GCC and MSVC branches), a test file that must not count.
func strataLikeRef(t *testing.T) (bare, sha string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	work := filepath.Join(root, "w")
	files := map[string]string{
		"CMakeLists.txt": `if(MSVC)
  set_source_files_properties(src/kernels/cpu/expert.cpp PROPERTIES COMPILE_OPTIONS "/arch:AVX512")
else()
  set_source_files_properties(src/kernels/cpu/expert.cpp
                              PROPERTIES COMPILE_OPTIONS "-mavx512f;-mavx512vnni;-mfma")
  set_source_files_properties(src/kernels/cpu/q2_avx2.cpp PROPERTIES COMPILE_OPTIONS "-mavx2;-mfma;-mf16c")
endif()
`,
		"cmake/extra.cmake":                   `set_source_files_properties(iq_avx512.cpp PROPERTIES COMPILE_OPTIONS "-mavx512bw;-O3")` + "\n",
		"src/kernels/cuda/fused_gdn.cu":       "__global__ void gdn_step_norm_kernel() {}\n__global__ void gdn_conv_l2_kernel() {}\n",
		"src/kernels/cuda/fused_gdn_test.cpp": "void t() { gdn_step_norm_kernel(); }\n",
		"src/kernels/cpu/expert.cpp":          "void s2_expert_vnni_q() {}\nvoid act_quant_q8_1() {}\nint cpu_features;\n",
		"src/kernels/cpu/q2_avx2.cpp":         "void row_dot_multi() {}\nvoid unpack_block() {}\n",
		"src/common/everywhere.h":             "int row_dot;\n",
	}
	for i := 0; i < 10; i++ { // row_dot is in too many files to say anything
		files[filepath.Join("src", "noise", strings.Repeat("n", i+1)+".cpp")] = "int row_dot;\n"
	}
	for p, c := range files {
		full := filepath.Join(work, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	gitT(t, root, "init", "-q", work)
	gitT(t, work, "add", "-A")
	gitT(t, work, "commit", "-q", "-m", "ref")
	bare = filepath.Join(root, "ref.git")
	gitT(t, root, "clone", "-q", "--bare", work, bare)
	return bare, gitT(t, work, "rev-parse", "HEAD")
}

func TestRefPerSourceFlagsFollowsTheReferenceBuild(t *testing.T) {
	bare, sha := strataLikeRef(t)
	got := refPerSourceFlags(bare, sha)
	want := map[string][]string{
		"src/kernels/cpu/expert.cpp":  {"-mavx512f", "-mavx512vnni", "-mfma"},
		"src/kernels/cpu/q2_avx2.cpp": {"-mavx2", "-mf16c", "-mfma"},
		"cmake/iq_avx512.cpp":         {"-mavx512bw"}, // relative to its build file; -O3 is not an ISA flag
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for _, fl := range got {
		for _, x := range fl {
			if strings.HasPrefix(x, "/arch") {
				t.Fatal("MSVC spelling taken")
			}
		}
	}
}

