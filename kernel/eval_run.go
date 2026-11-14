package kernel

// eval_run.go — a repository's own benchmarks run on this machine, by the kernel.
//
// The reference at the commit being integrated, and every repository eval_discover took, are
// checked out from their bare clones, built with their own build (CMake, with the GPU
// toolchain of this machine as the CUDA compiler when there is one), and their benchmarks are
// run as the repository declares them. The kernel only executes and reads (eval_metrics.go):
// what was built, what ran, which devices were open, and the numbers printed.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// EvalBenchRun is one benchmark's run.
type EvalBenchRun struct {
	Bench   EvalBench `json:"bench"`
	Exit    int       `json:"exit"`
	Metrics []Metric  `json:"metrics"`
	Tail    string    `json:"tail"`
	Devices []string  `json:"devices"` // GPU device nodes its processes had open
	// Kernels: the kernels the run launched, by the launch probe (the end-to-end run only);
	// ProbeErr: why that was not measured
	Kernels  []string `json:"kernels,omitempty"`
	ProbeErr string   `json:"probe_err,omitempty"`
	// Programs: the executables of the workspace the run's processes ran, path → sha256 of
	// the file (the end-to-end run only)
	Programs map[string]string `json:"programs,omitempty"`
	// PromptIDs, GeneratedIDs: the token ids it printed (eval_tokens.go)
	PromptIDs    []int `json:"prompt_ids,omitempty"`
	GeneratedIDs []int `json:"generated_ids,omitempty"`
}

// EvalRun is a repository at one commit, built and benchmarked here.
type EvalRun struct {
	Repo      string         `json:"repo"`
	SHA       string         `json:"sha"`
	BuildExit int            `json:"build_exit"`
	BuildTail string         `json:"build_tail"`
	Benches   []EvalBenchRun `json:"benches"`
	Err       string         `json:"err,omitempty"` // why nothing was run
}

// Metrics: every metric of every benchmark that exited 0.
func (r EvalRun) Metrics() []Metric {
	var out []Metric
	for _, b := range r.Benches {
		if b.Exit == 0 {
			out = append(out, b.Metrics...)
		}
	}
	return out
}

var evalBuildTimeout, evalBenchTimeout = 60 * time.Minute, 20 * time.Minute

// evalPrefetchFn fetches the blobs of a commit a blobless clone lacks (a variable for tests)
var evalPrefetchFn = prefetchCommitBlobs

// evalRunRepo checks out bare at sha into a fresh directory, builds it and runs benches.
func evalRunRepo(repo, bare, sha string, benches []EvalBench) EvalRun {
	run := EvalRun{Repo: repo, SHA: sha, BuildExit: -1}
	dir, err := os.MkdirTemp("", "einsteinos_eval_")
	if err != nil {
		run.Err = err.Error()
		return run
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "src")
	os.MkdirAll(src, 0o755)
	// the clones are blobless: the commit's files are fetched first in batches, by the routes
	// and retries the reference's clone has, not one by one by git archive's lazy fetch over
	// HTTPS with no retry — 11/10 triton-lang/triton was taken and then not run: "checkout:
	// fatal: unable to access …: GnuTLS recv error (-110) … tar: This does not look like a tar
	// archive"
	evalPrefetchFn(bare, sha)
	co := fmt.Sprintf("git --git-dir=%q archive %s | tar -x -C %q", bare, sha, src)
	if out, err := exec.Command("sh", "-c", co).CombinedOutput(); err != nil {
		run.Err = "checkout: " + tailText(string(out), 300)
		return run
	}
	if _, err := os.Stat(filepath.Join(src, "CMakeLists.txt")); err == nil {
		code, out, _, err := runWatched(evalCMakeCommand(src), src, evalBuildTimeout)
		run.BuildExit, run.BuildTail = code, tailText(out, 1500)
		if err != nil {
			run.BuildTail = err.Error()
		}
	} else {
		run.BuildExit, run.BuildTail = 0, "no CMakeLists.txt: nothing to build"
	}
	for _, b := range benches {
		br := EvalBenchRun{Bench: b, Exit: -1}
		cmd := evalBenchCommand(src, b)
		if cmd == "" {
			br.Tail = "not built: no executable " + b.Name + " in the build"
		} else {
			code, out, tr, err := runWatched(cmd, src, evalBenchTimeout)
			br.Exit, br.Metrics, br.Tail, br.Devices = code, metricsIn(out), tailText(out, 1500), tr.Devices
			if err != nil {
				br.Tail = err.Error()
			}
		}
		run.Benches = append(run.Benches, br)
	}
	return run
}

// evalCMakeCommand: the repository's own CMake build, the CUDA compiler being this machine's
// toolchain when it has one.
func evalCMakeCommand(src string) string {
	cuda := ""
	for _, c := range presentGPUCompilers() {
		if strings.HasSuffix(c, "/clang++") || cuda == "" {
			cuda = c
		}
	}
	args := "-DCMAKE_BUILD_TYPE=Release"
	if cuda != "" {
		args += fmt.Sprintf(" -DCMAKE_CUDA_COMPILER=%q", cuda)
	}
	return fmt.Sprintf("cmake -S %q -B %q %s 2>&1 && cmake --build %q -j %d 2>&1",
		src, filepath.Join(src, "build"), args, filepath.Join(src, "build"), runtime.NumCPU())
}

// evalBenchCommand: how the repository declares b runs.
func evalBenchCommand(src string, b EvalBench) string {
	build := filepath.Join(src, "build")
	switch b.Kind {
	case "cmake_test":
		return fmt.Sprintf("ctest --test-dir %q -R %q -V --timeout %d 2>&1", build, "^"+b.Name+"$", int(evalBenchTimeout.Seconds()))
	case "cmake_target":
		exe := ""
		filepath.WalkDir(build, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() == b.Name {
				if st, e := d.Info(); e == nil && st.Mode()&0o111 != 0 {
					exe = p
				}
			}
			return nil
		})
		if exe == "" {
			return ""
		}
		return fmt.Sprintf("%q 2>&1", exe)
	case "script":
		p := filepath.Join(src, b.File)
		if strings.HasSuffix(b.File, ".py") {
			return fmt.Sprintf("python3 %q 2>&1", p)
		}
		return fmt.Sprintf("bash %q 2>&1", p)
	}
	return ""
}
