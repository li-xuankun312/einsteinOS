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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type preVerifyResult struct {
	ok     bool
	digest string
	out    UnitVerifyOutcome
	// portMap: the unit's port map in the mirror as the verification started, and when
	portMap string
	at      time.Time
	// judged: the last verification before this one whose gates were judged
	judged *preVerifyResult
}

// lastJudged: the last verification whose gates were judged — this one when its build compiled,
// else the one it carries. 11/26 17:56:58 kernel_unported, port_ref_file_unknown, port_extra and
// port_incomplete were said to have passed at the verification of 17:42:38, whose compile had
// failed and whose gates were never judged.
func (p *preVerifyResult) lastJudged() *preVerifyResult {
	if p == nil {
		return nil
	}
	if p.out.Err == nil && p.out.Verified && p.out.Result.Compile == "pass" {
		return p
	}
	return p.judged
}

const (
	// %[1]s the gates, %[2]s when the last verification started, %[3]s the map, %[4]d lines,
	// %[5]s them
	gatesRegressedFmt = "\n[GATES THAT PASSED AT THE LAST VERIFICATION AND FAIL NOW — measured from the mirror as each verification started]\n%[1]s passed at the verification that started at %[2]s and fail now; %[3]s held then %[4]d line(s) it does not hold now:\n%[5]s\n"
	gatesRegressedMax = 60
	// %[1]s when the verification started, %[2]d changes, %[3]s them
	preVerifySinceFmt = "\n[WHAT THE MIRROR WAS JUDGED AS — measured]\nthis verification judged the mirror as it was at %[1]s; since then the conversations' commands changed it %[2]d time(s):\n%[3]s\n"
	preVerifySinceMax = 40
)

// preVerifySince: when the verification took the mirror, and what changed it after. 11/26 09:41:31
// the unit's conversation rewrote tests/k39_integration/ix_router_top10_wave.cu, and at 09:41:43 it
// was sent kernel_shadowed_in_test for that file, judged on the mirror of 09:34:37; each of the
// day's verifications took 7 minutes and was answered of a tree the conversation had changed since.
func preVerifySince(start time.Time, since []string) string {
	if len(since) == 0 {
		return ""
	}
	shown := since
	if len(shown) > preVerifySinceMax {
		shown = append(append([]string(nil), shown[:preVerifySinceMax]...), fmt.Sprintf("(+%d more)", len(since)-preVerifySinceMax))
	}
	return fmt.Sprintf(preVerifySinceFmt, start.Format("15:04:05"), len(since), strings.Join(shown, "\n"))
}

// gatesRegressed: the gates failing now that passed at the last verification, with the lines of
// the port map that verification saw and this one does not ("" when no gate went that way, or
// the map lost no line). 11/26 09:41:43 kernel_unported and port_incomplete passed; at 09:41:51
// the conversation removed 156 rows of the map, the next verification failed kernel_unported,
// the one after port_incomplete, and nothing said which lines had made them pass.
func gatesRegressed(prev *preVerifyResult, now UnitVerifyOutcome, mapNow, mapPath string) string {
	if prev == nil || prev.out.Err != nil || !prev.out.Verified || prev.out.Result.Compile != "pass" || now.Err != nil || !now.Verified || now.Result.Compile != "pass" {
		return ""
	}
	had := map[string]bool{}
	for _, g := range prev.out.Result.Gates {
		had[g] = true
	}
	var newly []string
	for _, g := range now.Result.Gates {
		if !had[g] {
			newly = append(newly, g)
		}
	}
	if len(newly) == 0 {
		return ""
	}
	holds := map[string]bool{}
	for _, l := range strings.Split(mapNow, "\n") {
		holds[l] = true
	}
	var lost []string
	for _, l := range strings.Split(prev.portMap, "\n") {
		if strings.TrimSpace(l) != "" && !holds[l] {
			lost = append(lost, l)
		}
	}
	if len(lost) == 0 {
		return ""
	}
	shown := lost
	if len(shown) > gatesRegressedMax {
		shown = append(append([]string(nil), shown[:gatesRegressedMax]...), fmt.Sprintf("(+%d more)", len(lost)-gatesRegressedMax))
	}
	return fmt.Sprintf(gatesRegressedFmt, strings.Join(newly, ", "), prev.at.Format("15:04:05"), mapPath, len(lost), strings.Join(shown, "\n"))
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
	s := fmt.Sprintf(preVerifyHeadFmt, o.Result.Compile, o.Result.BuildExit, o.Result.Smoke, o.Result.TestExit, gateOf(o.Result))
	for _, k := range []string{"compile_error", "smoke_error"} {
		if v := o.Status[k]; v != "" {
			s += k + ":\n" + ShapeOutput(v, LocalResultCap()/2) + "\n"
		}
	}
	if o.Result.GateText != "" {
		s += gatesSentText(o.Result.GateText, o.Result.Gates, LocalResultCap()/2) + "\n"
	}
	s += verifiedFilesText(o.Changed, o.Removed)
	return Bagu(preVerifyWhere, fmt.Sprintf(preVerifyWhatFmt, o.Result.Compile, o.Result.Smoke, gateOf(o.Result)), s+preVerifyTailFmt, "1", spawnTo())
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

// %[1]s the file holding the whole of it
const preVerifyGateTextFileFmt = " (the whole text sent back, %[1]d bytes: %[2]s)"

// gatesTextDir: where each verification's whole gate text is written — the crash directory when
// one is set, else ~/.einsteinos/gates. 11/26 09:49:16 the log kept 3000 bytes of a gate text
// whose kernel_unported part listed 168 refused rows, and what the conversation was sent of it
// could not be read anywhere afterwards.
var gatesTextDir = func() string {
	if d := crashDir(); d != "" {
		return filepath.Join(d, "gates")
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".einsteinos", "gates")
}

// writeGatesText writes a verification's whole gate text and returns where ("" when it could not).
func writeGatesText(unitID, text string, at time.Time) string {
	d := gatesTextDir()
	if d == "" || text == "" || os.MkdirAll(d, 0o755) != nil {
		return ""
	}
	p := filepath.Join(d, unitID+"-"+at.Format("20060102-150405")+".txt")
	if os.WriteFile(p, []byte(text), 0o644) != nil {
		return ""
	}
	return p
}

// gatesLogLine: the log line of the gates' text — its share of the cap, and where the whole is.
func gatesLogLine(repo, unitID, text string, kinds []string, at time.Time) string {
	line := fmt.Sprintf(preVerifyGateTextLogFmt, repo, unitID, gatesLogText(text, kinds, preVerifyGateTextLogMax))
	if p := writeGatesText(unitID, text, at); p != "" {
		line += fmt.Sprintf(preVerifyGateTextFileFmt, len(text), p)
	}
	return line
}

// %[1]s repo, %[2]s unit, %[3]s the gate's text
const preVerifyGateTextLogFmt = "dev_preverify: %[1]s unit %[2]s: the gates' text sent back: %[3]s"

// preVerifyGateTextLogMax: how much of it the log keeps. 11/23 20:43:15 and 11/24 00:56:50 the log
// said gate="kernel_unported" (also failed: toolchain_unused, port_ref_file_unknown, port_extra,
// port_incomplete) and not which rows, kernels or files any of them named.
const preVerifyGateTextLogMax = 3000

// gatesLogText: the gates' text for the log, each failed gate keeping its share of the cap, so
// that a long one does not take the whole. 11/25 23:05:05 and 23:21:19 entry_stubbed,
// kernel_shadowed_in_test, port_ref_file_unknown and port_extra failed beside kernel_unported,
// whose 220 refused rows filled the 3000 the log keeps, and no line of the others was logged.
func gatesLogText(text string, kinds []string, max int) string {
	if len(kinds) < 2 {
		return truncStr(text, max)
	}
	parts := gatesParts(text, kinds)
	share := max / len(parts)
	var out []string
	for _, p := range parts {
		out = append(out, truncStr(p, share))
	}
	return strings.Join(out, "\n")
}

// gatesSentText: the gates' text as the conversation is sent it, each failed gate shaped to its
// share of limit (its head and its end). 11/26 14:27:15 four gates came to 21728 bytes, shaped as
// one text to its first 4800 bytes and last 3200: port_ref_file_unknown and the start of
// port_extra, then the end of port_incomplete's 150 lines — kernel_unported, failing at 14:05:49,
// would have been in the part cut out.
func gatesSentText(text string, kinds []string, limit int) string {
	if len(kinds) < 2 || len(text) <= limit {
		return ShapeOutput(text, limit)
	}
	parts := gatesParts(text, kinds)
	share := limit / len(parts)
	var out []string
	for _, p := range parts {
		out = append(out, ShapeOutput(p, share))
	}
	return strings.Join(out, "\n")
}

// gatesParts: the gates' text cut where each failed gate's text begins.
func gatesParts(text string, kinds []string) []string {
	lines := strings.Split(text, "\n")
	starts := map[string]bool{}
	for _, k := range kinds {
		starts[k] = true
	}
	var parts []string
	var cur []string
	for _, l := range lines {
		if k, _, ok := strings.Cut(l, ": "); ok && starts[k] && len(cur) > 0 {
			parts = append(parts, strings.Join(cur, "\n"))
			cur = nil
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		parts = append(parts, strings.Join(cur, "\n"))
	}
	return parts
}

// %[1]s repo, %[2]s unit, %[3]s the compile's output — 11/24 03:24:38 and 03:25:36 compile=fail
// (exit 1) in 12s was logged with gate="" and nothing of what the compiler said
// %[1]s repo, %[2]s unit, %[3]s the files with whose they are
const preVerifyOwnersLogFmt = "dev_preverify: %[1]s unit %[2]s: the files the failed compile names, sent back: %[3]s"

// %[1]s repo, %[2]s unit, %[3]s the kind, %[4]s its text
const failureTextLogFmt = "dev_from_theorems: %[1]s unit %[2]s: the %[3]s failure's text, as the next attempt is given it: %[4]s"

// %[1]s repo, %[2]s unit, %[3]d exit, %[4]s the output's end
const preVerifySmokeLogFmt = "dev_preverify: %[1]s unit %[2]s: the failed test.sh's output sent back (exit %[3]d, its end): %[4]s"

const preVerifyCompileLogFmt = "dev_preverify: %[1]s unit %[2]s: the failed compile's output sent back (its end): %[3]s"

// failedCompileLogText: what the log gives of a failed build's output — its error lines first,
// then its end. BuildOut alone: the conversation's compile_error holds it too, and when the
// verification put the error lines first they are not put again — 11/25 04:34:25 the log gave
// them twice, compile_error and BuildOut joined.
func failedCompileLogText(out string) string {
	if strings.HasPrefix(out, compileErrorsHead) {
		return truncStr(out, preVerifyGateTextLogMax)
	}
	if errs := compileErrorLines(out, compileErrorLinesMax); errs != "" {
		// the errors first, wherever they were: 11/24 09:08:29 the log's 3000 bytes were the
		// output's end, the warnings of test_kg.cu around the one error
		out = errs + "…\n" + tailText(out, preVerifyGateTextLogMax/2)
	}
	return tailText(out, preVerifyGateTextLogMax)
}

// %[1]d missing then, %[2]d of, %[3]s when that verification started, %[4]d missing now, %[5]s the
// map, %[6]d lines, %[7]s them
const portIncompleteGrewFmt = "\n[PORT_INCOMPLETE FAILS ON MORE DEFINITIONS THAN AT THE LAST VERIFICATION — measured from the mirror as each verification started]\nport_incomplete: %[1]d of the %[2]d definitions had no counterpart at the verification that started at %[3]s, %[4]d now; %[5]s held then %[6]d line(s) giving a reference definition a counterpart that it does not hold now:\n%[7]s\n"

var portIncompleteCountRe = regexp.MustCompile(`(?m)^port_incomplete: (\d+) of the (\d+) `)

// portIncompleteCount: port_incomplete's number of definitions with no counterpart, and of all,
// from a verification's gate text (ok false when it did not fail).
func portIncompleteCount(o UnitVerifyOutcome) (n, of int, ok bool) {
	m := portIncompleteCountRe.FindStringSubmatch(o.Result.GateText)
	if m == nil {
		return 0, 0, false
	}
	n, _ = strconv.Atoi(m[1])
	of, _ = strconv.Atoi(m[2])
	return n, of, true
}

// portIncompleteGrew: when port_incomplete failed at the last verification and fails now on more
// definitions, the lines of the port map that verification saw giving a reference definition a
// counterpart and this one does not. 11/26 15:16:22 port_incomplete failed on 147 of 168 and at
// 15:55:28 on all 168, the unit's conversations having taken every row of the reference's
// definitions out of the map, and only a gate going from pass to fail was given what went.
func portIncompleteGrew(prev *preVerifyResult, now UnitVerifyOutcome, mapNow, mapPath string, syms []RefSymbol) string {
	if prev == nil {
		return ""
	}
	was, of, ok1 := portIncompleteCount(prev.out)
	is, _, ok2 := portIncompleteCount(now)
	if !ok1 || !ok2 || is <= was {
		return ""
	}
	own := map[string]bool{}
	for _, s := range syms {
		own[s.File+"\t"+s.Name] = true
	}
	holds := map[string]bool{}
	for _, l := range strings.Split(mapNow, "\n") {
		holds[l] = true
	}
	var lost []string
	for _, l := range strings.Split(prev.portMap, "\n") {
		f := strings.Split(l, "\t")
		if len(f) >= 4 && own[strings.TrimSpace(f[0])+"\t"+strings.TrimSpace(f[1])] && !holds[l] {
			lost = append(lost, l)
		}
	}
	if len(lost) == 0 {
		return ""
	}
	shown := lost
	if len(shown) > gatesRegressedMax {
		shown = append(append([]string(nil), shown[:gatesRegressedMax]...), fmt.Sprintf("(+%d more)", len(lost)-gatesRegressedMax))
	}
	return fmt.Sprintf(portIncompleteGrewFmt, was, of, prev.at.Format("15:04:05"), is, mapPath, len(lost), strings.Join(shown, "\n"))
}
