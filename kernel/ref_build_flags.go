package kernel

// ref_build_flags.go — how the reference itself compiles each source file.
//
// Strata does not build its SIMD kernels with the project's common flags: CMakeLists.txt
// gives them per source, e.g.
//
//	set_source_files_properties(src/kernels/cpu/expert.cpp
//	    PROPERTIES COMPILE_OPTIONS "-mavx512f;-mavx512bw;-mavx512vl;-mavx512dq;-mavx512vnni;-mavx512vbmi;-mf16c;-mfma")
//	set_source_files_properties(src/kernels/cpu/q2_avx2.cpp PROPERTIES COMPILE_OPTIONS "-mavx2;-mfma;-mf16c")
//
// (with an MSVC branch spelling them /arch:…). The 2026/11/03 units compiled everything
// with "-std=c++17 -O2 -Wall -Wextra -ffp-contract=off": the AVX2 path printed "not
// compiled in this build" and the VNNI kernel was an integer emulation, and both passed.
// This file reads those per-source options from the reference commit, so the kernel knows
// what "built the way Strata builds it" means for a unit's sources.

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

var (
	refBuildFileRe   = regexp.MustCompile(`(^|/)(CMakeLists\.txt|[^/]+\.cmake)$`)
	setSourcePropsRe = regexp.MustCompile(`(?s)set_source_files_properties\s*\(([^()]*?)\bPROPERTIES\s+COMPILE_OPTIONS\s+"([^"]*)"`)
	gccISAFlagRe     = regexp.MustCompile(`^-m[a-z0-9][a-z0-9-]*$|^-march=\S+$`)
)

// refPerSourceFlags maps each reference source file to the GCC/Clang -m… options its build
// files give it at sha (MSVC /arch spellings ignored). Paths are relative to the repo root.
func refPerSourceFlags(bare, sha string) map[string][]string {
	out := map[string][]string{}
	list, err := refGitFn(bare, "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return out
	}
	for _, f := range strings.Split(strings.TrimSpace(list), "\n") {
		if !refBuildFileRe.MatchString(f) {
			continue
		}
		text, err := refGitFn(bare, "show", sha+":"+f)
		if err != nil {
			continue
		}
		mergeFlags(out, parseSourceFlags(text, path.Dir(f)))
	}
	return out
}

// parseSourceFlags reads set_source_files_properties(<files> PROPERTIES COMPILE_OPTIONS "<a;b>")
// from one build file; relative file names are taken from the build file's directory.
func parseSourceFlags(text, dir string) map[string][]string {
	out := map[string][]string{}
	for _, m := range setSourcePropsRe.FindAllStringSubmatch(text, -1) {
		var flags []string
		for _, o := range strings.Split(m[2], ";") {
			if o = strings.TrimSpace(o); gccISAFlagRe.MatchString(o) {
				flags = append(flags, o)
			}
		}
		if len(flags) == 0 {
			continue
		}
		for _, f := range strings.Fields(m[1]) {
			if strings.HasPrefix(f, "${") || strings.HasPrefix(f, "#") {
				continue
			}
			if dir != "." && dir != "" && !strings.HasPrefix(f, "/") {
				f = path.Join(dir, f)
			}
			out[f] = append(out[f], flags...)
		}
	}
	return out
}

func mergeFlags(dst, src map[string][]string) {
	for f, fl := range src {
		set := map[string]bool{}
		for _, x := range append(dst[f], fl...) {
			set[x] = true
		}
		dst[f] = dst[f][:0]
		for x := range set {
			dst[f] = append(dst[f], x)
		}
		sort.Strings(dst[f])
	}
}
