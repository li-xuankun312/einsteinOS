package kernel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func resetRegistry(t *testing.T) {
	convRegistryMu.Lock()
	convRegistry = nil
	convResults = map[string]string{}
	convRegistryPath = ""
	convRegistryMu.Unlock()
	spawnMu.Lock()
	spawnCache = map[string]SpawnResult{}
	spawnMu.Unlock()
}

const (
	rootConv  = "aaaaaaaa-0000-0000-0000-000000000001"
	childConv = "bbbbbbbb-0000-0000-0000-000000000002"
)

func seedWork(convID, parent, role string) {
	convRegistryMu.Lock()
	convRegistry = append(convRegistry, ConvRecord{ConvID: convID, ParentConv: parent, Role: role, Status: "running", Kind: convKindForRole(role)})
	convRegistryMu.Unlock()
}

func TestKindForRole(t *testing.T) {
	for role, want := range map[string]string{
		"dev_unit_3": "work", "dev_fix_2": "work", "theorize_chunk_1": "work", "spawn_work": "work",
		"dev_planner": "plan", "theory_planner": "plan", "spawn_plan": "plan",
		"review_conv": "judge", "review_unit_2": "judge", "spawn_judge": "judge", "child": "work",
	} {
		if got := convKindForRole(role); got != want {
			t.Errorf("%s: %s want %s", role, got, want)
		}
	}
}

func TestCheckSpawnPolicy(t *testing.T) {
	resetRegistry(t)
	pol := SpawnPolicy{MaxDepth: 2, MaxChildren: 2, MaxTree: 3}
	seedWork(rootConv, "", "dev_unit_1")

	if err := CheckSpawn(rootConv, "work", pol); err != nil {
		t.Fatalf("a work conversation may start a work conversation: %v", err)
	}
	if err := CheckSpawn(rootConv, "bogus", pol); err == nil {
		t.Fatal("unknown kind accepted")
	}
	// leaves never spawn
	seedWork("cccccccc-judge", rootConv, "spawn_judge")
	if err := CheckSpawn("cccccccc-judge", "work", pol); err == nil || !strings.Contains(err.Error(), "does not start") {
		t.Fatalf("a judge must not spawn: %v", err)
	}
	seedWork("dddddddd-plan", rootConv, "dev_planner")
	if err := CheckSpawn("dddddddd-plan", "judge", pol); err == nil {
		t.Fatal("a planner must not spawn")
	}
	// width: root already has 2 children
	if err := CheckSpawn(rootConv, "work", pol); err == nil || !strings.Contains(err.Error(), "already started 2") {
		t.Fatalf("width limit not enforced: %v", err)
	}
	// depth
	resetRegistry(t)
	seedWork(rootConv, "", "dev_unit_1")
	seedWork(childConv, rootConv, "spawn_work")
	seedWork("eeeeeeee-grand", childConv, "spawn_work")
	if err := CheckSpawn("eeeeeeee-grand", "work", pol); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("depth limit not enforced: %v", err)
	}
	// tree size
	resetRegistry(t)
	seedWork(rootConv, "", "dev_unit_1")
	for _, c := range []string{"c1c1c1c1", "c2c2c2c2", "c3c3c3c3"} {
		seedWork(c, rootConv, "spawn_work")
	}
	wide := SpawnPolicy{MaxDepth: 3, MaxChildren: 10, MaxTree: 3}
	if err := CheckSpawn(rootConv, "work", wide); err == nil || !strings.Contains(err.Error(), "tree") {
		t.Fatalf("tree limit not enforced: %v", err)
	}
}

func fakeSpawnSeams(t *testing.T, addrs ...string) (forks *int32, release chan struct{}) {
	var n int32
	release = make(chan struct{})
	oldF, oldC, oldW, oldR := spawnForkFn, spawnConvID, spawnWaitFn, spawnFinal
	spawnForkFn = func(role, prompt string) (int64, error) { return int64(atomic.AddInt32(&n, 1)), nil }
	spawnConvID = func(pid int64) string { return addrs[int(pid)-1] }
	spawnWaitFn = func(pid int64) { <-release }
	spawnFinal = func(pid int64) string { return "final text of " + addrs[int(pid)-1] }
	t.Cleanup(func() { spawnForkFn, spawnConvID, spawnWaitFn, spawnFinal = oldF, oldC, oldW, oldR })
	return &n, release
}

func TestSpawnConvRegistersEdgeAndIsIdempotent(t *testing.T) {
	resetRegistry(t)
	seedWork(rootConv, "", "dev_unit_1")
	forks, release := fakeSpawnSeams(t, childConv)

	r1, err := SpawnConv(rootConv, "work", "do the thing")
	if err != nil {
		t.Fatal(err)
	}
	if r1.Addr != childConv || r1.Role != "spawn_work" {
		t.Fatalf("%+v", r1)
	}
	// a replayed tool call must not start a second conversation
	r2, err := SpawnConv(rootConv, "work", "do the thing")
	if err != nil || r2.Addr != childConv || atomic.LoadInt32(forks) != 1 {
		t.Fatalf("replay started another: %+v forks=%d err=%v", r2, atomic.LoadInt32(forks), err)
	}

	m := ConvManifest(rootConv)
	if len(m.Children) != 1 || m.Children[0].Addr != childConv || m.Children[0].Status != "running" {
		t.Fatalf("manifest %+v", m)
	}
	if m.Running() != 1 {
		t.Fatal("child is running")
	}
	if WaitConvTree(rootConv, 50*time.Millisecond, 10*time.Millisecond) {
		t.Fatal("WaitConvTree returned while a descendant runs")
	}

	close(release) // the child finishes
	if !WaitConvTree(rootConv, 2*time.Second, 10*time.Millisecond) {
		t.Fatal("WaitConvTree did not see the child finish")
	}
	if txt, ok := GetConvResult(childConv); !ok || !strings.Contains(txt, "final text of") {
		t.Fatalf("result not stored: %q %v", txt, ok)
	}
}

func TestSpawnRefusedByPolicyForksNothing(t *testing.T) {
	resetRegistry(t)
	seedWork(rootConv, "", "review_conv") // a judge
	forks, _ := fakeSpawnSeams(t, childConv)
	if _, err := SpawnConv(rootConv, "work", "x"); err == nil {
		t.Fatal("judge spawned")
	}
	if atomic.LoadInt32(forks) != 0 {
		t.Fatal("a refused spawn must not fork")
	}
}

func TestSplitCommandHeredoc(t *testing.T) {
	args, body := splitCommand("einsteinos spawn work --scope 'module A, B' <<'EOF'\nline one\nline two\nEOF\nignored after")
	if strings.Join(args, "|") != "einsteinos|spawn|work|--scope|module A, B" {
		t.Fatalf("args=%v", args)
	}
	if body != "line one\nline two" {
		t.Fatalf("body=%q", body)
	}
	args, body = splitCommand("einsteinos conv aaaa result")
	if len(args) != 4 || body != "" {
		t.Fatalf("%v %q", args, body)
	}
	args, body = splitCommand(`einsteinos spawn judge <<EOF
- q?
EOF`)
	if args[2] != "judge" || body != "- q?" {
		t.Fatalf("%v %q", args, body)
	}
}

func TestEinsteinCommandFrom(t *testing.T) {
	c, ok := EinsteinCommandFrom("bash_tool", `{"command":"einsteinos conv abc"}`)
	if !ok || c != "einsteinos conv abc" {
		t.Fatalf("%q %v", c, ok)
	}
	for _, in := range []string{`{"command":"ls -la"}`, `{"command":"echo einsteinos spawn"}`, `not json`} {
		if _, ok := EinsteinCommandFrom("bash_tool", in); ok {
			t.Fatalf("%s misread as an einsteinos command", in)
		}
	}
	if _, ok := EinsteinCommandFrom("create_file", `{"command":"einsteinos conv a"}`); ok {
		t.Fatal("only bash tools carry commands")
	}
}

func TestJudgeBatchOneConversationManyQuestions(t *testing.T) {
	resetRegistry(t)
	seedWork(rootConv, "", "dev_unit_1")
	var gotPrompt string
	oldF := spawnForkFn
	fakeSpawnSeams(t, childConv)
	spawnForkFn = func(role, prompt string) (int64, error) { gotPrompt = prompt; return 1, nil }
	t.Cleanup(func() { spawnForkFn = oldF })

	out := HandleEinsteinCommand(rootConv, "einsteinos spawn judge <<'EOF'\nthe header layout is in ix_quant.h\n- does struct A match the theory?\n- is B aligned to 16?\n- is C const?\nEOF")
	var res SpawnResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Addr != childConv || res.Kind != "judge" {
		t.Fatalf("out=%s err=%v", out, err)
	}
	if !strings.Contains(gotPrompt, "[QUESTIONS] (3)") || !strings.Contains(gotPrompt, "3. is C const?") || !strings.Contains(gotPrompt, "ix_quant.h") {
		t.Fatalf("one prompt must carry all three questions and the context:\n%s", gotPrompt)
	}
	// no questions → refused, nothing forked
	if o := HandleEinsteinCommand(rootConv, "einsteinos spawn judge <<'EOF'\njust talk\nEOF"); !strings.Contains(o, "error") {
		t.Fatalf("%s", o)
	}
}

func TestParseJudgeAnswers(t *testing.T) {
	items := []JudgeItem{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	text := "ok here goes\n1: PASS: matches\n2. fail - misaligned\n2: PASS: second answer to 2 is ignored\n9: YES: out of range\n4) yes\n"
	got := ParseJudgeAnswers(text, items)
	if got[0].Verdict != "PASS" || got[0].ID != "a" {
		t.Fatalf("%+v", got)
	}
	if got[1].Verdict != "FAIL" || got[1].Reason != "misaligned" {
		t.Fatalf("%+v", got[1])
	}
	if got[2].Verdict != "NO_ANSWER" {
		t.Fatalf("an unanswered question must be NO_ANSWER, never PASS: %+v", got[2])
	}
	if got[3].Verdict != "YES" {
		t.Fatalf("%+v", got[3])
	}
}

func TestConvCommandReadsManifestAndResultByPrefix(t *testing.T) {
	resetRegistry(t)
	seedWork(rootConv, "", "dev_unit_1")
	seedWork(childConv, rootConv, "spawn_work")
	SetConvResult(childConv, "I wrote ix_quant.h")
	ConvDone(childConv, "done")

	var n ConvNode
	if err := json.Unmarshal([]byte(HandleEinsteinCommand(childConv, "einsteinos conv aaaaaaaa")), &n); err != nil {
		t.Fatal(err)
	}
	if len(n.Children) != 1 || n.Children[0].Status != "done" {
		t.Fatalf("%+v", n)
	}
	out := HandleEinsteinCommand(rootConv, "einsteinos conv bbbbbbbb result")
	if !strings.Contains(out, "I wrote ix_quant.h") || !strings.Contains(out, `"status":"done"`) {
		t.Fatal(out)
	}
	if !strings.Contains(HandleEinsteinCommand(rootConv, "einsteinos frobnicate"), "einsteinos spawn work") {
		t.Fatal("unknown subcommand should print help")
	}
}

func TestRegistryPersistsAndMarksRunningAsLost(t *testing.T) {
	resetRegistry(t)
	path := filepath.Join(t.TempDir(), "sub", "reg.json")
	SetConvRegistryPath(path)
	t.Cleanup(func() { SetConvRegistryPath("") })
	RegisterConvChild(rootConv, childConv, "o/r", "spawn_work", "work", 7)
	SetConvResult(childConv, "text")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("registry not written: %v", err)
	}
	// "restart"
	convRegistryMu.Lock()
	convRegistry = nil
	convResults = map[string]string{}
	convRegistryMu.Unlock()
	if err := LoadConvRegistry(path); err != nil {
		t.Fatal(err)
	}
	m := ConvManifest(rootConv)
	if len(m.Children) != 1 || m.Children[0].Addr != childConv {
		t.Fatalf("tree lost across restart: %+v", m)
	}
	if m.Children[0].Status != "lost" {
		t.Fatalf("a conversation that was running when the kernel died is lost, not running: %s", m.Children[0].Status)
	}
	if WaitConvTree(rootConv, time.Second, 10*time.Millisecond) != true {
		t.Fatal("a lost conversation must not block waiting forever")
	}
	if txt, _ := GetConvResult(childConv); txt != "text" {
		t.Fatalf("result lost: %q", txt)
	}
}

func TestManifestMergesTheTwoRowsOfOneConversationAndSurvivesCycles(t *testing.T) {
	resetRegistry(t)
	// TrackChildConv writes a generic row; RegisterConv later writes the role row
	convRegistryMu.Lock()
	convRegistry = append(convRegistry,
		ConvRecord{ConvID: rootConv, Role: "child", Status: "running"},
		ConvRecord{ConvID: rootConv, Role: "dev_unit_2", Repo: "o/r", Status: "running", Kind: "work"},
		ConvRecord{ConvID: childConv, ParentConv: rootConv, Role: "spawn_work", Status: "running", Kind: "work"},
	)
	convRegistryMu.Unlock()
	m := ConvManifest(rootConv)
	if m.Role != "dev_unit_2" || len(m.Children) != 1 {
		t.Fatalf("%+v", m)
	}
	ConvDone(rootConv, "done")
	if ConvManifest(rootConv).Status != "done" {
		t.Fatal("ConvDone must reach every row of a conversation")
	}
	// a cycle must not hang the kernel
	convRegistryMu.Lock()
	convRegistry = append(convRegistry, ConvRecord{ConvID: rootConv, ParentConv: childConv})
	convRegistryMu.Unlock()
	done := make(chan struct{})
	go func() { ConvManifest(rootConv); ConvDepth(childConv); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cycle hangs the registry walk")
	}
}

func TestUnregisteredConversationCannotSpawn(t *testing.T) {
	resetRegistry(t)
	forks, _ := fakeSpawnSeams(t, childConv)
	if _, err := SpawnConv("ffffffff-never-registered", "work", "x"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("%v", err)
	}
	if atomic.LoadInt32(forks) != 0 {
		t.Fatal("must not fork for an unknown parent")
	}
}

func TestDevRolesAndTheGitNotice(t *testing.T) {
	for role, want := range map[string]bool{
		"dev_unit_3": true, "dev_fix_2": true, "dev_planner": false, "review_conv": false,
		"theorize_chunk_1": false, "spawn_work": false, "hw_probe": false, "": false,
	} {
		if IsDevRole(role) != want {
			t.Errorf("%q → %v want %v", role, IsDevRole(role), want)
		}
	}
	RegisterDevRepo("o", "noticerepo", "")
	t.Cleanup(func() {
		dev_repos_mu.Lock()
		delete(dev_repos, "o/noticerepo")
		dev_repos_mu.Unlock()
	})
	n := DevGitNotice()
	for _, want := range []string{"git is not used in this phase", "/home/claude/noticerepo", "do not clone, fetch, pull or push", "the kernel pushes"} {
		if !strings.Contains(n, want) {
			t.Fatalf("notice lacks %q: %s", want, n)
		}
	}
}

func TestConvRoleOfReadsTheRegistryAndIgnoresTheGenericRow(t *testing.T) {
	resetRegistry(t)
	convRegistryMu.Lock()
	convRegistry = append(convRegistry,
		ConvRecord{ConvID: "dddddddd-0000-0000-0000-00000000000d", Role: "child", Status: "running"},
		ConvRecord{ConvID: "eeeeeeee-0000-0000-0000-00000000000e", Role: "child", Status: "running"},
		ConvRecord{ConvID: "eeeeeeee-0000-0000-0000-00000000000e", Role: "dev_unit_3", Status: "running"},
	)
	convRegistryMu.Unlock()
	if got := ConvRoleOf("eeeeeeee-0000-0000-0000-00000000000e"); got != "dev_unit_3" {
		t.Fatalf("%q", got)
	}
	if got := ConvRoleOf("dddddddd-0000-0000-0000-00000000000d"); got != "" {
		t.Fatalf("a conversation known only as 'child' has no role yet: %q", got)
	}
	if ConvRoleOf("nobody") != "" {
		t.Fatal("unknown conversation")
	}
}
