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
	// ISAFuncs: per ISA flag, the names the unit's port map gives to the counterparts of the
	// reference definitions in files built with that flag (isaPortFuncs)
	ISAFuncs map[string][]string
	// PortKernels: the kernels the unit's port map names as counterparts of reference kernels
	// (portKernels); empty when it names none
	PortKernels map[string]bool
	// UnitID: the unit verified (its port map is PortMapPath(UnitID))
	UnitID string
	// PortFiles: the files the unit's port map writes its counterparts into
	PortFiles map[string]bool
	// Shadowed: kernels the port map names that a test file defines too (shadowedKernels)
	Shadowed []string
	// PortRefFiles: port map lines whose reference file is not one of the unit's
	// (portRefFiles)
	PortRefFiles []string
	// PortExtra: port map lines that name no definition the unit ports (portExtra)
	PortExtra []string
	// PortForeign: port map lines whose counterpart is a name the reference file does not
	// define (portForeignNames)
	PortForeign []string
	// PortScripts: the unit's verify files that read a port map (scriptsReadingPortMap)
	PortScripts []string
	// LeanCopied: the declarations of the unit's changed .lean files whose text is the
	// theory's (lean_copy.go), one "file: kind name" each
	LeanCopied []string
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
	gateISAWhereFmt            = "; looked for inside the unit's counterparts of the definitions of those files: %[1]s"
	gateISAUnmeasuredFmt       = "isa_unmeasured: the reference builds %[1]s with %[2]s, and the binaries could not be disassembled (%[3]s)"
	gateToolchainUnusedFmt     = "toolchain_unused: the unit is GPU work but no process of build.sh ran a program from %[1]s (programs that ran: %[2]s) — a build that skips the toolchain proves nothing"
	gateKernelUnportedFmt      = "kernel_unported: %[1]s define __global__ kernels (%[2]s) and are neither test files nor files the unit's port map %[3]s writes a counterpart into"
	gateGPUKernelMissingFmt    = "gpu_kernel_missing: the unit's modules come from CUDA sources in the reference (%[1]s) but its CUDA files define no __global__ kernel: %[2]s — a host header about a kernel is not the kernel; kernels its port map %[3]s names: %[4]d; kernels its test launched: %[5]s"
	gateKernelNotLaunchedFmt   = "gpu_kernel_not_launched: test.sh exited 0 but launched none of the unit's kernels (%[1]s); kernels its processes launched: %[2]s; executables the launch probe was in: %[3]s — a test that opens the device and checks on the CPU proves nothing about the kernels"
	gateLaunchUnmeasuredFmt    = "gpu_launch_unmeasured: whether the unit's kernels ran on the device could not be measured: %[1]s"
	gateIntegrationNoKernelFmt = "integration_kernel_not_launched: the reference builds CUDA and its %[1]d registered tests passed here, but none of them launched a kernel of this repository (it defines %[2]d: %[3]s); kernels launched: %[4]s"
	gateKernelShadowedFmt      = "kernel_shadowed_in_test: %[1]s define a __global__ kernel of the same name as a counterpart in the unit's port map %[2]s. The launch probe reads a launch by name, so a test that launches its own copy proves nothing about the ported kernel: remove these definitions and call the ported kernel (or its launcher) from the test instead"
	gatePortRefFileFmt         = "port_ref_file_unknown: %[1]d reference file(s) in %[2]s are not files of the reference the kernel measured — that column is the reference's, and a header written in this repository is not one of its files, whatever it is called:\n%[3]s\nthe unit's reference files: %[4]s"
	gatePortExtraFmt           = "port_extra: %[1]d line(s) of %[2]s name no definition this unit ports — the map holds exactly the %[3]d the kernel measured from the reference:\n%[4]s"
	gatePortForeignFmt         = "port_name_foreign: %[1]d line(s) of %[2]s give a counterpart the reference file does not define — a port keeps the reference's names:\n%[3]s"
	gatePortScriptFmt          = "port_count_in_script: %[1]s read the port map — the kernel checks %[2]s against the %[3]d definitions it measured from the reference, and a count a script checks is the conversation's own number (remove the port check from the scripts; they test the code)"
	gateResultUncheckedFmt     = "gpu_result_unchecked: %[1]s still exits 0 with the body of %[2]s skipped (rebuilt so each returns at once): the test checks that the kernel ran, not what it computed — compare its outputs with what the reference computes for the same input"
	gateLeanCopiedFmt          = "lean_copied: %[1]d declaration(s) of the unit's Lean are the theory's own text, not the unit's (.einsteinOS/theory.md holds each, comments and white space aside):\n%[2]s"
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

// GateNothingCompared: the integration's run was compared with nothing outside the pipeline
// (eval_integration.go). What it would be compared with — the reference's runs here, the
// previous integration, searched repositories, published numbers — is not the unit's code, so
// a conversation cannot change it either.
const GateNothingCompared = "integration_nothing_compared"

// gateOutsideUnit: a gate no conversation on the unit can change — the machine's, or the
// integration having nothing outside the pipeline to be judged against. The conversation is
// let end and the run stops (dev_run), as for a machine gate.
func gateOutsideUnit(kind string) bool { return gateIsMachine(kind) || kind == GateNothingCompared }

// preverifyMayEnd: whether a done line ends the conversation — the unit passed, or what failed
// is the machine.
func preverifyMayEnd(passed bool, gate string) bool { return passed || gateOutsideUnit(gate) }

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
	// a kernel in a file outside the tests that no line of the port map writes into ports nothing:
	// 11/09 15:32 unit ucc74f228b9 added src/kernels/cuda/ucc7_geometry.cu and 11/10 unit
	// u61716a9a8a src/kernels/cuda/verify_commit_dev.cu, each to pass gpu_kernel_missing
	if in.PortFiles != nil {
		var files, names []string
		for f, ks := range in.Kernels {
			if len(ks) > 0 && !isTestPath(f) && !in.PortFiles[f] {
				files = append(files, f)
				names = append(names, ks...)
			}
		}
		if len(files) > 0 {
			sort.Strings(files)
			sort.Strings(names)
			fail("kernel_unported", fmt.Sprintf(gateKernelUnportedFmt, strings.Join(files, ", "), shortList(names, 12), PortMapPath(in.UnitID)))
		}
	}
	// …unless its port map names __global__ kernels as its counterparts of the reference's: 11/09
	// 15:31 unit ucc74f228b9's port map named flash_kernel, qsa_attend_kernel and the rest, in
	// files the branch already held, and the gate sent it back until it wrote three new kernels
	// (geometry_kernel, order_key_kernel, quota_kernel) of its own
	if in.Req.GPU() && in.KernelsMeasured && len(in.Kernels) > 0 && len(kernelSet(in.Kernels)) == 0 && len(in.PortKernels) == 0 {
		// the measured facts that tell a port written into an existing file from no port at all:
		// 11/10 02:07 unit u61716a9a8a was told only that its CUDA files held no kernel, while
		// its test launched dense_steps_kernel, mtp_select_kernel and nine more of an existing
		// file; it wrote two new kernels instead of naming those in its port map
		fail("gpu_kernel_missing", fmt.Sprintf(gateGPUKernelMissingFmt, strings.Join(in.Req.GPUSources, ", "), kernelFilesText(in.Kernels),
			PortMapPath(in.UnitID), len(in.PortKernels), shortList(sortedKeys(launchedNames(in.Test.Launches)), 16)))
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
		// the unit's own kernels are its ports of the reference's kernels when its port map names
		// any: 11/09 unit ube4f27df84 passed with a partition_kernel of its own test file, launched
		// to satisfy this gate, and its review failed it for that
		own := kernelSet(in.Kernels)
		if len(in.PortKernels) > 0 {
			own = in.PortKernels
		}
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
		bins := isaBinaries(in.Build, in.Test, in.Changed)
		found, err := isaFoundIn(bins, need, in.ISAFuncs)
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
				text := fmt.Sprintf(gateISAMissingFmt, isaSourceList(in.Req), strings.Join(need, " "), strings.Join(missing, ", "), shortList(bins, 6))
				var where []string
				for _, f := range need {
					where = append(where, in.ISAFuncs[f]...)
				}
				if len(where) > 0 {
					text += fmt.Sprintf(gateISAWhereFmt, shortList(dedupStrings(sortedCopy(where)), 12))
				}
				fail("isa_path_missing", text)
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
	if len(in.LeanCopied) > 0 {
		fail("lean_copied", fmt.Sprintf(gateLeanCopiedFmt, len(in.LeanCopied), linesList(in.LeanCopied, 40)))
	}
	if len(in.PortScripts) > 0 {
		fail("port_count_in_script", fmt.Sprintf(gatePortScriptFmt, strings.Join(in.PortScripts, ", "), PortMapPath(in.UnitID), len(in.Req.Symbols)))
	}
	if len(in.Shadowed) > 0 {
		fail("kernel_shadowed_in_test", fmt.Sprintf(gateKernelShadowedFmt, strings.Join(in.Shadowed, ", "), PortMapPath(in.UnitID)))
	}
	if len(in.PortRefFiles) > 0 {
		fail("port_ref_file_unknown", fmt.Sprintf(gatePortRefFileFmt, len(in.PortRefFiles), PortMapPath(in.UnitID), strings.Join(in.PortRefFiles, "\n"), shortList(sortedCopy(refFilesOf(in.Req.Symbols)), 12)))
	}
	if len(in.PortExtra) > 0 {
		fail("port_extra", fmt.Sprintf(gatePortExtraFmt, len(in.PortExtra), PortMapPath(in.UnitID), len(in.Req.Symbols), strings.Join(in.PortExtra, "\n")))
	}
	if len(in.PortForeign) > 0 {
		fail("port_name_foreign", fmt.Sprintf(gatePortForeignFmt, len(in.PortForeign), PortMapPath(in.UnitID), strings.Join(in.PortForeign, "\n")))
	}
	if len(in.PortMissing) > 0 {
		fail("port_incomplete", fmt.Sprintf(gatePortFmt, len(in.PortMissing), len(in.Req.Symbols), strings.Join(in.PortMissing, "\n")))
	}

	if len(kinds) == 0 {
		return "", ""
	}
	return kinds[0], strings.Join(texts, "\n")
}

// refFilesOf: the files the symbols come from, deduplicated.
func refFilesOf(syms []RefSymbol) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range syms {
		if !seen[s.File] {
			seen[s.File] = true
			out = append(out, s.File)
		}
	}
	return out
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
		b, err := exec.Command("objdump", "-d", "-C", "--no-show-raw-insn", file).Output()
		return string(b), err
	}
	isaObjdumpMax = 24 // binaries disassembled per unit
)

// isaBinaries: the outputs the build's compiler command lines named (-o …) that exist, and
// the non-system executables the test ran. The outputs of compiles of the unit's own changed
// files come first and are all kept; the rest fill up to isaObjdumpMax. 11/09 12:43 the build
// of unit ube4f27df84 named more binaries than the cap, its own objects were not among the
// first 24 disassembled, and its conversation renamed them "a0_…" to get them looked at.
func isaBinaries(build, test ProcTrace, changed []string) []string {
	seen := map[string]bool{}
	var own, rest []string
	add := func(p string, mine bool) {
		if p == "" || seen[p] || systemBinRe.MatchString(p) {
			return
		}
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			seen[p] = true
			if mine {
				own = append(own, p)
			} else {
				rest = append(rest, p)
			}
		}
	}
	compilesChanged := func(cmd string) bool {
		for _, f := range strings.Fields(cmd) {
			for _, c := range changed {
				if f == c || strings.HasSuffix(f, "/"+c) {
					return true
				}
			}
		}
		return false
	}
	for pass := 0; pass < 2; pass++ {
		for _, c := range build.Cmdlines {
			if !compilerCmdRe.MatchString(c) || compilesChanged(c) != (pass == 0) {
				continue
			}
			for _, m := range outputFlagRe.FindAllStringSubmatch(c, -1) {
				add(m[1], pass == 0)
			}
		}
	}
	for _, e := range test.Exes {
		add(e, false)
	}
	if room := isaObjdumpMax - len(own); room < len(rest) {
		if room < 0 {
			room = 0
		}
		rest = rest[:room]
	}
	return append(own, rest...)
}

// isaPortFuncs: per flag, the names of the unit's counterparts (its port map) of reference
// definitions whose file the reference builds with that flag.
func isaPortFuncs(pm map[string]portEntry, flagFiles map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, e := range pm {
		for _, f := range flagFiles[e.refFile] {
			if isaInsn[f] != nil && e.name != "" {
				out[f] = append(out[f], symbolBase(e.name))
			}
		}
	}
	for f := range out {
		sort.Strings(out[f])
		out[f] = dedupStrings(out[f])
	}
	return out
}

var objdumpFuncRe = regexp.MustCompile(`(?m)^[0-9a-f]+ <([^>]*)>:$`)

// asmFuncBase: the identifier of a function header objdump -C printed ("baloon::cpu::q2_rows(float
// const*, int)" → "q2_rows").
func asmFuncBase(header string) string {
	h := strings.ReplaceAll(header, "(anonymous namespace)", "anon")
	h = strings.TrimSuffix(strings.TrimSpace(h), " const")
	// the parameter list: the parenthesis that closes at the end
	if strings.HasSuffix(h, ")") {
		depth := 0
		for i := len(h) - 1; i >= 0; i-- {
			switch h[i] {
			case ')':
				depth++
			case '(':
				depth--
			}
			if depth == 0 {
				h = h[:i]
				break
			}
		}
	}
	// template arguments at the end
	if strings.HasSuffix(h, ">") {
		if i := strings.Index(h, "<"); i >= 0 {
			h = h[:i]
		}
	}
	if i := strings.LastIndex(h, "::"); i >= 0 {
		h = h[i+2:]
	}
	if i := strings.LastIndex(h, " "); i >= 0 {
		h = h[i+1:]
	}
	return strings.TrimSpace(h)
}

// asmFuncs splits a disassembly into function name → its instructions.
func asmFuncs(asm string) map[string]string {
	out := map[string]string{}
	idx := objdumpFuncRe.FindAllStringSubmatchIndex(asm, -1)
	for i, m := range idx {
		end := len(asm)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		name := asmFuncBase(asm[m[2]:m[3]])
		out[name] += asm[m[1]:end]
	}
	return out
}

// isaFoundIn reports which flags' instructions appear. A flag whose ported functions (funcs)
// are functions of the binaries is found only inside them: 11/09 unit ube4f27df84 passed
// isa_path_missing with AVX-512 code its conversation had put into five unrelated files and
// never called, which its review then failed. A flag none of whose functions is in the
// binaries (inlined, or no port map) is looked for anywhere, as before.
func isaFoundIn(bins, need []string, funcs map[string][]string) (map[string]bool, error) {
	if len(funcs) == 0 {
		return isaFound(bins, need)
	}
	found := map[string]bool{}
	if len(bins) == 0 {
		return found, nil
	}
	ok := 0
	var lastErr error
	present := map[string]bool{} // flag → one of its functions is in a binary
	whole := map[string]bool{}   // flag → its instructions are somewhere in a binary
	for _, b := range bins {
		asm, err := objdumpFn(b)
		if err != nil {
			lastErr = err
			continue
		}
		ok++
		fs := asmFuncs(asm)
		for _, f := range need {
			if isaInsn[f].MatchString(asm) {
				whole[f] = true
			}
			for _, name := range funcs[f] {
				if body, in := fs[name]; in {
					present[f] = true
					if isaInsn[f].MatchString(body) {
						found[f] = true
					}
				}
			}
		}
	}
	if ok == 0 {
		return found, fmt.Errorf("objdump on %d binaries: %v", len(bins), lastErr)
	}
	for _, f := range need {
		if !present[f] && whole[f] {
			found[f] = true
		}
	}
	return found, nil
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

func sortedCopy(xs []string) []string {
	out := append([]string{}, xs...)
	sort.Strings(out)
	return out
}

// isTestPath: a path with a directory or file name that starts with "test".
func isTestPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(strings.ToLower(seg), "test") {
			return true
		}
	}
	return false
}
