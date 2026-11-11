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
	"time"
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
	stop := touchRoleWhile(role, preVerifyTouchEvery)
	ok, report = fn()
	stop()
	return true, ok, report
}

// preVerifyTouchEvery: how often a conversation waiting on the kernel's verification is marked
// active. 11/10 the integration unit's verification ran for hours (the search's clones);
// the conversation, waiting on it, did nothing the watchdog could see, was found idle after
// 2h30m at 18:31 and 21:15, and a second and a third conversation were started for the unit.
var preVerifyTouchEvery = 30 * time.Second

// touchRoleWhile marks every task of role active now and every interval until stop is called.
func touchRoleWhile(role string, interval time.Duration) (stop func()) {
	touch := func() {
		metaMu.Lock()
		now := time.Now().Unix()
		for _, m := range taskMeta {
			if m.Role == role {
				m.LastActivity = now
			}
		}
		metaMu.Unlock()
	}
	touch()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				touch()
			}
		}
	}()
	return func() { close(done); touch() }
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

// sameRejected: the last verification at a done line failed on exactly these files. 11/06
// 18:19-18:50 four done lines in a row re-ran the same 7.5-minute verification of unchanged
// files, each with the same result.
func (p *preVerifyResult) sameRejected(digest string) bool {
	return p != nil && !p.ok && p.digest != "" && p.digest == digest
}
