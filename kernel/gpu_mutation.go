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
	"runtime"
	"sort"
	"strings"
	"sync"
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
//
// The copies are built at the same time, at most mutationBuildParallel at once, and tested
// one after another: a test is never run beside another (the GPUs are shared, and a test that
// fails for that would read as checking its kernel). A build that fails among others is built
// again alone before its kernel is called inconclusive. 11/14 unit u41a85163f4's six copies
// were built and tested in turn, each build.sh about 6.5 minutes: every verification that
// reached this gate took 1h0m28s, four of them that day; uaa158d17c5's two took 26m.
func resultUnchecked(ws, build, test, env string, files map[string][]string, buildTimeout, testTimeout time.Duration) (unchecked, inconclusive []string) {
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) > mutationMaxKernels {
		names = names[:mutationMaxKernels]
	}
	type mutant struct {
		dir, why string // why: set when the copy cannot be tested
	}
	muts := make([]mutant, len(names))
	defer func() {
		for _, m := range muts {
			if m.dir != "" {
				os.RemoveAll(m.dir)
			}
		}
	}()
	buildOne := func(dir string) error {
		code, _, err := devVerifyRunner(env+"bash "+build+" 2>&1", dir, buildTimeout)
		if err == nil && code != 0 {
			err = fmt.Errorf("build.sh exited %d with the body skipped", code)
		}
		return err
	}
	// make every copy
	for i, k := range names {
		dir, err := os.MkdirTemp("", "einsteinos_mutant_")
		if err != nil {
			muts[i].why = err.Error()
			continue
		}
		muts[i].dir = dir
		if err := copyTree(ws, dir); err != nil {
			muts[i].why = err.Error()
			continue
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
			muts[i].why = "no body found to skip"
		}
	}
	// build them at the same time
	var wg sync.WaitGroup
	slots := make(chan struct{}, mutationBuildParallel(len(names)))
	failed := make([]bool, len(names))
	for i := range muts {
		if muts[i].why != "" {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			failed[i] = buildOne(muts[i].dir) != nil
		}(i)
	}
	wg.Wait()
	// a build that failed among others is built again alone, then each copy is tested in turn
	for i, k := range names {
		if muts[i].why == "" && failed[i] {
			if err := buildOne(muts[i].dir); err != nil {
				muts[i].why = err.Error()
			}
		}
		if muts[i].why != "" {
			inconclusive = append(inconclusive, k+" ("+muts[i].why+")")
			continue
		}
		code, _, err := devVerifyRunner(env+"bash "+test+" 2>&1", muts[i].dir, testTimeout)
		if err == nil && code == 0 {
			unchecked = append(unchecked, k)
		}
	}
	return unchecked, inconclusive
}

// mutationCPUs: this machine's CPUs (a variable for tests)
var mutationCPUs = runtime.NumCPU

// mutationBuildParallel: how many copies are built at once — one per 8 of this machine's
// CPUs, at least one, at most n.
func mutationBuildParallel(n int) int {
	p := mutationCPUs() / 8
	if p < 1 {
		p = 1
	}
	if p > n {
		p = n
	}
	if p < 1 {
		p = 1
	}
	return p
}
