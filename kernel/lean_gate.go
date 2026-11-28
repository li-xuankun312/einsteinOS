package kernel

// lean_gate.go — a unit's Lean modules must check in Lean before the unit passes.
//
// The kernel sends each .lean file the unit changed to Axle (the verifier the kernel itself
// holds, SetAxleVerifier), as the content of a proof of a trivial statement appended to it:
// Axle elaborates the whole file, so a definition that does not elaborate, or a proof that
// does not prove its statement, fails the file. A file that holds sorry fails as well.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// GateLeanUnchecked: Axle could not be asked or gave no usable answer — not the unit's
	// code, so the run stops on it (gateIsMachine)
	GateLeanUnchecked    = "lean_unchecked"
	gateLeanUnprovedFmt  = "lean_unproved: %[1]s does not check in Lean (Axle, %[2]s): %[3]s"
	gateLeanSorryFmt     = "lean_unproved: %[1]s holds sorry"
	gateLeanUncheckedFmt = GateLeanUnchecked + ": %[1]s could not be checked in Lean: %[2]s"
	leanCheckName        = "einsteinos_lean_check"
	leanCheckEnvironment = "lean-4.35.0"
)

// leanVerifyFn sends one verify_proof payload to Axle and returns its raw answer ("" for
// none); a variable for tests.
var leanVerifyFn = func(payload string) string {
	axleMu.Lock()
	f := axleVerifier
	axleMu.Unlock()
	if f == nil {
		return ""
	}
	return f(payload)
}

// leanGate checks every .lean file among the unit's changed files. 11/14 19:25 unit
// ub848524604 was merged with lean/ub848/MtpDraftChain.lean's carve ending in "carve (base +
// bump n) ns" after its pattern had been renamed to "n :: rest", and runProd and pick in
// SpecDecodeController.lean likewise — a script renaming parameters to get past lean_copied
// left names that no longer exist, nothing in the verification elaborated the files, and
// only the review (19:30) found them. 11/14 08:41 unit u41a85163f4's review found
// NgramPleHash.lean and NativePlePostops.lean failing in Axle after the unit had passed.
func leanGate(ws string, changed []string) (kind, text string) {
	var files []string
	for _, f := range changed {
		if strings.HasSuffix(f, ".lean") {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	// every file is checked and every failure kept: 11/17 09:08:45 the check stopped at
	// PrefillOrchestration.lean (the third of four) and ServeEngine.lean was not looked at, so
	// dev_unit_8 learned of its files' errors one verification (2m40s) at a time
	var failed, unchecked []string
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(f)))
		if err != nil {
			continue // removed by the unit
		}
		text := string(b)
		if ContainsSorry(text) {
			failed = append(failed, fmt.Sprintf(gateLeanSorryFmt, f))
			continue
		}
		statement := "theorem " + leanCheckName + " : True := sorry"
		content := text + "\n\ntheorem " + leanCheckName + " : True := trivial\n"
		payload := axlePayload(statement, content)
		resp := ""
		for try := 0; try < 2 && strings.TrimSpace(resp) == ""; try++ {
			resp = leanVerifyFn(payload)
		}
		ok, msg, usable := AxleVerdict(resp)
		switch {
		case !usable:
			why := "no answer from Axle"
			if strings.TrimSpace(resp) != "" {
				why = "Axle's answer was not a verification result: " + tailText(resp, 300)
			}
			unchecked = append(unchecked, fmt.Sprintf(gateLeanUncheckedFmt, f, why))
		case !ok:
			// every error Axle answered, each on its own line: 11/17 dev_unit_8 was told only the
			// first of a file's errors and found the next one at the next verification
			if errs := AxleErrors(resp); len(errs) > 1 {
				msg = strings.Join(errs, "\n")
			}
			failed = append(failed, fmt.Sprintf(gateLeanUnprovedFmt, f, leanCheckEnvironment, msg))
		}
	}
	// what the unit wrote decides first; a file Axle could not answer on is the machine's
	if len(failed) > 0 {
		return "lean_unproved", strings.Join(failed, "\n")
	}
	if len(unchecked) > 0 {
		return GateLeanUnchecked, strings.Join(unchecked, "\n")
	}
	return "", ""
}

var (
	leanTheoremRe = regexp.MustCompile(`(?m)^\s*(?:theorem|lemma|example)\s+([^\s:({\[]*)\s*([^:]*?)\s*:\s*([^\n]*?)\s*:=`)
	// a statement of numerals and arithmetic alone
	leanConstantRe = regexp.MustCompile(`^[0-9\s+\-*/%^()=<>≤≥≠]+$`)
)

const (
	leanConstantHead = "\n[LEAN THEOREMS THIS ATTEMPT ADDED THAT STATE NUMERALS ALONE — measured from the mirror; no definition of the port is named in them]\n"
	// %[1]s the file, %[2]s the theorem, %[3]s its statement
	leanConstantLineFmt = "%[1]s %[2]s : %[3]s\n"
)

// leanConstantTheorems: the theorems of the delta's .lean files, not in the branch the unit
// started from (tip), whose statement — with no binders — is numerals and arithmetic alone.
// 11/28 the part embed (7a052aa6) added theorem gatherPerRow2048 : 2048 / 8 = 256 := by decide to
// lean/Baloon/PrefillKernels.lean for the sentence of embed_parity.cu on the model's geometry.
func leanConstantTheorems(tip, mirror string, changed []string) string {
	var lines []string
	for _, f := range changed {
		if !strings.HasSuffix(f, ".lean") {
			continue
		}
		now, err := os.ReadFile(filepath.Join(mirror, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		then, _ := os.ReadFile(filepath.Join(tip, filepath.FromSlash(f)))
		had := map[string]bool{}
		for _, m := range leanTheoremRe.FindAllStringSubmatch(string(then), -1) {
			had[m[1]+"\x00"+m[3]] = true
		}
		for _, m := range leanTheoremRe.FindAllStringSubmatch(string(now), -1) {
			if strings.TrimSpace(m[2]) == "" && leanConstantRe.MatchString(m[3]) && !had[m[1]+"\x00"+m[3]] {
				lines = append(lines, fmt.Sprintf(leanConstantLineFmt, f, m[1], m[3]))
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	sort.Strings(lines)
	return leanConstantHead + strings.Join(lines, "")
}
