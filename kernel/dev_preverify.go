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
	"strings"
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
	preVerifyTailFmt = "the kernel ends this conversation at a done line only when this verification passes.\n"
	// preVerifyFilesFmt: the files the verification was run on. 11/15 dev_unit_33 was told its
	// test failed on a pattern missing from locate.py, could not see that locate.py was not
	// among the files the kernel had, and spent 13 rounds concluding the kernel's checkout was
	// stale or cached — committing deliberately wrong code to "align" with it, and finally
	// installing a .git/hooks/post-checkout to rewrite the file behind the kernel's back.
	preVerifyFilesFmt   = "the %[1]d file(s) this was run on, the only ones the kernel has from you:\n%[2]s\n"
	preVerifyNoFilesFmt = "you have changed no file the kernel can see: it ran on the branch's existing code. Anything you wrote is somewhere the kernel does not read.\n"
	// inserted into the shell: where, and %[1]s/%[2]s/%[3]s what the verification measured
	preVerifyWhere   = "这个对话在真机上的 tree"
	preVerifyWhatFmt = "内核刚在真机上验证了它，compile=%[1]s smoke=%[2]s gate=%[3]s"
)

// preVerifyReport is what a failed check sends back, formatted from the outcome.
func preVerifyReport(o UnitVerifyOutcome) string {
	if o.Err != nil {
		return Bagu(preVerifyWhere, fmt.Sprintf(preVerifyWhatFmt, "error", "error", o.Err.Error()),
			fmt.Sprintf(preVerifyHeadFmt, "error", -1, "error", -1, o.Err.Error())+preVerifyTailFmt, "1", spawnTo())
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
	s += verifiedFilesText(o.Changed, o.Removed)
	return Bagu(preVerifyWhere, fmt.Sprintf(preVerifyWhatFmt, o.Result.Compile, o.Result.Smoke, gateOrNone(o.Result.Gate)), s+preVerifyTailFmt, "1", spawnTo())
}

// verifiedFilesText: the files the verification ran on, or that there were none of the
// conversation's own.
func verifiedFilesText(changed, removed []string) string {
	files := append([]string(nil), changed...)
	for _, f := range removed {
		files = append(files, f+" (removed)")
	}
	sort.Strings(files)
	if len(files) == 0 {
		return preVerifyNoFilesFmt
	}
	shown := files
	if len(shown) > 40 {
		shown = append(append([]string{}, shown[:40]...), fmt.Sprintf("… %d more", len(files)-40))
	}
	return fmt.Sprintf(preVerifyFilesFmt, len(files), strings.Join(shown, "\n"))
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
// endedOn: p is the verification at a done line that let the conversation end (it passed, or
// what failed is outside the unit, preverifyMayEnd) and digest is its tree.
func (p *preVerifyResult) endedOn(digest string) bool {
	return p != nil && p.digest != "" && p.digest == digest && p.out.Verified && preverifyMayEnd(p.ok, p.out.Result.Gate)
}

func (p *preVerifyResult) sameRejected(digest string) bool {
	return p != nil && !p.ok && p.digest != "" && p.digest == digest
}

// preVerifyUnchangedMax: how many done lines in a row may carry the files of the last
// rejected verification before the conversation is let go. The conversation has been told
// what is wrong and has changed nothing since; another round changes nothing either.
// 11/15 17:47–19:53 dev_unit_33 wrote its done line 13 times over two hours on the same tree
// (smoke_fail), each time getting the same report back, until MaxRounds(20) ended it.
const preVerifyUnchangedMax = 3

// preVerifyUnchangedFmt: what the conversation is told the second time it writes a done line
// on the files of the last rejected verification, instead of the same report a second time.
// 11/15 dev_unit_33 got that report back eight times word for word, with no tool result and
// nothing new in it, and said EINSTEINOS_DONE to it nineteen times.
const preVerifyUnchangedFmt = "[KERNEL VERIFICATION] nothing has changed since the verification above: the files the " +
	"kernel has from you are byte for byte the ones it just rejected, so there is nothing new to run. " +
	"Whatever you wrote since then went somewhere the kernel does not read — not the mirror it verifies. " +
	"Check that the path you are writing is under %[1]s, and that the file you mean is one of the ones " +
	"listed above. This is time %[2]d of %[3]d; at %[3]d the kernel ends this conversation and judges the tree as it stands.\n"

// UnchangedReport: what to send back on the nth done line in a row on an unchanged rejected
// tree, with the mirror the kernel reads.
func UnchangedReport(mirror string, n int) string {
	return Bagu(preVerifyWhere, preVerifyUnchangedWhat,
		fmt.Sprintf(preVerifyUnchangedFmt, mirror, n, preVerifyUnchangedMax)+preVerifyTailFmt, "1", spawnTo())
}

const preVerifyUnchangedWhat = "内核读到的文件和上次被拒绝的那次一模一样，没有任何新东西可测"

// unchangedEnd: whether this is the nth done line in a row on the files of the last rejected
// verification, with n at the limit — the conversation ends and the kernel judges the tree.
func (p *preVerifyResult) unchangedEnd(n int) bool { return n >= preVerifyUnchangedMax }

// ordinalSuffix: "st", "nd", "rd" or "th" for n.
func ordinalSuffix(n int) string {
	if n%100 >= 11 && n%100 <= 13 {
		return "th"
	}
	switch n % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	}
	return "th"
}

// preVerifyLogFields: the verification's outcome as the log line gives it — the error, when
// the verification could not run, as the conversation is told it (preVerifyReport). 11/18
// 01:51:43–02:47:35 seven lines read 'compile= smoke= gate=""' while dev_fix_1 of uf7cdc0dcca
// was told "gate=delta: lstat /home/claude/baloon: no such file or directory": the log alone
// could not say why its unit was sent back.
func preVerifyLogFields(o UnitVerifyOutcome) string {
	if o.Err != nil {
		return fmt.Sprintf("compile=error smoke=error gate=%q", o.Err.Error())
	}
	return fmt.Sprintf("compile=%s smoke=%s gate=%q", o.Result.Compile, o.Result.Smoke, o.Result.Gate)
}
