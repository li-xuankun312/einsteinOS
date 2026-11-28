package kernel

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The integration unit may change a file of the units it puts together; a kernel another unit's
// port map ports there is that unit's, verified with the definition the branch holds. 11/26 the
// integration conversation rewrote rms_norm_weighted_kernel of src/kernels/gpu/ew_ops.cu,
// pcd027bc02f's port, to sum in double through kgc::wave_sum_d; from 20:42:55 to 00:17:44 eight
// verifications failed test.sh on bf16_gemv_parity, "FAIL: rms_norm_weighted (0 vs 2.47799772)",
// and nothing said that a kernel another unit had ported differed from what that unit verified.

// %[1]d kernels, %[2]s them
const portedChangedFmt = "\n[KERNELS OTHER UNITS PORTED THAT DIFFER FROM THE BRANCH THE UNIT STARTED FROM — measured from the mirror]\n%[1]d kernel(s) another unit's port map writes are defined otherwise than on that branch: %[2]s\n"

// kernelDefinitionText: the text of name's definition in src, from its name to the brace closing
// its body ("" when src defines no body of that name).
func kernelDefinitionText(src, name string) string {
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(name) + `\s*\([^;{}]*\)\s*(?:const\s*)?(?:noexcept\s*)?\{`)
	if err != nil {
		return ""
	}
	loc := re.FindStringIndex(src)
	if loc == nil {
		return ""
	}
	depth := 0
	for i := loc[1] - 1; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return strings.TrimSpace(src[loc[0] : i+1])
			}
		}
	}
	return ""
}

// portedKernelsChanged: for the integration unit, the kernels other units' maps port whose
// definition in the mirror differs from the one on the branch the unit started from, as
// "<file> <kernel>"; branch reads the branch's file (ok false when the branch lacks it).
func portedKernelsChanged(mirror, unitID string, branch func(f string) ([]byte, bool)) []string {
	var out []string
	for f, names := range integrationOtherPorted(mirror, unitID) {
		was, ok := branch(f)
		if !ok {
			continue
		}
		now, err := os.ReadFile(filepath.Join(mirror, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		for n := range names {
			a := kernelDefinitionText(string(was), n)
			if a != "" && a != kernelDefinitionText(string(now), n) {
				out = append(out, f+" "+n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// portedChangedText: what the report says of them ("" for none).
func portedChangedText(changed []string) string {
	if len(changed) == 0 {
		return ""
	}
	return fmt.Sprintf(portedChangedFmt, len(changed), strings.Join(changed, "; "))
}
