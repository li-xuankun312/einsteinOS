package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The verdict of 11/10 06:26 against u0063babba8 named kv_append_q8_kernel in
// src/kernels/cuda/kv_q8.cu; src/ilu_kv_q8.cu defined it too. Both files are measured, and a
// name defined once (or only declared elsewhere) is not listed.
func TestEveryCopyOfANamedDefinitionIsFound(t *testing.T) {
	const branch = "einsteinOS/dev-3089b9ac95"
	dir := t.TempDir()
	git := func(a ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, a...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", a, out)
		}
	}
	write := func(f, text string) {
		p := filepath.Join(dir, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(text), 0o644)
	}
	git("init", "-q", "-b", branch)
	k := "__global__ void kv_append_q8_kernel(const float* x, int8_t* q, half* s, int n) {\n    const float inv = 127.f / amax;\n}\n"
	write("src/kernels/cuda/kv_q8.cu", k+"cudaError_t kv_gather_q8_step(int n) {\n    return cudaSuccess;\n}\n")
	write("src/ilu_kv_q8.cu", k)
	write("include/baloon/kv_q8_kernels.hpp", "__global__ void kv_append_q8_kernel(const float* x, int8_t* q, half* s, int n);\n")
	git("add", "-A")
	git("commit", "-qm", "units")
	verdict := "kv_append_q8_kernel (src/kernels/cuda/kv_q8.cu) quantizes with unrounded amax instead of the stored fp16 scale the kv_cache_q8 theory requires (~0.6% of codes differ), and kv_gather_q8_step is untested"
	got := definitionCopies(dir, branch, verdict)
	if len(got) != 1 || strings.Join(got["kv_append_q8_kernel"], " ") != "src/ilu_kv_q8.cu src/kernels/cuda/kv_q8.cu" {
		t.Fatalf("copies: %v", got)
	}
	text := reviewCopiesText(branch, got)
	if !strings.Contains(text, "kv_append_q8_kernel: src/ilu_kv_q8.cu, src/kernels/cuda/kv_q8.cu") {
		t.Fatalf("text: %q", text)
	}
	if reviewCopiesText(branch, nil) != "" {
		t.Fatal("no copies, no text")
	}
}
