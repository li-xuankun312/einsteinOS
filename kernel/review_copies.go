package kernel

// review_copies.go — every definition on the dev branch of what a review verdict names.
//
// 11/10 06:26 the review of unit u0063babba8 failed kv_append_q8_kernel in
// src/kernels/cuda/kv_q8.cu (quantized with the unrounded amax); the fix changed that file.
// src/ilu_kv_q8.cu defines kv_append_q8_kernel and kv_append_q8_step as well, with the same
// bug, and stayed as it was until unit p81030368e8's review named it again at 14:36. The fix
// conversation is now given, measured on the branch, every file that holds a definition with
// a body of a name the verdict says, when there is more than one.

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// reviewCopySources: the files a definition is looked for in.
var reviewCopySources = []string{"*.cu", "*.cuh", "*.cpp", "*.cc", "*.c", "*.h", "*.hpp", "*.inc", "*.py"}

// definitionCopies: for each name the verdict says that has a definition with its body in more
// than one file of branch (clone at dir), the files, sorted.
func definitionCopies(dir, branch, verdict string) map[string][]string {
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, w := range reviewIdentRe.FindAllString(verdict, -1) {
		if seen[w] || !strings.Contains(w, "_") {
			continue
		}
		seen[w] = true
		args := append([]string{"-C", dir, "grep", "-l", "-w", "-e", w, branch, "--"}, reviewCopySources...)
		ls, _ := exec.Command("git", args...).Output()
		var files []string
		for _, l := range strings.Split(strings.TrimSpace(string(ls)), "\n") {
			f := strings.TrimPrefix(l, branch+":")
			if f == "" {
				continue
			}
			src, err := exec.Command("git", "-C", dir, "show", branch+":"+f).Output()
			if err == nil && definesWithBody(string(src), w) {
				files = append(files, f)
			}
		}
		if len(files) > 1 {
			sort.Strings(files)
			out[w] = files
		}
	}
	return out
}

// reviewCopiesFmt: the measured copies, as the fix conversation reads them.
const reviewCopiesFmt = "definitions on %[1]s of names the verdict says, in more than one file (measured by the kernel):\n%[2]s"

// reviewCopiesText formats copies (empty when there are none).
func reviewCopiesText(branch string, copies map[string][]string) string {
	if len(copies) == 0 {
		return ""
	}
	names := make([]string, 0, len(copies))
	for n := range copies {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(fmt.Sprintf("  %s: %s\n", n, strings.Join(copies[n], ", ")))
	}
	return fmt.Sprintf(reviewCopiesFmt, branch, b.String())
}
