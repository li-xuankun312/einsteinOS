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
	"os/exec"
	"path"
	"regexp"
	"sort"
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
	// PortMissing: reference definitions without a verified counterpart (port_gate.go)
	PortMissing []string
	// Kernels: per changed CUDA file, the __global__ kernels it defines (gpu_kernels.go);
	// KernelsMeasured says the files were read (a hand-built input leaves both empty)
	Kernels         map[string][]string
	KernelsMeasured bool
	// NoToolchain: build.sh run again with the toolchain hidden (build_without_toolchain.go)
	NoToolchain NoToolchainBuild
	// Includers: per changed .cuh, the .cu files of the workspace that #include it
	// (IncludersMeasured says the workspace was read; a hand-built input leaves both empty)
	Includers         map[string][]string
	IncludersMeasured bool
	// RepoKernels: for the integration unit, the kernels the whole working tree defines
	RepoKernels map[string]bool
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
	gateGPUCodeMissingFmt      = "gpu_code_missing: the unit's modules come from CUDA sources in the reference (%[1]s) but it wrote no .cu/.cuh file"
	gateGPUNotCompiledFmt      = "gpu_not_compiled: build.sh ran no CUDA compile of %[1]s (CUDA compiles seen: %[2]s)"
	gateGPUUnusedFmt           = "gpu_unused: test.sh exited 0 but none of its %[1]d process(es) opened a GPU device (%[2]s); executables that ran: %[3]s"
	gateISAMissingFmt          = "isa_path_missing: the reference builds %[1]s with %[2]s; no binary the build produced or the test ran contains %[3]s (binaries looked at: %[4]s)"
	gateISAUnmeasuredFmt       = "isa_unmeasured: the reference builds %[1]s with %[2]s, and the binaries could not be disassembled (%[3]s)"
	gateToolchainUnusedFmt     = "toolchain_unused: the unit is GPU work but no process of build.sh ran a program from %[1]s (programs that ran: %[2]s) — a build that skips the toolchain proves nothing"
	gateGPUKernelMissingFmt    = "gpu_kernel_missing: the unit's modules come from CUDA sources in the reference (%[1]s) but its CUDA files define no __global__ kernel: %[2]s — a host header about a kernel is not the kernel"
	gateKernelNotLaunchedFmt   = "gpu_kernel_not_launched: test.sh exited 0 but launched none of the unit's kernels (%[1]s); kernels its processes launched: %[2]s; executables the launch probe was in: %[3]s — a test that opens the device and checks on the CPU proves nothing about the kernels"
	gateLaunchUnmeasuredFmt    = "gpu_launch_unmeasured: whether the unit's kernels ran on the device could not be measured: %[1]s"
	gateIntegrationNoKernelFmt = "integration_kernel_not_launched: the reference builds CUDA and its %[1]d registered tests passed here, but none of them launched a kernel of this repository (it defines %[2]d: %[3]s); kernels launched: %[4]s"
	gateToolchainFmt           = "toolchain_missing: the unit's sources are CUDA but none of %[1]s exists in %[2]s on this machine; a build that skips the GPU here proves nothing"
)

// GateToolchainMissing is the gate a retry cannot fix: the run stops on it (dev_run).
const GateToolchainMissing = "toolchain_missing"

// gateIsMachine: a gate that measured this machine, not the unit — no toolchain, or no way to
// hide it. A conversation cannot change it: 11/06 17:57-20:18 unit u5c4a635e52's three
// conversations spent two hours on build_toolchain_dependence_unmeasured, rewriting build.sh
// and adding an "oracle" kernel. The conversation is let end and the run stops (dev_run).
func gateIsMachine(kind string) bool {
	return kind == GateToolchainMissing || kind == GateToolchainDependenceUnmeasured
}

// preverifyMayEnd: whether a done line ends the conversation — the unit passed, or what failed
// is the machine.
func preverifyMayEnd(passed bool, gate string) bool { return passed || gateIsMachine(gate) }

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
	// a CUDA file is GPU work only if it holds a kernel: 11/04's FusedGdnKernels,
	// QsaDecodeAttention and PrefillGpuKernels were host headers of index arithmetic
	if in.Req.GPU() && in.KernelsMeasured && len(in.Kernels) > 0 && len(kernelSet(in.Kernels)) == 0 {
		fail("gpu_kernel_missing", fmt.Sprintf(gateGPUKernelMissingFmt, strings.Join(in.Req.GPUSources, ", "), kernelFilesText(in.Kernels)))
	}
	// what the unit wrote must be what its build compiled for the GPU — not only the
	// repository's existing src/*.cu, which is all the 11/03 builds compiled
	if in.Req.GPU() && len(gpuFiles) > 0 {
		missing := notCudaCompiled(gpuFiles, in.Build.Cmdlines)
		if in.IncludersMeasured {
			missing = append(missing, cuhNotCompiled(gpuFiles, in.Includers, in.Build.Cmdlines)...)
		}
		if len(missing) > 0 {
			fail("gpu_not_compiled", fmt.Sprintf(gateGPUNotCompiledFmt, strings.Join(missing, ", "), cudaCompilesSeen(in.Build.Cmdlines)))
		}
	}

	// a GPU unit's tests must run on the GPU: some process of test.sh had a device open
	if (in.Req.GPU() || in.Req.RequireGPU) && len(in.Test.Devices) == 0 {
		fail("gpu_unused", fmt.Sprintf(gateGPUUnusedFmt, in.Test.Procs, gpuDeviceRe().String(), shortList(in.Test.Exes, 8)))
	}

	// no measurement is not a pass: say why the launches could not be seen
	unmeasured := ""
	if in.Req.GPU() || in.Req.RequireGPU {
		if unmeasured = launchUnmeasured(in.Test); unmeasured != "" {
			fail("gpu_launch_unmeasured", fmt.Sprintf(gateLaunchUnmeasuredFmt, unmeasured))
		}
	}
	// the unit's own kernels must have run on the device: the launch probe names every
	// kernel the test's processes launched (launch_probe.go, launch_symbols.go)
	if in.Req.GPU() && in.KernelsMeasured && probeMeasured(in.Test) && unmeasured == "" {
		own := kernelSet(in.Kernels)
		if len(own) > 0 && !anyLaunched(own, in.Test.Launches) {
			fail("gpu_kernel_not_launched", fmt.Sprintf(gateKernelNotLaunchedFmt, setText(own), launchesText(in.Test.Launches), shortList(in.Test.ProbeLoaded, 8)))
		}
	}

	// the integration unit: the reference's tests, run here, must put this repository's
	// kernels on the device — 11/06 its 28 "parity" tests opened GPU 0 and ran the units'
	// own invariant checks
	if len(in.Req.Tests) > 0 && in.Req.RequireGPU && probeMeasured(in.Test) && unmeasured == "" && len(in.RepoKernels) > 0 &&
		!anyLaunched(in.RepoKernels, in.Test.Launches) {
		fail("integration_kernel_not_launched", fmt.Sprintf(gateIntegrationNoKernelFmt, len(in.Req.Tests), len(in.RepoKernels), shortList(sortedKeys(in.RepoKernels), 12), launchesText(in.Test.Launches)))
	}

	// the reference's per-source ISA flags: the paths they enable must exist in what was
	// built and run — 11/03 printed "AVX2 intrinsic path not compiled in this build", and its
	// "VNNI kernel" was an integer emulation with no VNNI instruction in it
	if need := attestableISA(in.Req.ISAFlags); len(need) > 0 {
		bins := isaBinaries(in.Build, in.Test)
		found, err := isaFound(bins, need)
		switch {
		case err != nil:
			fail("isa_unmeasured", fmt.Sprintf(gateISAUnmeasuredFmt, isaSourceList(in.Req), strings.Join(need, " "), err))
		default:
			var missing []string
			for _, f := range need {
				if !found[f] {
					missing = append(missing, f+" ("+isaInsn[f].String()+")")
				}
			}
			if len(missing) > 0 {
				fail("isa_path_missing", fmt.Sprintf(gateISAMissingFmt, isaSourceList(in.Req), strings.Join(need, " "), strings.Join(missing, ", "), shortList(bins, 6)))
			}
		}
	}

	// 11/04 14:27:52 the unit's build.sh printed "corex toolchain not present on this host" and
	// exited 0: whether the toolchain was used is measured from the build's processes
	if (in.Req.GPU() || in.Req.RequireGPU) && len(in.Compilers) > 0 && !toolchainRan(in.Build) {
		fail("toolchain_unused", fmt.Sprintf(gateToolchainUnusedFmt, strings.Join(toolchainDirs, ":"), shortList(in.Build.Exes, 8)))
	}
	// and its exit code must depend on the toolchain: the same build.sh with the toolchain
	// hidden has to fail, or "BUILD OK" is printed either way
	// (a hand-built input without the run leaves NoToolchain zero, like KernelsMeasured)
	if (in.Req.GPU() || in.Req.RequireGPU) && len(in.Compilers) > 0 && (in.NoToolchain.Measured || in.NoToolchain.Err != "") {
		if k, t := noToolchainGate(in.NoToolchain); k != "" {
			fail(k, t)
		}
	}
	if len(in.PortMissing) > 0 {
		fail("port_incomplete", fmt.Sprintf(gatePortFmt, len(in.PortMissing), len(in.Req.Symbols), strings.Join(in.PortMissing, "\n")))
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

// isaInsn: for each ISA flag the kernel can attest, an instruction (AT&T, objdump -d) that
// only exists in code built with it.
var isaInsn = map[string]*regexp.Regexp{
	"-mavx2":       regexp.MustCompile(`%ymm[0-9]+`),
	"-mavx512f":    regexp.MustCompile(`%zmm[0-9]+`),
	"-mavx512vnni": regexp.MustCompile(`\bvpdpbusds?\b|\bvpdpwssds?\b`),
	"-mavx512vbmi": regexp.MustCompile(`\bvpermb\b|\bvperm[it]2b\b|\bvpmultishiftqb\b`),
	"-mfma":        regexp.MustCompile(`\bvf(n?m(add|sub))(132|213|231)[ps][sd]\b`),
	"-mf16c":       regexp.MustCompile(`\bvcvtph2ps\b|\bvcvtps2ph\b`),
}

func attestableISA(flags []string) []string {
	var out []string
	for _, f := range flags {
		if isaInsn[f] != nil {
			out = append(out, f)
		}
	}
	return out
}

var (
	compilerCmdRe = regexp.MustCompile(`(^|/)(g\+\+|c\+\+|clang\+\+|gcc|clang|cc|nvcc)(-[0-9.]+)?(\s|$)`)
	outputFlagRe  = regexp.MustCompile(`(?:^|\s)-o\s*(\S+)`)
	systemBinRe   = regexp.MustCompile(`^/(usr|bin|sbin|lib|lib64|opt|etc)/`)
	objdumpFn     = func(file string) (string, error) {
		b, err := exec.Command("objdump", "-d", "--no-show-raw-insn", file).Output()
		return string(b), err
	}
	isaObjdumpMax = 24 // binaries disassembled per unit
)

// isaBinaries: the outputs the build's compiler command lines named (-o …) that exist, and
// the non-system executables the test ran.
func isaBinaries(build, test ProcTrace) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] || systemBinRe.MatchString(p) {
			return
		}
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, c := range build.Cmdlines {
		if !compilerCmdRe.MatchString(c) {
			continue
		}
		for _, m := range outputFlagRe.FindAllStringSubmatch(c, -1) {
			add(m[1])
		}
	}
	for _, e := range test.Exes {
		add(e)
	}
	if len(out) > isaObjdumpMax {
		out = out[:isaObjdumpMax]
	}
	return out
}

// isaFound disassembles the binaries and reports which flags' instructions appear.
func isaFound(bins, need []string) (map[string]bool, error) {
	found := map[string]bool{}
	if len(bins) == 0 {
		return found, nil
	}
	ok := 0
	var lastErr error
	for _, b := range bins {
		asm, err := objdumpFn(b)
		if err != nil {
			lastErr = err
			continue
		}
		ok++
		for _, f := range need {
			if !found[f] && isaInsn[f].MatchString(asm) {
				found[f] = true
			}
		}
	}
	if ok == 0 {
		return found, fmt.Errorf("objdump on %d binaries: %v", len(bins), lastErr)
	}
	return found, nil
}

func isaSourceList(req UnitRequirements) string {
	var fs []string
	for f := range req.FlagFiles {
		fs = append(fs, f)
	}
	if len(fs) == 0 {
		return "its sources"
	}
	sort.Strings(fs)
	return strings.Join(fs, ", ")
}

// toolchainRan: a process of the build ran the GPU toolchain — its executable lives in
// toolchainDirs, its command line starts there, or it is a CUDA compile (which on this machine
// only the toolchain can do; /proc's exe of a compiler driver may be a resolved symlink).
func toolchainRan(t ProcTrace) bool {
	if ranFrom(t.Exes, toolchainDirs) {
		return true
	}
	for _, c := range t.Cmdlines {
		f := strings.Fields(c)
		if len(f) == 0 {
			continue
		}
		if ranFrom(f[:1], toolchainDirs) || cudaCompileRe.MatchString(c) {
			return true
		}
	}
	return false
}

// ranFrom: some program in exes lives in one of dirs.
func ranFrom(exes, dirs []string) bool {
	for _, e := range exes {
		for _, d := range dirs {
			if strings.HasPrefix(e, strings.TrimRight(d, "/")+"/") {
				return true
			}
		}
	}
	return false
}

// cuhNotCompiled: the changed .cuh files no CUDA compile reached — none of the .cu files that
// include them was named by a CUDA compile command line. 11/03 "corex compile" was the
// repository's existing src/*.cu, and a unit that changed only headers passed on any of them.
func cuhNotCompiled(gpuFiles []string, includers map[string][]string, cmdlines []string) []string {
	var missing []string
	for _, f := range gpuFiles {
		if path.Ext(f) != ".cuh" {
			continue
		}
		reached := false
		for _, cu := range includers[f] {
			if len(notCudaCompiled([]string{cu}, cmdlines)) == 0 {
				reached = true
			}
		}
		if !reached {
			missing = append(missing, f+" (included by: "+strings.Join(append([]string{"none"}, includers[f]...)[min(1, len(includers[f])):], " ")+")")
		}
	}
	return missing
}
