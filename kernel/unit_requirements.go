package kernel

// unit_requirements.go — what a dev unit must produce, measured from the reference.
//
// On 2026/11/03 all 10 units passed, and none of the 152 files they changed was a .cu or
// .cuh: FusedGdnKernels, QsaDecodeAttention and PrefillGpuKernels became host headers of
// "index arithmetic", tested on the CPU. Nothing told the kernel that these modules come
// from CUDA sources, so it had nothing to hold the unit to.
//
// The link is in the theory and the reference: a module's section names the identifiers
// of its source (`gdn_step_norm_kernel`, "row_multi -> row_dot_multi", "# MODULE:
// fused_gdn_kernels"), and the reference commit says which files define them. Each
// identifier found in 1..refIdentMaxFiles files votes 1/n for each of them (test and parity
// files excluded); the files with at least half the top vote are the module's sources. A
// unit's sources are its modules' sources. A unit is a GPU unit when one of them is
// .cu/.cuh; its ISA flags are what the reference's own build gives those files
// (refPerSourceFlags).
//
// It is evidence, not certainty, so it is logged with the identifiers that decided it, and
// a module with no identifier found is reported as unmeasured rather than guessed.

import (
	"fmt"
	"log"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
)

var (
	refIdentMaxFiles = 8  // an identifier in more files says nothing about one module
	refIdentMax      = 80 // identifiers looked up per module
	refSourceExts    = []string{"*.cu", "*.cuh", "*.cpp", "*.cc", "*.c", "*.h", "*.hpp"}
	gpuSourceExts    = []string{".cu", ".cuh"}
	refNotSourceRe   = regexp.MustCompile(`(_test|_parity|_bench)\.|(^|/)(tests?|bench)/`)
	// refVendoredRe: code the reference vendors from elsewhere (third_party/ggml/ggml-common.h
	// in Strata's first commit, 78 definitions). It is not the reference's own code: a port may
	// vendor the same file, so its definitions are not a port requirement.
	refVendoredRe     = regexp.MustCompile(`(^|/)(third_party|thirdparty|3rdparty|vendor|vendored|external|extern|deps)/`)
	backtickRe        = regexp.MustCompile("`([^`\\n]{1,200})`")
	cIdentRe          = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,}`)
	mappingLineRe     = regexp.MustCompile(`->|<-|←|→`)
	moduleNameRe      = regexp.MustCompile(`#\s*MODULE:\s*([A-Za-z_][A-Za-z0-9_]*)`)
	camelOrSnakeRe    = regexp.MustCompile(`_|[a-z][A-Z]`)
	theoryFileHeadRe  = regexp.MustCompile(`(?m)^## (?:FILE|MODULE): (\S+)\n`)
	refGitFn          = func(dir string, args ...string) (string, error) { return gitDirOut(dir, args...) }
	refBareDirFmt     = "/tmp/ref_%[1]s_%[2]s.git"
	requirementLogFmt = "dev_requirements: %[1]s unit %[2]s: sources %[3]v; gpu %[4]v; isa %[5]v; unmeasured modules %[6]v; definitions to port %[7]d"
)

func gitDirOut(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"--git-dir", dir}, args...)...).Output()
	return string(out), err
}

// refBareDir is where ref_clone keeps the reference's bare clone.
func refBareDir(repo string) string {
	owner, name, _ := strings.Cut(repo, "/")
	return fmt.Sprintf(refBareDirFmt, owner, name)
}

// moduleSourceIdents: the source identifiers a module section names — backtick spans,
// mapping lines ("a -> b", "x <- y") and its # MODULE name — C-like only (snake_case or
// camelCase), since a Lean-only name will not be in the source anyway.
func moduleSourceIdents(section string) []string {
	set := map[string]bool{}
	add := func(s string) {
		for _, id := range cIdentRe.FindAllString(s, -1) {
			if camelOrSnakeRe.MatchString(id) {
				set[id] = true
			}
		}
	}
	for _, m := range backtickRe.FindAllStringSubmatch(section, -1) {
		add(m[1])
	}
	for _, l := range strings.Split(section, "\n") {
		if mappingLineRe.MatchString(l) {
			add(l)
		}
	}
	if m := moduleNameRe.FindStringSubmatch(section); m != nil {
		set[m[1]] = true
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	if len(out) > refIdentMax {
		out = out[:refIdentMax]
	}
	return out
}

// refFilesWith lists the reference's source files (tests excluded) containing ident as a word.
func refFilesWith(bare, sha, ident string) []string {
	args := append([]string{"grep", "-l", "-w", "-F", "-e", ident, sha, "--"}, refSourceExts...)
	out, err := refGitFn(bare, args...)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil // git grep exits 1 for no match
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if _, p, ok := strings.Cut(l, ":"); ok && !refNotSourceRe.MatchString(p) {
			files = append(files, p)
		}
	}
	return files
}

// moduleSources votes the reference files a module's identifiers come from.
func moduleSources(bare, sha, section string, lookup func(string) []string) (files []string, evidence map[string][]string) {
	score := map[string]float64{}
	evidence = map[string][]string{}
	for _, id := range moduleSourceIdents(section) {
		fs := lookup(id)
		if len(fs) == 0 || len(fs) > refIdentMaxFiles {
			continue
		}
		for _, f := range fs {
			score[f] += 1 / float64(len(fs))
			evidence[f] = append(evidence[f], id)
		}
	}
	top := 0.0
	for _, s := range score {
		if s > top {
			top = s
		}
	}
	for f, s := range score {
		if s >= top/2 {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files, evidence
}

// splitTheoryModules splits merged theory text into its module sections, by name.
func splitTheoryModules(theory string) map[string]string {
	out := map[string]string{}
	locs := theoryFileHeadRe.FindAllStringSubmatchIndex(theory, -1)
	for i, l := range locs {
		end := len(theory)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		name := strings.TrimSuffix(path.Base(theory[l[2]:l[3]]), ".lean")
		out[name] = theory[l[1]:end]
	}
	return out
}

// UnitRequirements is what the reference says a unit is made of.
type UnitRequirements struct {
	Sources    []string            // reference files the unit's modules come from
	GPUSources []string            // the .cu/.cuh among them
	ISAFlags   []string            // compile options the reference's build gives them (-m…)
	FlagFiles  map[string][]string // source → its options, from the reference's build files
	Unmeasured []string            // modules for which no identifier was found
	Evidence   map[string][]string // source → identifiers that put it there
	// Symbols: every top-level definition of Sources at the reference commit (ref_symbols.go);
	// the unit's port is checked against these (port_gate.go)
	Symbols []RefSymbol
	// Tests: the reference's registered tests, for the integration unit (integration.go);
	// RequireGPU: the reference builds CUDA, so they must open a GPU device
	Tests []RefTest
	// Benches: the reference's own benchmarks at the commit (eval_bench.go), for the
	// integration unit: the port has to build and run their counterparts (eval_port.go)
	Benches    []EvalBench
	RequireGPU bool
	Repo, SHA  string
	// RefRead: the reference commit could be read here, so an empty measurement means the
	// reference really holds nothing for the unit (not that it was not looked at)
	RefRead bool
	// Vendored: sources the reference vendors (refVendoredRe), not ported
	Vendored []string
}

// GPU: the unit's sources include CUDA.
func (u UnitRequirements) GPU() bool { return len(u.GPUSources) > 0 }

// measureUnitRequirements measures a unit from its theory text and the reference commit.
func measureUnitRequirements(repo, sha, unitTheory string) UnitRequirements {
	bare := refBareDir(repo)
	cache := map[string][]string{}
	lookup := func(id string) []string {
		if fs, ok := cache[id]; ok {
			return fs
		}
		fs := refFilesWith(bare, sha, id)
		cache[id] = fs
		return fs
	}
	req := UnitRequirements{FlagFiles: map[string][]string{}, Evidence: map[string][]string{}}
	if _, err := refGitFn(bare, "cat-file", "-e", sha+"^{commit}"); err == nil {
		req.RefRead = true
	}
	seen := map[string]bool{}
	mods := splitTheoryModules(unitTheory)
	names := make([]string, 0, len(mods))
	for n := range mods {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		files, ev := moduleSources(bare, sha, mods[name], lookup)
		if len(files) == 0 {
			req.Unmeasured = append(req.Unmeasured, name)
			continue
		}
		for _, f := range files {
			if !seen[f] {
				seen[f] = true
				req.Sources = append(req.Sources, f)
			}
			req.Evidence[f] = append(req.Evidence[f], ev[f]...)
		}
	}
	finishRequirements(bare, sha, &req)
	return req
}

// logUnitRequirements writes the measurement and its evidence.
func logUnitRequirements(repo, unitID string, req UnitRequirements) {
	log.Printf(requirementLogFmt, repo, unitID, req.Sources, req.GPUSources, req.ISAFlags, req.Unmeasured, len(req.Symbols))
}

const (
	reqHeadFmt     = "[UNIT REQUIREMENTS — measured from the reference %[1]s at %[2]s]\nyour modules' identifiers are defined in these reference files:\n"
	reqSourceFmt   = "  %[1]s%[2]s%[3]s\n"
	reqGPUTag      = " (CUDA)"
	reqFlagsFmt    = " built by the reference with %[1]s"
	reqGPUCheckFmt = "the kernel checks: your changes include a .cu/.cuh; build.sh compiles each .cu you write with the CUDA toolchain in %[1]s; a process of test.sh opens a GPU device (%[2]s).\n"
	reqISACheckFmt = "the kernel checks: the binaries build.sh produces (-o) or test.sh runs contain the instructions of %[1]s (EINSTEINOS_ISA_FLAGS holds them).\n"
	reqUnmeasFmt   = "no reference file was found for: %[1]s\n"
)

// Block is the [UNIT REQUIREMENTS] section of the unit's prompt ("" when nothing was measured).
func (u UnitRequirements) Block(repo, sha string) string {
	if len(u.Sources) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf(reqHeadFmt, repo, shortSHA12(sha)))
	for _, f := range u.Sources {
		gpu, flags := "", ""
		for _, g := range u.GPUSources {
			if g == f {
				gpu = reqGPUTag
			}
		}
		if fl := u.FlagFiles[f]; len(fl) > 0 {
			flags = fmt.Sprintf(reqFlagsFmt, strings.Join(fl, " "))
		}
		b.WriteString(fmt.Sprintf(reqSourceFmt, f, gpu, flags))
	}
	if u.GPU() {
		b.WriteString(fmt.Sprintf(reqGPUCheckFmt, strings.Join(toolchainDirs, ":"), gpuDeviceRe().String()))
	}
	if len(u.ISAFlags) > 0 {
		b.WriteString(fmt.Sprintf(reqISACheckFmt, strings.Join(u.ISAFlags, " ")))
	}
	if len(u.Unmeasured) > 0 {
		b.WriteString(fmt.Sprintf(reqUnmeasFmt, strings.Join(u.Unmeasured, ", ")))
	}
	b.WriteString("\n")
	return b.String()
}

// Status is what the reviewer reads next to the kernel's measurements.
func (u UnitRequirements) Status() map[string]string {
	st := map[string]string{}
	if len(u.Sources) > 0 {
		st["reference_sources"] = strings.Join(u.Sources, " ")
	}
	if u.GPU() {
		st["reference_gpu_sources"] = strings.Join(u.GPUSources, " ")
	}
	if len(u.ISAFlags) > 0 {
		st["reference_isa_flags"] = strings.Join(u.ISAFlags, " ")
	}
	return st
}

// finishRequirements measures, for req.Sources, their definitions, the CUDA files among them
// and the compile options the reference's build gives them.
func finishRequirements(bare, sha string, req *UnitRequirements) {
	var own []string
	for _, f := range req.Sources {
		if refVendoredRe.MatchString(f) {
			req.Vendored = append(req.Vendored, f)
			continue
		}
		own = append(own, f)
	}
	req.Sources = own
	sort.Strings(req.Sources)
	for _, f := range req.Sources {
		if text, err := refGitFn(bare, "show", sha+":"+f); err == nil {
			req.Symbols = append(req.Symbols, SymbolsOf(f, text)...)
		}
	}
	flags := refPerSourceFlags(bare, sha)
	isa := map[string]bool{}
	for _, f := range req.Sources {
		for _, ext := range gpuSourceExts {
			if strings.HasSuffix(f, ext) {
				req.GPUSources = append(req.GPUSources, f)
			}
		}
		if fl := flags[f]; len(fl) > 0 {
			req.FlagFiles[f] = fl
			for _, x := range fl {
				isa[x] = true
			}
		}
	}
	for x := range isa {
		req.ISAFlags = append(req.ISAFlags, x)
	}
	sort.Strings(req.ISAFlags)
}

// requirementsForSources measures a unit whose reference files are given (a port unit) rather
// than found from theory identifiers.
func requirementsForSources(repo, sha string, sources []string) UnitRequirements {
	req := UnitRequirements{Sources: append([]string(nil), sources...), FlagFiles: map[string][]string{}, Evidence: map[string][]string{}}
	if _, err := refGitFn(refBareDir(repo), "cat-file", "-e", sha+"^{commit}"); err == nil {
		req.RefRead = true
	}
	finishRequirements(refBareDir(repo), sha, &req)
	return req
}

// portSourceRe: the files of a reference commit that are code to port (tests, parity checks
// and benchmarks excluded by refNotSourceRe).
var portSourceRe = regexp.MustCompile(`\.(cu|cuh|cpp|cc|c|h|hpp|hh|inc|py)$`)

// refPortSources lists every file of the reference commit that is code to port.
func refPortSources(repo, sha string) []string {
	out, err := refGitFn(refBareDir(repo), "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return nil
	}
	var files []string
	for _, f := range strings.Split(out, "\n") {
		f = strings.TrimSpace(f)
		if f != "" && portSourceRe.MatchString(f) && !refNotSourceRe.MatchString(f) && !refVendoredRe.MatchString(f) {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}
