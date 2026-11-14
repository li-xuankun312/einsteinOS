package kernel

// gpu_mutation.go — whether a unit's test checks what its kernels compute.
//
// The launch probe proves a kernel ran on the device, not that it computed anything. 11/13
// unit u7663fc74b8's tests/u7663fc74b8/test_gpu.cu gave router_top10 four logits, asked
// for the top two and returned 0 whenever the launch returned cudaSuccess: the indices and
// weights (1 and 3) were never read, and gpu_kernel_not_launched passed on it. The kernel
// now builds the unit once more with each launched kernel's body skipped (it returns at
// once) and runs test.sh again: a test that still exits 0 does not check that kernel.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// kernelSkipStmt is what each skipped kernel body starts with.
const kernelSkipStmt = " return; /* einsteinOS: body skipped to see whether the test checks it */ "

var globalRe = regexp.MustCompile(`__global__\b`)

// skipKernelBodies returns src with every definition of the __global__ kernel name
// returning at its first statement, and how many definitions were changed. Declarations
// (no body) are left as they are.
func skipKernelBodies(src, name string) (string, int) {
	nameRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*\(`)
	var b strings.Builder
	last, n := 0, 0
	for _, g := range globalRe.FindAllStringIndex(src, -1) {
		if g[0] < last {
			continue
		}
		// the kernel's name before the end of the declarator (the next ';' or '{')
		head := src[g[1]:]
		stop := strings.IndexAny(head, ";{")
		if stop < 0 {
			break
		}
		m := nameRe.FindStringIndex(head[:stop])
		if m == nil {
			continue
		}
		// the parameter list, balanced (default arguments may hold parentheses)
		open := g[1] + m[1] - 1
		depth, i := 0, open
		for ; i < len(src); i++ {
			if src[i] == '(' {
				depth++
			} else if src[i] == ')' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if i >= len(src) {
			break
		}
		rest := src[i+1:]
		body := strings.IndexAny(rest, ";{")
		if body < 0 || rest[body] != '{' {
			continue // a declaration
		}
		at := i + 1 + body + 1
		b.WriteString(src[last:at])
		b.WriteString(kernelSkipStmt)
		last = at
		n++
	}
	b.WriteString(src[last:])
	return b.String(), n
}

// mutationMaxKernels: at most this many kernels are rebuilt skipped, one build and test each.
const mutationMaxKernels = 6

// kernelFiles: for each kernel the unit ports and its test launched, the files of the
// workspace that define it (its port map lines and the unit's changed CUDA files).
func kernelFiles(ws, unitID string, own map[string]bool, perFile map[string][]string, launched map[string]bool) map[string][]string {
	out := map[string][]string{}
	add := func(k, f string) {
		for _, x := range out[k] {
			if x == f {
				return
			}
		}
		out[k] = append(out[k], f)
	}
	lines, _ := readPortLines(ws, unitID)
	for _, e := range lines {
		if k := symbolBase(e.name); own[k] && launched[k] {
			add(k, e.file)
		}
	}
	for f, ks := range perFile {
		for _, k := range ks {
			if own[k] && launched[k] && !isTestPath(f) {
				add(k, f)
			}
		}
	}
	return out
}

// resultUnchecked builds and tests a copy of the workspace once per kernel with that kernel's
// body skipped. unchecked: the kernels whose test still exited 0; inconclusive: those the
// copy could not be made, changed or built for (said, not failed).
func resultUnchecked(ws, build, test, env string, files map[string][]string, buildTimeout, testTimeout time.Duration) (unchecked, inconclusive []string) {
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) > mutationMaxKernels {
		names = names[:mutationMaxKernels]
	}
	for _, k := range names {
		why := func() string {
			dir, err := os.MkdirTemp("", "einsteinos_mutant_")
			if err != nil {
				return err.Error()
			}
			defer os.RemoveAll(dir)
			if err := copyTree(ws, dir); err != nil {
				return err.Error()
			}
			changed := 0
			for _, f := range files[k] {
				p := filepath.Join(dir, filepath.FromSlash(f))
				b, err := os.ReadFile(p)
				if err != nil {
					continue
				}
				src, n := skipKernelBodies(string(b), k)
				if n > 0 && os.WriteFile(p, []byte(src), 0o644) == nil {
					changed += n
				}
			}
			if changed == 0 {
				return "no body found to skip"
			}
			if code, _, err := devVerifyRunner(env+"bash "+build+" 2>&1", dir, buildTimeout); err != nil || code != 0 {
				return fmt.Sprintf("build.sh exited %d with the body skipped", code)
			}
			code, _, err := devVerifyRunner(env+"bash "+test+" 2>&1", dir, testTimeout)
			if err == nil && code == 0 {
				return "" // unchecked
			}
			return "checked"
		}()
		switch why {
		case "":
			unchecked = append(unchecked, k)
		case "checked":
		default:
			inconclusive = append(inconclusive, k+" ("+why+")")
		}
	}
	return unchecked, inconclusive
}
