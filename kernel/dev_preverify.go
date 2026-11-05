package kernel

// dev_preverify.go — a dev conversation is told the kernel's verdict before it may end.
//
// 11/05 02:47 dev_fix_1 of u52d156705c worked five rounds against the real compiler and GPU
// and ended; the kernel's verification then failed it on port_incomplete — a gate the
// conversation never ran. At 02:55 dev_fix_2 ended on "bash build.sh | tail; bash test.sh |
// tail" exiting 0, an exit code the pipes had taken from tail. Whatever the conversation ran,
// the kernel's own verification of its tree (build, test, every gate, /proc) is what decides
// the unit; it now runs when the conversation writes its done line, and a failure goes back
// into the conversation instead of ending it. A pass is reused by the final verification when
// the tree has not changed since.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type preVerifyResult struct {
	ok     bool
	digest string
	out    UnitVerifyOutcome
}

var (
	preVerifyMu    sync.Mutex
	preVerifyHooks = map[string]func() (bool, string){}
)

// RegisterPreVerify makes fn the check run when the conversation of role writes its done line.
func RegisterPreVerify(role string, fn func() (bool, string)) {
	preVerifyMu.Lock()
	preVerifyHooks[role] = fn
	preVerifyMu.Unlock()
}

// UnregisterPreVerify removes role's check.
func UnregisterPreVerify(role string) {
	preVerifyMu.Lock()
	delete(preVerifyHooks, role)
	preVerifyMu.Unlock()
}

// PreVerify runs role's check, if one is registered: ran, passed, and the report to send back.
func PreVerify(role string) (ran, ok bool, report string) {
	preVerifyMu.Lock()
	fn := preVerifyHooks[role]
	preVerifyMu.Unlock()
	if fn == nil {
		return false, false, ""
	}
	ok, report = fn()
	return true, ok, report
}

const (
	preVerifyHeadFmt = "[KERNEL VERIFICATION — the check that decides this unit, run now on the real machine on your tree]\ncompile=%[1]s (build.sh exit %[2]d)  smoke=%[3]s (test.sh exit %[4]d)  gate=%[5]s\n"
	preVerifyTailFmt = "the unit is not done until this passes: fix it, run it again, and write the done line again.\n"
)

// preVerifyReport is what a failed check sends back, formatted from the outcome.
func preVerifyReport(o UnitVerifyOutcome) string {
	if o.Err != nil {
		return fmt.Sprintf(preVerifyHeadFmt, "error", -1, "error", -1, o.Err.Error()) + preVerifyTailFmt
	}
	s := fmt.Sprintf(preVerifyHeadFmt, o.Result.Compile, o.Result.BuildExit, o.Result.Smoke, o.Result.TestExit, gateOrNone(o.Result.Gate))
	for _, k := range []string{"compile_error", "smoke_error"} {
		if v := o.Status[k]; v != "" {
			s += k + ":\n" + ShapeOutput(v, LocalResultCap()/2) + "\n"
		}
	}
	if o.Result.GateText != "" {
		s += ShapeOutput(o.Result.GateText, LocalResultCap()/2) + "\n"
	}
	return s + preVerifyTailFmt
}

// deltaDigest identifies the mirror's delta (paths and contents of changed files, removed paths).
func deltaDigest(mirror string, changed, removed []string) string {
	h := sha256.New()
	files := append([]string(nil), changed...)
	sort.Strings(files)
	for _, f := range files {
		b, _ := os.ReadFile(filepath.Join(mirror, f))
		fmt.Fprintf(h, "c %s %d\n", f, len(b))
		h.Write(b)
	}
	rm := append([]string(nil), removed...)
	sort.Strings(rm)
	for _, f := range rm {
		fmt.Fprintf(h, "r %s\n", f)
	}
	return hex.EncodeToString(h.Sum(nil))
}
