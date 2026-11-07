package kernel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// An end-to-end run of dev_from_theorems against: a local bare "GitHub", the
// kernel's own process table, real bash for the unit scripts, and a scripted
// stand-in for the conversation model. Nothing in dev_run.go / dev_verify.go /
// dev_plan_*.go is replaced except the model and the network.

var kernelBoot sync.Once

// bootKernel initialises the process table once per test binary: Init() resets
// globals the kernel's timer goroutine is already reading.
func bootKernel() { kernelBoot.Do(Init) }

var unitIDRe = regexp.MustCompile(`id=(u[0-9a-f]{10})`)

type fakeModel struct {
	mu       sync.Mutex
	prompts  []string
	mirror   string
	unitWork func(prompt, unitID string, attempt int) // writes into the mirror like shadow replays do
	attempts map[string]int
	planner  func(prompt string) string
	reply    func(unit string, attempt int) string // what a unit conversation says at the end; nil: a normal summary
}

func (f *fakeModel) exec(prompt string) (string, string) {
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	f.mu.Unlock()
	id := fmt.Sprintf("%08x-0000-4000-8000-%012x", len(f.prompts)*7919, len(f.prompts))
	SetForkConvID(id)
	SetConvID(GetLastForkedPid(), id)

	switch {
	case strings.Contains(prompt, "[YOU ARE A REVIEWER"):
		return "looked at it.\nPASS", ""
	case strings.Contains(prompt, "[TASK] group the") || strings.Contains(prompt, "is too big for one session"):
		if f.planner != nil {
			return f.planner(prompt), ""
		}
		return "", ""
	case strings.Contains(prompt, "[INCREMENTAL BUILD"):
		m := unitIDRe.FindStringSubmatch(prompt)
		unit := ""
		if m != nil {
			unit = m[1]
		}
		f.mu.Lock()
		f.attempts[unit]++
		n := f.attempts[unit]
		f.mu.Unlock()
		f.unitWork(prompt, unit, n)
		if f.reply != nil {
			return f.reply(unit, n), ""
		}
		return "wrote the files.\ncompile=pass\nsmoke=pass\n" + strings.Repeat("padding so the kernel does not poll a thin conversation. ", 12), ""
	}
	return strings.Repeat("ok ", 200), ""
}

func (f *fakeModel) writeScripts(unit string, buildExit, testExit int, buildMsg string) {
	b, t := devVerifyScripts(unit)
	mustWrite(f.mirror, b, fmt.Sprintf("#!/bin/bash\necho %q\nexit %d\n", buildMsg, buildExit))
	mustWrite(f.mirror, t, fmt.Sprintf("#!/bin/bash\necho tests\nexit %d\n", testExit))
}

func mustWrite(root, rel, content string) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(content), 0o644)
}

func theory2(names ...string) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString("## MODULE: " + n + ".lean\ntheorem " + n + "_ok : True := trivial\n" + strings.Repeat("spec ", 40) + "\n\n")
	}
	return b.String()
}

type devEnv struct {
	bare, clone, mirror string
	model               *fakeModel
	prDone              *[]string
	dev                 string
}

var devEnvN int

func newDevEnv(t *testing.T, theory string) *devEnv {
	t.Helper()
	devEnvN++
	repoName := fmt.Sprintf("itest%d_%d", os.Getpid(), devEnvN)
	bare, clone := setupRemote(t) // also installs the local devGitPush
	// the dev repo's branch "main" exists in setupRemote; the dev branch is created by the kernel
	mirror := "/home/claude/" + repoName
	t.Cleanup(func() { os.RemoveAll(mirror) })

	oldHook := devRepoLocalDirHook
	devRepoLocalDirHook = func(string) (string, error) { return clone, nil }
	t.Cleanup(func() { devRepoLocalDirHook = oldHook })

	bootKernel()
	RegisterDevRepo("o", repoName, "")
	t.Cleanup(func() {
		dev_repos_mu.Lock()
		delete(dev_repos, "o/"+repoName)
		dev_repos_mu.Unlock()
	})
	theorem_store("ref/proj", "refsha1", theory, 0)

	fm := &fakeModel{mirror: mirror, attempts: map[string]int{}}
	oldExec := convExecFn
	SetConvExec(fm.exec)
	t.Cleanup(func() { convExecFn = oldExec })

	var pr []string
	oldPR := devCompletionPR
	devCompletionPR = func(pid int64, a, b, c, branch, sha string, v []string) { pr = append(pr, v...) }
	t.Cleanup(func() { devCompletionPR = oldPR })

	oldProbe := devRepoProbe
	devRepoProbe = func(url, branch string) bool { return false }
	t.Cleanup(func() { devRepoProbe = oldProbe })

	devPlanMu.Lock()
	devPlanMem = map[string]*DevPlan{}
	devPlanMu.Unlock()
	resetRegistry(t)
	return &devEnv{bare: bare, clone: clone, mirror: mirror, model: fm, prDone: &pr, dev: "o/" + repoName}
}

func bareShow(bare, ref string) string {
	out, err := exec.Command("git", "--git-dir", bare, "show", ref).CombinedOutput()
	if err != nil {
		return "<missing>"
	}
	return string(out)
}

func TestDevFromTheorems_HappyPath_KernelVerifiesAndPushes(t *testing.T) {
	env := newDevEnv(t, theory2("Alpha", "Beta"))
	env.model.unitWork = func(prompt, unit string, n int) {
		mustWrite(env.mirror, "src/impl.cu", "// implementation\n")
		env.model.writeScripts(unit, 0, 0, "built")
	}
	const pid = 9001
	branch := devBranchFor("ref/proj", "refsha1", pid)

	out, err := dev_from_theorems(pid, &Step{Owner: "ref", Repo: "proj"})
	if err != nil {
		t.Fatalf("dev_from_theorems: %v (out=%q)", err, out)
	}

	// the code is on the dev branch because the KERNEL pushed it — the fake child never ran git
	if got := bareShow(env.bare, branch+":src/impl.cu"); !strings.Contains(got, "implementation") {
		t.Fatalf("verified code not on the dev branch: %q", got)
	}
	// the plan is stored on the branch, with the module marked pass
	plan := bareShow(env.bare, devStateBranch+":"+devStatePlanPath(branch))
	if !strings.Contains(plan, `"pass": true`) || !strings.Contains(plan, "Alpha.lean") {
		t.Fatalf("plan.json on the branch lacks the pass state:\n%s", plan)
	}
	// 11/05 08:52:07: a reviewer read plan.json (it names the reference). The dev branch,
	// which conversations clone, holds no plan; the state branch does.
	if got := bareShow(env.bare, branch+":"+devPlanFile); got != "<missing>" {
		t.Fatalf("the dev branch must not hold the plan: %.120s", got)
	}
	// the theory was readable by the child: the mirror was primed from the branch tip
	if _, err := os.Stat(filepath.Join(env.mirror, ".einsteinOS/theory.md")); err != nil {
		t.Fatalf("mirror was not primed with the theory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.mirror, ".git")); err == nil {
		t.Fatal("the mirror must never contain .git")
	}
	if len(*env.prDone) != 1 || !strings.Contains((*env.prDone)[0], "PASS") {
		t.Fatalf("completion not reported: %v", *env.prDone)
	}
	// no planner for a theory that fits one unit; one work child + one reviewer
	var planners, works, reviews int
	for _, p := range env.model.prompts {
		switch {
		case strings.Contains(p, "[TASK] group the"):
			planners++
		case strings.Contains(p, "[INCREMENTAL BUILD"):
			works++
		case strings.Contains(p, "[YOU ARE A REVIEWER"):
			reviews++
		}
	}
	if planners != 0 || works != 1 || reviews != 1 {
		t.Fatalf("planners=%d works=%d reviews=%d", planners, works, reviews)
	}
	// the reviewer was told the kernel's measurement and not to rebuild
	for _, p := range env.model.prompts {
		if strings.Contains(p, "[YOU ARE A REVIEWER") && (!strings.Contains(p, "compile = pass") || !strings.Contains(p, "do NOT re-run the build")) {
			t.Fatalf("reviewer prompt lacks the kernel status / the no-rebuild rule:\n%s", p)
		}
	}
}

func TestDevFromTheorems_FailThenFix_RealErrorReachesTheFixConversation(t *testing.T) {
	env := newDevEnv(t, theory2("Alpha"))
	env.model.unitWork = func(prompt, unit string, n int) {
		if n == 1 {
			mustWrite(env.mirror, "src/impl.cu", "// broken\n")
			env.model.writeScripts(unit, 2, 0, "impl.cu(1): error: undefined symbol ix_frobnicate")
			return
		}
		// the fix conversation must be handed the REAL output and the failing code
		if !strings.Contains(prompt, "FIX ATTEMPT 1") || !strings.Contains(prompt, "undefined symbol ix_frobnicate") {
			t.Errorf("fix prompt lacks the real build error:\n%s", prompt)
		}
		if b, _ := os.ReadFile(filepath.Join(env.mirror, "src/impl.cu")); !strings.Contains(string(b), "broken") {
			t.Errorf("the mirror must hold the previous attempt's code for the fix to edit; has %q", b)
		}
		mustWrite(env.mirror, "src/impl.cu", "// fixed\n")
		env.model.writeScripts(unit, 0, 0, "built")
	}
	const pid = 9002
	branch := devBranchFor("ref/proj", "refsha1", pid)

	if _, err := dev_from_theorems(pid, &Step{Owner: "ref", Repo: "proj"}); err != nil {
		t.Fatal(err)
	}
	if got := bareShow(env.bare, branch+":src/impl.cu"); !strings.Contains(got, "fixed") || strings.Contains(got, "broken") {
		t.Fatalf("dev branch should hold the FIXED code, got %q", got)
	}
	// the broken code was parked on the unit's wip branch, never on the dev branch: the fix
	// was merged from it — and once merged, the wip branch is deleted (11/03 left two behind)
	hist, _ := exec.Command("git", "--git-dir", env.bare, "log", "--format=%s", branch).Output()
	if !strings.Contains(string(hist), "merge verified "+branch+"-wip-") {
		t.Fatalf("the fix should have been merged from the unit's wip branch; history:\n%s", hist)
	}
	out, _ := exec.Command("git", "--git-dir", env.bare, "branch", "--list", "*wip*").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("merged wip branch left on the remote: %s", out)
	}
	works := 0
	for _, p := range env.model.prompts {
		if strings.Contains(p, "[INCREMENTAL BUILD") {
			works++
		}
	}
	if works != 2 {
		t.Fatalf("want exactly one work + one fix conversation, got %d", works)
	}
}

func TestDevFromTheorems_ReentryResumesAndNeverReplans(t *testing.T) {
	// 12 modules of 8 KB: over the module budget → the planner runs once
	var names []string
	for i := 0; i < 12; i++ {
		names = append(names, fmt.Sprintf("M%02d", i))
	}
	var b strings.Builder
	for _, n := range names {
		b.WriteString("## MODULE: " + n + ".lean\ntheorem " + n + "_ok : True := trivial\n" + strings.Repeat("s", 8000) + "\n\n")
	}
	env := newDevEnv(t, b.String())
	plannerCalls := 0
	env.model.planner = func(prompt string) string {
		plannerCalls++
		if plannerCalls == 1 {
			return "unit: M00, M01, M02, M03\nunit: M04, M05, M06, M07\nunit: M08, M09, M10, M11\n"
		}
		return "unit: M11\nunit: M00\n" // a different answer, if anyone asked again
	}
	failFirst := true
	env.model.unitWork = func(prompt, unit string, n int) {
		mustWrite(env.mirror, "src/"+unit+".cu", "// "+unit+"\n")
		code := 0
		if failFirst {
			failFirst, code = false, 1 // the very first unit attempt fails its build
		}
		env.model.writeScripts(unit, code, 0, "out")
	}
	const pid = 9003
	step := &Step{Owner: "ref", Repo: "proj"}
	if _, err := dev_from_theorems(pid, step); err != nil {
		t.Fatal(err)
	}
	if plannerCalls != 1 {
		t.Fatalf("the planner ran %d times: a failure inside the run must never trigger a re-plan", plannerCalls)
	}
	branch := devBranchFor("ref/proj", "refsha1", pid)
	for _, u := range []string{"M00", "M04", "M08"} {
		if !strings.Contains(bareShow(env.bare, devStateBranch+":"+devStatePlanPath(branch)), u+".lean") {
			t.Fatalf("plan.json lost %s", u)
		}
	}
	// a second entry (what re-queueing does) reuses the stored plan and has nothing left to do
	before := len(env.model.prompts)
	if _, err := dev_from_theorems(pid, step); err != nil {
		t.Fatal(err)
	}
	if plannerCalls != 1 || len(env.model.prompts) != before {
		t.Fatalf("re-entry with everything passed must start nothing: planner=%d, new conversations=%d", plannerCalls, len(env.model.prompts)-before)
	}
	// even after the in-memory plan is gone (kernel restart), the plan comes back from the branch
	devPlanMu.Lock()
	devPlanMem = map[string]*DevPlan{}
	devPlanMu.Unlock()
	if _, err := dev_from_theorems(pid, step); err != nil {
		t.Fatal(err)
	}
	if plannerCalls != 1 || len(env.model.prompts) != before {
		t.Fatalf("after a restart the plan must be read from the branch: planner=%d, new=%d", plannerCalls, len(env.model.prompts)-before)
	}
}

func TestDevCollectLiteNeverPollsAndNeverTrustsTheChildsCompileClaim(t *testing.T) {
	bootKernel()
	devSettleSleep = 0
	mk := func(result string) int64 {
		pid, err := ForkExec("true", "")
		if err != nil {
			t.Fatal(err)
		}
		Wait(pid)
		SetResult(pid, result)
		return pid
	}
	// a child that claims compile=pass: the claim is dropped, the gpu report is kept
	st := func(r string) map[string]string { _, s := devCollectLite(mk(r), "o/r"); return s }
	s := st("done, " + strings.Repeat("x", 120) + "\ncompile=pass\nsmoke=pass\ngpu_pairs_ok=0-1\n")
	if s["compile"] != "" || s["smoke"] != "" || s["gpu_pairs_ok"] != "0-1" {
		t.Fatalf("%v", s)
	}
	// an API error text, or nothing at all, is "no response" → a fresh child
	if st("[API Error] TLS handshake timeout")["compile"] != "no_response" {
		t.Fatal("API error must read as no_response")
	}
	if st("")["compile"] != "no_response" {
		t.Fatal("empty output and no files must read as no_response")
	}
	// a short honest summary is NOT no_response: the mirror decides what was done
	if st(strings.Repeat("built ix_quant.h and the tests. ", 5))["compile"] == "no_response" {
		t.Fatal("a real summary must not be retried as a dead child")
	}
}

// The theory the theory phase really produces: one "## FILE: <sandbox path>" section per
// .lean file, rendered by renderChunkFiles (chunk_files.go) — not the hand-written
// "## MODULE:" form the other tests use. This runs it from the files to a finished dev branch.
func theoryFromSandboxFiles(names ...string) string {
	var files []chunkFile
	for _, n := range names {
		body := "/-! # MODULE: " + n + " -/\ntheorem " + strings.ToLower(n) + "_ok : True := trivial\n" + strings.Repeat("-- spec line\n", 20)
		files = append(files, chunkFile{RemotePath: TheoryOutputsDir + "/" + n + ".lean", Name: n + ".lean", Content: []byte(body)})
	}
	return renderChunkFiles(files)
}

func TestTheoryPhaseOutputRunsThroughTheDevLoopToAFinishedBranch(t *testing.T) {
	theory := theoryFromSandboxFiles("ExpertCacheResidency", "ExpertSourceDispatch", "GgufHeaderReader")
	env := newDevEnv(t, theory)
	var prompts []string
	env.model.unitWork = func(prompt, unit string, n int) {
		prompts = append(prompts, prompt)
		mustWrite(env.mirror, "src/"+unit+".cu", "// "+unit+"\n")
		env.model.writeScripts(unit, 0, 0, "built")
	}
	const pid = 9101
	branch := devBranchFor("ref/proj", "refsha1", pid)

	if _, err := dev_from_theorems(pid, &Step{Owner: "ref", Repo: "proj"}); err != nil {
		t.Fatalf("the loop did not close: %v", err)
	}
	// the three sandbox files became three modules named by file, in one unit
	plan := bareShow(env.bare, devStateBranch+":"+devStatePlanPath(branch))
	for _, m := range []string{"ExpertCacheResidency.lean", "ExpertSourceDispatch.lean", "GgufHeaderReader.lean"} {
		if !strings.Contains(plan, m) {
			t.Fatalf("module %s missing from the plan:\n%s", m, plan)
		}
	}
	if strings.Contains(plan, TheoryOutputsDir) {
		t.Fatalf("modules must be named by file, not by sandbox path:\n%s", plan)
	}
	// the theory the child is told to read is on the branch, with the headers the prompt tells it to grep for
	th := bareShow(env.bare, branch+":.einsteinOS/theory.md")
	if !strings.Contains(th, "## FILE: "+TheoryOutputsDir+"/ExpertCacheResidency.lean") {
		t.Fatalf("theory.md on the branch lacks the sections:\n%.300s", th)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], `grep -n '^## \(FILE\|MODULE\):'`) {
		t.Fatalf("the child's instructions must search for the headers the theory really has; prompts=%d", len(prompts))
	}
	if len(*env.prDone) != 1 {
		t.Fatalf("the loop must end in the completion report: %v", *env.prDone)
	}
	// and the unit's code is on the branch, pushed by the kernel
	if !strings.Contains(bareShow(env.bare, branch+":"+"src/"), ".cu") {
		t.Fatalf("no unit code on the dev branch")
	}
}

// the 2026/11/02 unit 2: two attempts wrote nothing and nobody could say why
func TestAConversationThatWritesNothingHandsItsLastWordsToTheNextAttempt(t *testing.T) {
	env := newDevEnv(t, theory2("Alpha"))
	env.model.reply = func(unit string, n int) string {
		if n == 1 {
			return "I looked for the repository for a long time.\nBLOCKER: I cannot see /home/claude/x anywhere in my sandbox, so I did not write anything.\n" + strings.Repeat("more explanation. ", 30)
		}
		return "wrote the files.\n" + strings.Repeat("padding. ", 80)
	}
	var fixPrompt string
	env.model.unitWork = func(prompt, unit string, n int) {
		if n == 1 {
			return // writes nothing
		}
		fixPrompt = prompt
		mustWrite(env.mirror, "src/impl.cu", "// ok\n")
		env.model.writeScripts(unit, 0, 0, "built")
	}
	const pid = 9201
	if _, err := dev_from_theorems(pid, &Step{Owner: "ref", Repo: "proj"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FIX ATTEMPT 1", "nothing_written", "wrote no file", "BLOCKER: I cannot see /home/claude/x"} {
		if !strings.Contains(fixPrompt, want) {
			t.Fatalf("the fix conversation must be told what the failed one said; lacks %q in:\n%.1500s", want, fixPrompt)
		}
	}
}

func TestDevBranchIsNamedByWhatItDevelopsNotByThePid(t *testing.T) {
	a := devBranchFor("Niko1221/Strata", "01529aaa82b9", 6)
	b := devBranchFor("Niko1221/Strata", "01529aaa82b9", 7)
	if a != b {
		t.Fatalf("a restart (pid 6 → 7) must find the same branch: %s vs %s", a, b)
	}
	if a == devBranchFor("Niko1221/Strata", "ffffffffffff", 6) || a == devBranchFor("Other/Repo", "01529aaa82b9", 6) {
		t.Fatal("another commit or another reference repo is another branch")
	}
	if got := devBranchFor("x/y", "", 9); got != "einsteinOS/dev-9" {
		t.Fatalf("without a commit the pid is the fallback: %s", got)
	}
	if !strings.HasPrefix(a, "einsteinOS/dev-") || strings.ContainsAny(a, " ~^:?*[\\") {
		t.Fatalf("not a valid ref name: %q", a)
	}
}

// the restart the 2026/11/02 run needed: units 1 and 2 had passed on einsteinOS/dev-6
func TestARestartedProcessResumesFromTheBranchAndRedoesNothing(t *testing.T) {
	env := newDevEnv(t, theory2("Alpha", "Beta"))
	env.model.unitWork = func(prompt, unit string, n int) {
		mustWrite(env.mirror, "src/"+unit+".cu", "// "+unit+"\n")
		env.model.writeScripts(unit, 0, 0, "built")
	}
	step := &Step{Owner: "ref", Repo: "proj"}
	if _, err := dev_from_theorems(9301, step); err != nil {
		t.Fatal(err)
	}
	conversationsBefore := len(env.model.prompts)

	// the process dies and a new one starts: new pid, empty memory
	devPlanMu.Lock()
	devPlanMem = map[string]*DevPlan{}
	devPlanMu.Unlock()
	if _, err := dev_from_theorems(9302, step); err != nil {
		t.Fatal(err)
	}
	if len(env.model.prompts) != conversationsBefore {
		t.Fatalf("the restarted process started %d conversation(s): it did not find the finished work on the branch", len(env.model.prompts)-conversationsBefore)
	}
}

func countConversations(prompts []string) (units, planners int) {
	for _, p := range prompts {
		switch {
		case strings.Contains(p, "[INCREMENTAL BUILD"):
			units++
		case strings.Contains(p, "[TASK] group") || strings.Contains(p, "is too big for one session"):
			planners++
		}
	}
	return
}

// the overnight run: 3 attempts that wrote nothing, then a split, then more of the same
func TestTwoConversationsInARowThatWriteNothingStopTheRunWithoutSplitting(t *testing.T) {
	env := newDevEnv(t, theory2("Alpha", "Beta"))
	env.model.reply = func(unit string, n int) string {
		return "I cannot see the repository, so I wrote nothing. (conversation " + string(rune('0'+n)) + ")\n" + strings.Repeat("explanation. ", 30)
	}
	env.model.unitWork = func(prompt, unit string, n int) {} // writes nothing, ever
	const pid = 9701
	branch := devBranchFor("ref/proj", "refsha1", pid)

	_, err := dev_from_theorems(pid, &Step{Owner: "ref", Repo: "proj"})
	if err == nil {
		t.Fatal("the run must stop with an error")
	}
	for _, want := range []string{"the last 2 conversations wrote no file", "not cured by retrying or by splitting", "I cannot see the repository", branch} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error must say what happened; lacks %q:\n%s", want, err)
		}
	}
	units, planners := countConversations(env.model.prompts)
	if units != devMaxSilentAttempts {
		t.Fatalf("exactly %d unit conversations, got %d: a third one, or a split, is the loop", devMaxSilentAttempts, units)
	}
	if planners != 0 {
		t.Fatalf("a planner was asked to split a unit whose conversations never started (%d)", planners)
	}
	// nothing is lost: the plan is on the branch, ready for a restart
	if got := bareShow(env.bare, devStateBranch+":"+devStatePlanPath(branch)); !strings.Contains(got, "Alpha.lean") {
		t.Fatalf("the plan must be kept:\n%.200s", got)
	}
}

func TestOneSilentConversationIsNotAStop(t *testing.T) {
	env := newDevEnv(t, theory2("Alpha"))
	env.model.unitWork = func(prompt, unit string, n int) {
		if n == 1 {
			return // the first one writes nothing
		}
		mustWrite(env.mirror, "src/impl.cu", "// ok\n")
		env.model.writeScripts(unit, 0, 0, "built")
	}
	if _, err := dev_from_theorems(9702, &Step{Owner: "ref", Repo: "proj"}); err != nil {
		t.Fatalf("one silent conversation followed by one that works is just a fix attempt: %v", err)
	}
	if units, _ := countConversations(env.model.prompts); units != 2 {
		t.Fatalf("%d unit conversations", units)
	}
}

func TestTheSilentStreakEndsWhenAConversationWritesSomething(t *testing.T) {
	p := newDevPlan("o/r", "s", mods("A"), [][]string{{"A"}}, "p")
	id := p.Leaves()[0].ID
	if p.NoteSilent(id) != 1 || p.NoteSilent(id) != 2 || p.Silent(id) != 2 {
		t.Fatal("the streak counts")
	}
	p.ClearSilent(id)
	if p.Silent(id) != 0 || p.NoteSilent(id) != 1 {
		t.Fatal("a conversation that wrote something restarts the count")
	}
	p.MarkUnitPass(p.Leaves()[0])
	if p.Silent(id) != 0 {
		t.Fatal("a pass clears it")
	}
	// it is not persisted: a restart is a person deciding to try again
	p.NoteSilent(id)
	b, _ := p.Marshal()
	q, err := unmarshalDevPlan(b)
	if err != nil {
		t.Fatal(err)
	}
	if q.Silent(id) != 0 {
		t.Fatal("the streak must not survive a restart")
	}
}
