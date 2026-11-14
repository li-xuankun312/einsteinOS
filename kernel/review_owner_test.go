package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The files a verdict names are matched to the unit that wrote them (its port map) or whose
// measured sources they are; 11/08 unit pf7b804cf95's three verdicts named files of
// u41e2bf114f and u4decb4e657.
func TestAReviewsFilesAreMatchedToTheUnitThatOwnsThem(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, filepath.Dir(PortMapPath("x"))), 0o755)
	os.WriteFile(filepath.Join(ws, PortMapPath("u41e2bf114f")),
		[]byte("src/prefill/kernels.cu\tkv_append\tsrc/prefill/kernels.cu\tkv_append\nsrc/prefill/prefill.cpp\tbytes_needed\tsrc/prefill/prefill_host.cpp\tbytes_needed\n"), 0o644)
	os.WriteFile(filepath.Join(ws, PortMapPath("u4decb4e657")),
		[]byte("src/kernels/cuda/gr.cu\tgr_norm\tsrc/kernels/gpu/gr_core.cu\tgr_norm\n"), 0o644)
	git := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = ws
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "dev-x")
	git("add", "-A")
	git("commit", "-qm", "maps")
	// the maps are read from the branch, not from the working tree
	os.RemoveAll(filepath.Join(ws, filepath.Dir(PortMapPath("x"))))
	written := portMapOwners(ws, "dev-x")
	if got := written["src/prefill/kernels.cu"]; len(got) != 1 || got[0] != "u41e2bf114f" {
		t.Fatalf("port map owners: %v", written)
	}

	verdict := "FAIL: gr_norm (src/kernels/gpu/gr_core.cu) normalizes jointly over all 4 streams, and kv_append in src/prefill/kernels.cu writes f32 scales; include/strata/prefill/gemm.hpp is fine"
	sources := map[string][]string{"pf7b804cf95": {"include/strata/prefill/gemm.hpp", "include/strata/prefill/kernels.hpp"}}
	owners := reviewOwners(verdict, written, sources)
	if len(owners) != 3 || owners["src/kernels/gpu/gr_core.cu"][0] != "u4decb4e657" || owners["include/strata/prefill/gemm.hpp"][0] != "pf7b804cf95" {
		t.Fatalf("owners: %v", owners)
	}
	blames := reviewBlames("pf7b804cf95", owners)
	if strings.Join(blames, ",") != "u41e2bf114f,u4decb4e657" {
		t.Fatalf("blames: %v", blames)
	}
	if b := reviewBlames("pf7b804cf95", reviewOwners("FAIL: include/strata/prefill/gemm.hpp is wrong", written, sources)); len(b) != 0 {
		t.Fatalf("its own file blames nobody: %v", b)
	}
	if n := len(reviewPaths("FAIL: the test only checks chunk-invariance")); n != 0 {
		t.Fatalf("a verdict naming no file: %d", n)
	}
}

// A verdict that names no file is attributed by the definition it names: the unit whose
// written file holds that definition's body. The port map lines and file text are the ones on
// zeng-bin3123/baloon einsteinOS/dev-3b59928b8f (11/08).
func TestAVerdictWithoutAPathIsAttributedByTheDefinitionItNames(t *testing.T) {
	entries := []portMapEntry{
		{Unit: "pf7b804cf95", File: "include/baloon/prefill_kernels_api.hpp", Sym: "gr_norm"},
		{Unit: "u41e2bf114f", File: "src/prefill/kernels.cu", Sym: "gr_norm"},
		{Unit: "u4decb4e657", File: "src/kernels/gpu/gr_core.cu", Sym: "gr_norm_kernel"},
	}
	files := map[string]string{
		"include/baloon/prefill_kernels_api.hpp": "int gr_norm(const float* x, float* out, int T, float eps);                                  // rms over the 4 streams of a token\n",
		"src/prefill/kernels.cu":                 "int gr_norm(const float* x, float* out, int T, float eps) {\n    if (!x || !out || T <= 0) return -1;\n    gr_norm_kernel<<<(unsigned)T, 256>>>(x, out, eps);\n",
		"src/kernels/gpu/gr_core.cu":             "__global__ void gr_norm_kernel(const float* x, float* out, float eps) {\n}\n",
	}
	verdict := "gr_norm normalizes jointly over all 4 streams (D=10240) but theory/decode reference norm each stream row (N=2560) separately, and the unit test cannot detect it"
	if p := reviewPaths(verdict); len(p) != 0 {
		t.Fatalf("the verdict names no file: %v", p)
	}
	owners := reviewSymbolOwners(verdict, entries, func(f string) string { return files[f] })
	if len(owners) != 1 || len(owners["src/prefill/kernels.cu"]) != 1 || owners["src/prefill/kernels.cu"][0] != "u41e2bf114f" {
		t.Fatalf("owners: %v", owners)
	}
	if b := reviewBlames("pf7b804cf95", owners); len(b) != 1 || b[0] != "u41e2bf114f" {
		t.Fatalf("blames: %v", b)
	}
}
