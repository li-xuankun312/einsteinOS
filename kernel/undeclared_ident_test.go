package kernel

import (
	"strings"
	"testing"
)

// 11/25 22:50:31 router_top10_parity.cu:44 "use of undeclared identifier 'ix_router_top10_wave'"
// with #include "ix_router_top10_wave.cu" 67 lines before: where the name is written and the #if
// standing over it are given
func TestWhereAnUndeclaredNameIsWrittenIsGiven(t *testing.T) {
	ws := t.TempDir()
	putTree(t, ws, map[string]string{
		"src/ix_router_top10_wave.cu": "#include <cuda.h>\n" +
			"#ifdef __ILUVATAR_WAVE64__\n" +
			"cudaError_t ix_router_top10_wave(const float* lg, int n) { return cudaSuccess; }\n" +
			"#endif\n",
		"tests/k39_integration/router_top10_parity.cu": "#include \"ix_router_top10_wave.cu\"\n" +
			"// ix_router_top10_wave(x) in a comment\n" +
			"int main() { return 0; }\n",
	})
	out := "tests/k39_integration/router_top10_parity.cu:44:8: error: use of undeclared identifier 'ix_router_top10_wave'\n" +
		"    ok(ix_router_top10_wave(d_lg, 1, NE, K, d_ids_wave, d_wt_wave) == cudaSuccess && sync_ok(),\n"
	got := undeclaredNear(ws, out)
	for _, want := range []string{
		"  ix_router_top10_wave:\n",
		"    src/ix_router_top10_wave.cu:3: cudaError_t ix_router_top10_wave(const float* lg, int n) { return cudaSuccess; }\n",
		"      under ifdef __ILUVATAR_WAVE64__\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "in a comment") || strings.Contains(got, "#include") {
		t.Fatalf("a comment or the include given as a definition:\n%s", got)
	}
	// a name nothing writes
	if got := undeclaredNear(ws, "a.cu:1:1: error: use of undeclared identifier 'nowhere_at_all'\n"); !strings.Contains(got, "no source of the workspace writes nowhere_at_all") {
		t.Fatalf("%s", got)
	}
	if undeclaredNear(ws, "a.cu:1:1: error: something else\n") != "" {
		t.Fatal("no undeclared identifier, something said")
	}
}
