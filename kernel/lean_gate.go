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
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(f)))
		if err != nil {
			continue // removed by the unit
		}
		text := string(b)
		if ContainsSorry(text) {
			return "lean_unproved", fmt.Sprintf(gateLeanSorryFmt, f)
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
			return GateLeanUnchecked, fmt.Sprintf(gateLeanUncheckedFmt, f, why)
		case !ok:
			return "lean_unproved", fmt.Sprintf(gateLeanUnprovedFmt, f, leanCheckEnvironment, msg)
		}
	}
	return "", ""
}
