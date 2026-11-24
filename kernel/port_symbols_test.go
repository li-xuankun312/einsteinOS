package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putTree(t *testing.T, tree string, files map[string]string) {
	t.Helper()
	for p, s := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(tree, p)), 0o755)
		if err := os.WriteFile(filepath.Join(tree, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const gdnHeader1123 = `#pragma once
namespace kg {
cudaError_t gdn_bf16_conv_step(const uint16_t* w, float* state, const float* x, float* out, int C, int dc, cudaStream_t s = 0);
cudaError_t gdn_bf16_conv_step(const uint16_t* w, float* state, const float* x, float* out, int C, int dc, cudaStream_t s = 0);
cudaError_t gdn_ssm_norm(const float* o, const float* norm_w, float* y,
                         int h_v, int S, float eps, cudaStream_t s = 0);
}  // namespace kg
`

func gdnSource(extra string) string {
	return `#include "kg_kernels.hpp"
namespace kg {
static __device__ float sig(float x) { return 1.f / (1.f + expf(-x)); }
__global__ void gdn_bf16_conv_step_kernel(const uint16_t* w, float* st) {
    if (threadIdx.x == 0) { st[0] = 0; }
}
cudaError_t gdn_bf16_conv_step(const uint16_t* w, float* state, const float* x, float* out, int C, int dc, cudaStream_t s) {
    gdn_bf16_conv_step_kernel<<<1, 1, 0, s>>>(w, state);
    return cudaGetLastError();
}
cudaError_t gdn_ssm_norm(const float* o, const float* norm_w, float* y,
                         int h_v, int S, float eps, cudaStream_t s)
{
    return cudaSuccess;
}
` + extra + `}  // namespace kg
`
}

// 11/23 13:32: gdn_layer.cu and gdn_qwen.cu each defined the GDN functions the header declared
// three times — the functions two sources define are measured, a static helper and a kernel of
// one name in two namespaces are not counted
func TestFunctionsDefinedInTwoSourcesAreMeasured(t *testing.T) {
	tree := t.TempDir()
	putTree(t, tree, map[string]string{
		"include/baloon/kg_kernels.hpp": gdnHeader1123,
		"src/kernels/gpu/gdn_layer.cu":  gdnSource(""),
		"src/kernels/gpu/gdn_qwen.cu":   gdnSource(""),
		"src/kernels/gpu/other.cu":      "namespace other {\ncudaError_t gdn_ssm_norm(const float* o) {\n  return cudaSuccess;\n}\n}\nstruct S {\n  int f(int a) { return a; }\n};\n",
		"src/a.cpp":                     "int f(int a) {\n  if (a) {\n    return g(a);\n  }\n  return 0;\n}\n",
		"src/b.cpp":                     "static int f(int a) { return a; }\n",
	})
	got := redefinedFuncs(treeFuncSites(tree))
	want := []string{
		"kg::gdn_bf16_conv_step: src/kernels/gpu/gdn_layer.cu, src/kernels/gpu/gdn_qwen.cu",
		"kg::gdn_bf16_conv_step_kernel: src/kernels/gpu/gdn_layer.cu, src/kernels/gpu/gdn_qwen.cu",
		"kg::gdn_ssm_norm: src/kernels/gpu/gdn_layer.cu, src/kernels/gpu/gdn_qwen.cu",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	ms := ModelShape{Tensors: []ModelTensor{{Name: "model.norm.weight", Dtype: "BF16", Shape: []int64{8}}}}
	blk := measurePortInventory(tree, ms).Block(1)
	if !strings.Contains(blk, "### 3 function(s) defined in more than one source") || !strings.Contains(blk, "kg::gdn_ssm_norm: src/kernels/gpu/gdn_layer.cu, src/kernels/gpu/gdn_qwen.cu") {
		t.Fatalf("%s", blk)
	}
}

// 11/23 16:11: both GDN definitions deleted, the header's declarations and the parity test's
// calls left — the functions no source defines are measured; one declared and never called, or
// defined inline in a header, is not
func TestFunctionsDeclaredAndCalledButDefinedNowhereAreMeasured(t *testing.T) {
	tree := t.TempDir()
	putTree(t, tree, map[string]string{
		"include/baloon/kg_kernels.hpp":             gdnHeader1123 + "namespace kg {\ncudaError_t never_called(int n);\ninline int helper(int a) { return a; }\ncudaError_t rms_norm_bf16w_inplace(float* x, int n);\n}\n",
		"tests/k39_integration/gdn_layer_parity.cu": "#include \"kg_kernels.hpp\"\nint main() {\n  kg::gdn_bf16_conv_step(nullptr, nullptr, nullptr, nullptr, 1, 4);\n  kg::helper(1);\n  return kg::gdn_ssm_norm(nullptr, nullptr, nullptr, 1, 1, 1e-6f) + kg::rms_norm_bf16w_inplace(nullptr, 1);\n}\n",
		"src/kernels/gpu/ew_ops.cu":                 "namespace kg {\ncudaError_t rms_norm_bf16w_inplace(float* x, int n) {\n  return cudaSuccess;\n}\n}\n",
	})
	got := undefinedFuncs(tree, treeFuncSites(tree))
	want := []string{
		"kg::gdn_bf16_conv_step: declared in include/baloon/kg_kernels.hpp, called 1 time(s), defined in no source",
		"kg::gdn_ssm_norm: declared in include/baloon/kg_kernels.hpp, called 1 time(s), defined in no source",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	ms := ModelShape{Tensors: []ModelTensor{{Name: "model.norm.weight", Dtype: "BF16", Shape: []int64{8}}}}
	if blk := measurePortInventory(tree, ms).Block(1); !strings.Contains(blk, "### 2 function(s) a header declares and a source calls that no source defines") {
		t.Fatalf("%s", blk)
	}
}

// 11/23 16:14:40 compile=fail went back with the compiler's tail alone: the functions the mirror
// defines twice or nowhere go with it; a compile that passed carries nothing
func TestAFailedCompileCarriesTheTreesFunctions(t *testing.T) {
	tree := t.TempDir()
	putTree(t, tree, map[string]string{
		"include/baloon/kg_kernels.hpp":             gdnHeader1123,
		"tests/k39_integration/gdn_layer_parity.cu": "int main() { return kg::gdn_ssm_norm(nullptr, nullptr, nullptr, 1, 1, 1e-6f); }\n",
	})
	got := compileSymbolFacts("fail", tree)
	if !strings.Contains(got, "0 function(s) defined in more than one source") || !strings.Contains(got, "1 function(s) a header declares and a source calls that no source defines:\n  kg::gdn_ssm_norm: declared in include/baloon/kg_kernels.hpp") {
		t.Fatalf("%s", got)
	}
	if compileSymbolFacts("pass", tree) != "" {
		t.Fatal("a compile that passed carries the facts")
	}
}

// 11/24 the measures named main in 30 programs and run_test in each tests/unitN/test_main.cpp as
// defined twice, and block_layer and session_zero, each a one-line body, as defined nowhere; a
// definition in a C++17 nested namespace or written qualified is a definition too
func TestTheFunctionMeasuresLeaveOutProgramsAndReadOneLineBodies(t *testing.T) {
	tree := t.TempDir()
	putTree(t, tree, map[string]string{
		"include/baloon/core_layer.hpp": "namespace baloon {\nnamespace core {\nBlockResult block_layer(const BlockCall& c, const BlockHooks& h);\nvoid session_zero(Session& s);\nint map_kind(int k);\n}\n}\n",
		"src/core/layer.cpp":            "namespace baloon {\nnamespace core {\nBlockResult block_layer(const BlockCall& c, const BlockHooks& h) { return block_layer_post(c, h, block_layer_pre(c, h)); }\n}\n}\n",
		"src/core/session.cpp":          "namespace baloon::core {\nvoid session_zero(Session& s) { std::fill(s.arena.begin(), s.arena.end(), (uint8_t)0); }\n}\n",
		"src/core/weights.cpp":          "int baloon::core::map_kind(int k) {\n  return k;\n}\n",
		"src/use.cpp":                   "void use() { baloon::core::block_layer(c, h); baloon::core::session_zero(s); baloon::core::map_kind(1); }\n",
		"src/a_main.cpp":                "int human(int x) { return x; }\nint main() { return 0; }\n",
		"src/b_main.cpp":                "int human(int x) { return x; }\nint main() { return 0; }\n",
		"tests/unit1/test_main.cpp":     "int run_test() {\n  return 0;\n}\n",
		"tests/unit2/test_main.cpp":     "int run_test() {\n  return 0;\n}\n",
	})
	sites := treeFuncSites(tree)
	if re := redefinedFuncs(sites); len(re) != 0 {
		t.Fatalf("redefined %q", re)
	}
	if un := undefinedFuncs(tree, sites); len(un) != 0 {
		t.Fatalf("undefined %q", un)
	}
}
