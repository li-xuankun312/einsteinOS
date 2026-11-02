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
	branch := fmt.Sprintf("einsteinOS/dev-%d", pid)

	out, err := dev_from_theorems(pid, &Step{Owner: "ref", Repo: "proj"})
	if err != nil {
		t.Fatalf("dev_from_theorems: %v (out=%q)", err, out)
	}

	// the code is on the dev branch because the KERNEL pushed it — the fake child never ran git
	if got := bareShow(env.bare, branch+":src/impl.cu"); !strings.Contains(got, "implementation") {
		t.Fatalf("verified code not on the dev branch: %q", got)
	}
	// the plan is stored on the branch, with the module marked pass
	plan := bareShow(env.bare, branch+":"+devPlanFile)
	if !strings.Contains(plan, `"pass": true`) || !strings.Contains(plan, "Alpha.lean") {
		t.Fatalf("plan.json on the branch lacks the pass state:\n%s", plan)
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
	branch := fmt.Sprintf("einsteinOS/dev-%d", pid)

	if _, err := dev_from_theorems(pid, &Step{Owner: "ref", Repo: "proj"}); err != nil {
		t.Fatal(err)
	}
	if got := bareShow(env.bare, branch+":src/impl.cu"); !strings.Contains(got, "fixed") || strings.Contains(got, "broken") {
		t.Fatalf("dev branch should hold the FIXED code, got %q", got)
	}
	// the broken code was parked on the unit's wip branch, never on the dev branch
	out, _ := exec.Command("git", "--git-dir", env.bare, "branch", "--list", "*wip*").Output()
	if !strings.Contains(string(out), "-wip-") {
		t.Fatalf("failing code should have been parked on a wip branch; branches: %s", out)
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
	branch := fmt.Sprintf("einsteinOS/dev-%d", pid)
	for _, u := range []string{"M00", "M04", "M08"} {
		if !strings.Contains(bareShow(env.bare, branch+":"+devPlanFile), u+".lean") {
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
