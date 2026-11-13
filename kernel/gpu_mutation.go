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
	"regexp"
	"strings"
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
