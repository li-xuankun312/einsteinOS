package kernel

// unit_gates.go — what "verified" requires beyond two exit codes, judged on measurements.
//
// A gate is a fact about the unit the kernel measured (its changed files, the processes
// its build and test ran) set against what the reference says the unit is
// (UnitRequirements). The first gate that fails is the unit's failure kind, so the next
// attempt is told exactly that; every failed gate is in the text.

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

// cudaCompileRe: a command line that compiles CUDA (clang -x cuda / --cuda-gpu-arch, nvcc).
var cudaCompileRe = regexp.MustCompile(`(^|\s)-x\s+cuda(\s|$)|--cuda-gpu-arch|(^|/)nvcc(\s|$)`)

// GateInput is everything a gate may look at.
type GateInput struct {
	Req     UnitRequirements
	Changed []string  // files the unit added or changed (repo-relative)
	Build   ProcTrace // what build.sh's processes did
	Test    ProcTrace // what test.sh's processes did
	// Compilers: the GPU compilers present in toolchainDirs on this machine (measured)
	Compilers []string
}

// gpuCompilers are the programs that make a GPU build possible, looked for in toolchainDirs.
var gpuCompilers = []string{"clang++", "nvcc"}

// presentGPUCompilers measures which gpuCompilers exist in toolchainDirs.
func presentGPUCompilers() []string {
	var out []string
	for _, d := range toolchainDirs {
		for _, c := range gpuCompilers {
			p := strings.TrimRight(d, "/") + "/" + c
			if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
				out = append(out, p)
			}
		}
	}
	return out
}

const (
	gateGPUCodeMissingFmt = "gpu_code_missing: the unit's modules come from CUDA sources in the reference (%[1]s) but it wrote no .cu/.cuh file"
	gateGPUNotCompiledFmt = "gpu_not_compiled: build.sh ran no CUDA compile of %[1]s (CUDA compiles seen: %[2]s)"
	gateGPUUnusedFmt      = "gpu_unused: test.sh exited 0 but none of its %[1]d process(es) opened a GPU device (%[2]s); executables that ran: %[3]s"
	gateToolchainFmt      = "toolchain_missing: the unit's sources are CUDA but none of %[1]s exists in %[2]s on this machine; a build that skips the GPU here proves nothing"
)

// GateToolchainMissing is the gate a retry cannot fix: the run stops on it (dev_run).
const GateToolchainMissing = "toolchain_missing"

// unitGates returns the first failed gate's kind and the text of every failed gate.
func unitGates(in GateInput) (kind, text string) {
	var kinds, texts []string
	fail := func(k, t string) { kinds = append(kinds, k); texts = append(texts, t) }

	// a GPU unit is only judged on a machine that can build for the GPU: the 11/03 build.sh
	// files printed "corex toolchain not present … skipping" and still exited 0
	if in.Req.GPU() && len(in.Compilers) == 0 {
		fail(GateToolchainMissing, fmt.Sprintf(gateToolchainFmt, strings.Join(gpuCompilers, "/"), strings.Join(toolchainDirs, ":")))
	}
	gpuFiles := changedGPUFiles(in.Changed)
	if in.Req.GPU() && len(gpuFiles) == 0 {
		fail("gpu_code_missing", fmt.Sprintf(gateGPUCodeMissingFmt, strings.Join(in.Req.GPUSources, ", ")))
	}
	// what the unit wrote must be what its build compiled for the GPU — not only the
	// repository's existing src/*.cu, which is all the 11/03 builds compiled
	if in.Req.GPU() && len(gpuFiles) > 0 {
		if missing := notCudaCompiled(gpuFiles, in.Build.Cmdlines); len(missing) > 0 {
			fail("gpu_not_compiled", fmt.Sprintf(gateGPUNotCompiledFmt, strings.Join(missing, ", "), cudaCompilesSeen(in.Build.Cmdlines)))
		}
	}

	// a GPU unit's tests must run on the GPU: some process of test.sh had a device open
	if in.Req.GPU() && len(in.Test.Devices) == 0 {
		fail("gpu_unused", fmt.Sprintf(gateGPUUnusedFmt, in.Test.Procs, gpuDeviceRe().String(), shortList(in.Test.Exes, 8)))
	}

	if len(kinds) == 0 {
		return "", ""
	}
	return kinds[0], strings.Join(texts, "\n")
}

// changedGPUFiles: the .cu/.cuh among the changed files.
func changedGPUFiles(changed []string) []string {
	var out []string
	for _, f := range changed {
		for _, ext := range gpuSourceExts {
			if path.Ext(f) == ext {
				out = append(out, f)
			}
		}
	}
	return out
}

// notCudaCompiled: the changed .cu files no CUDA compile command line named. A .cuh is
// compiled through a .cu that includes it, so with only headers changed, any CUDA compile
// counts.
func notCudaCompiled(gpuFiles, cmdlines []string) []string {
	var cuda []string
	for _, c := range cmdlines {
		if cudaCompileRe.MatchString(c) {
			cuda = append(cuda, c)
		}
	}
	var missing []string
	anyCU := false
	for _, f := range gpuFiles {
		if path.Ext(f) != ".cu" {
			continue
		}
		anyCU = true
		named := false
		for _, c := range cuda {
			named = named || strings.Contains(c, f) || strings.Contains(c, "/"+path.Base(f)) || strings.Contains(c, " "+path.Base(f))
		}
		if !named {
			missing = append(missing, f)
		}
	}
	if !anyCU && len(cuda) == 0 {
		return gpuFiles
	}
	return missing
}

// cudaCompilesSeen names the files of the CUDA compiles that did run, for the failure text.
func cudaCompilesSeen(cmdlines []string) string {
	var files []string
	for _, c := range cmdlines {
		if !cudaCompileRe.MatchString(c) {
			continue
		}
		for _, w := range strings.Fields(c) {
			if strings.HasSuffix(w, ".cu") {
				files = append(files, w)
			}
		}
	}
	if len(files) == 0 {
		return "none"
	}
	return strings.Join(files, " ")
}

func shortList(xs []string, n int) string {
	if len(xs) == 0 {
		return "none seen"
	}
	if len(xs) > n {
		return strings.Join(xs[:n], " ") + fmt.Sprintf(" (+%d more)", len(xs)-n)
	}
	return strings.Join(xs, " ")
}
