package kernel

// gpu_kernels.go — which GPU kernels a unit's CUDA files define.
//
// 11/04: modules named after GPU kernels (FusedGdnKernels, QsaDecodeAttention,
// PrefillGpuKernels) were written as host headers of "index arithmetic … of the … CUDA
// kernels" — a .cu/.cuh file with no kernel in it passes "wrote a CUDA file". A kernel is a
// __global__ function; this is what the unit's CUDA files are measured for.

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// launchBoundsRe: __launch_bounds__(…) sits between __global__ and the name
	launchBoundsRe = regexp.MustCompile(`__launch_bounds__\s*\([^)]*\)`)
	// globalKernelRe: __global__, then the return type and qualifiers, then the name and "("
	globalKernelRe = regexp.MustCompile(`\b__global__\b[^;{}()]*?\b([A-Za-z_]\w*)\s*\(`)
	lineCommentRe  = regexp.MustCompile(`//[^\n]*`)
	blockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// kernelNamesIn returns the __global__ functions defined or declared in CUDA source text,
// sorted and without duplicates. Comments are ignored: a comment that mentions __global__
// is not a kernel.
func kernelNamesIn(src string) []string {
	src = blockCommentRe.ReplaceAllString(src, " ")
	src = lineCommentRe.ReplaceAllString(src, " ")
	src = launchBoundsRe.ReplaceAllString(src, " ")
	seen := map[string]bool{}
	var out []string
	for _, m := range globalKernelRe.FindAllStringSubmatch(src, -1) {
		name := m[1]
		if name == "void" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// unitKernels reads the unit's changed CUDA files in ws and returns, per file, the kernels
// it defines. Files that cannot be read are absent from the map.
func unitKernels(ws string, changed []string) map[string][]string {
	out := map[string][]string{}
	for _, f := range changedGPUFiles(changed) {
		b, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		out[f] = kernelNamesIn(string(b))
	}
	return out
}

// kernelSet flattens unitKernels' map to the set of kernel names.
func kernelSet(perFile map[string][]string) map[string]bool {
	set := map[string]bool{}
	for _, names := range perFile {
		for _, n := range names {
			set[n] = true
		}
	}
	return set
}

// kernelFilesText lists "file (n kernels)" for the failure text.
func kernelFilesText(perFile map[string][]string) string {
	var files []string
	for f := range perFile {
		files = append(files, f)
	}
	sort.Strings(files)
	var parts []string
	for _, f := range files {
		parts = append(parts, f+" ("+strings.Join(perFile[f], " ")+")")
	}
	return strings.Join(parts, ", ")
}

// repoKernels: every kernel defined in the CUDA files of the working tree ws (not under
// .git or a build directory) — what the integration unit's tests have to choose from.
func repoKernels(ws string) map[string]bool {
	set := map[string]bool{}
	_ = filepath.WalkDir(ws, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "build", "_build", "cmake-build", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(p)
		if ext != ".cu" && ext != ".cuh" {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil {
			for _, n := range kernelNamesIn(string(b)) {
				set[n] = true
			}
		}
		return nil
	})
	return set
}

// cuhIncluders: for each changed .cuh, the .cu files of ws whose #include names it.
func cuhIncluders(ws string, changed []string) map[string][]string {
	out := map[string][]string{}
	var cuh []string
	for _, f := range changedGPUFiles(changed) {
		if path.Ext(f) == ".cuh" {
			cuh = append(cuh, f)
			out[f] = nil
		}
	}
	if len(cuh) == 0 {
		return out
	}
	filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Ext(p) != ".cu" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(ws, p)
		for _, m := range cudaIncludeRe.FindAllStringSubmatch(string(b), -1) {
			for _, h := range cuh {
				if path.Base(m[1]) == path.Base(h) {
					out[h] = append(out[h], filepath.ToSlash(rel))
				}
			}
		}
		return nil
	})
	return out
}

var cudaIncludeRe = regexp.MustCompile(`(?m)^[ \t]*#[ \t]*include[ \t]*["<]([^">]+)[">]`)
