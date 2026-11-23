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
