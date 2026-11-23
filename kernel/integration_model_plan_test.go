package kernel

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestThePlannerIsAskedOnceForWhatItIsGiven(t *testing.T) {
	old := devPlannerRunner
	defer func() { devPlannerRunner = old }()
	asked := 0
	var prompt string
	devPlannerRunner = func(_ int64, _, p string) string { asked++; prompt = p; return "op: a u1 k\n" }
	ms := ModelShape{Dir: "/m", Config: map[string]any{"model_type": "x"}, Tensors: []ModelTensor{{Name: "a", Dtype: "BF16", Shape: []int64{2}}}}
	data := integrationModelData(ms, map[string][]string{"u1": {"k"}}, map[string][]string{"u1": {"src/k.cu"}})
	for _, w := range []string{"### u1\nkernels: k\nfrom: src/k.cu", "  model_type=\"x\"", "  a BF16 [2] ×1"} {
		if !strings.Contains(data, w) {
			t.Fatalf("missing %q in\n%s", w, data)
		}
	}
	plan := &DevPlan{}
	direct := func(f func()) { f() }
	out, fresh := askIntegrationModel(0, "r", plan, data, "theory", 1, 1, direct, func(string) bool { return true })
	if out != "op: a u1 k\n" || !fresh || asked != 1 || len(plan.IntegrationModel) != 1 {
		t.Fatalf("first ask: %q %v %d %v", out, fresh, asked, plan.IntegrationModel)
	}
	for _, w := range []string{"[ANSWER] the kernel reads lines", "'unused: <unit id>'", "integration_model.md"} {
		if !strings.Contains(prompt, w) {
			t.Fatalf("missing %q in the planner's prompt", w)
		}
	}
	// the same question again, also after a restart (the plan's store), is not asked again
	integrationModelMu.Lock()
	integrationModelMem = map[string]string{}
	integrationModelMu.Unlock()
	if out, fresh := askIntegrationModel(0, "r", plan, data, "theory", 1, 1, direct, func(string) bool { return true }); out != "op: a u1 k\n" || fresh || asked != 1 {
		t.Fatalf("asked again: %q %v %d", out, fresh, asked)
	}
	if _, _ = askIntegrationModel(0, "r", plan, data+"x", "theory", 1, 1, direct, func(string) bool { return true }); asked != 2 {
		t.Fatalf("other data was not asked: %d", asked)
	}
}

func TestThePlannersLinesAreCheckedAgainstWhatWasMeasured(t *testing.T) {
	ms := ModelShape{Tensors: []ModelTensor{
		{Name: "m.layers.0.linear_attn.in_proj.weight", Dtype: "BF16", Shape: []int64{8, 2}},
		{Name: "m.layers.0.mlp.experts.gate_up_proj", Dtype: "BF16", Shape: []int64{4, 8, 2}},
		{Name: "m.layers.0.self_attn.q_norm.weight", Dtype: "BF16", Shape: []int64{256}},
	}}
	perUnit := map[string][]string{"u_gdn": {"gdn_step_kernel", "gdn_conv_kernel"}, "u_moe": {"moe_combine_kernel"}}
	out := "here is the mapping:\n" +
		"op: m.layers.{i}.linear_attn.in_proj.weight u_gdn gdn_step_kernel\n" +
		"- `op: m.layers.{i}.mlp.experts.gate_up_proj u_moe moe_combine_kernel`\n" +
		"op: m.layers.{i}.mlp.experts.gate_up_proj u_moe not_a_kernel\n" +
		"op: m.layers.{i}.nope.weight u_gdn gdn_step_kernel\n" +
		"op: m.layers.{i}.linear_attn.in_proj.weight u_none gdn_step_kernel\n" +
		"missing: m.layers.{i}.self_attn.q_norm.weight per-head RMSNorm of q before rope\n" +
		"missing: m.layers.{i}.other.weight something\n"
	p := parseIntegrationModel(out, ms, perUnit)
	if len(p.Ops) != 3 {
		t.Fatalf("want 2 ops and 1 missing, got %+v", p.Ops)
	}
	blk := p.Block()
	for _, w := range []string{"2 tensor-group use(s) by a unit's kernel, 1 by none",
		"  m.layers.{i}.linear_attn.in_proj.weight: u_gdn gdn_step_kernel\n",
		"  m.layers.{i}.mlp.experts.gate_up_proj: u_moe moe_combine_kernel\n",
		"  m.layers.{i}.self_attn.q_norm.weight: no unit — per-head RMSNorm of q before rope\n"} {
		if !strings.Contains(blk, w) {
			t.Fatalf("missing %q in\n%s", w, blk)
		}
	}
	if (IntegrationModelPlan{}).Block() != "" {
		t.Fatal("no answer should insert nothing")
	}
}

func TestUnitsTheModelDoesNotUseAreNotAskedOfTheEndToEndRun(t *testing.T) {
	ms := ModelShape{Tensors: []ModelTensor{{Name: "m.layers.0.linear_attn.in_proj.weight", Dtype: "BF16", Shape: []int64{8, 2}}}}
	perUnit := map[string][]string{"u_gdn": {"gdn_step_kernel"}, "u_hc": {"gr_mix_kernel"}, "u_ple": {"ple_kernel"}}
	p := parseIntegrationModel("op: m.layers.{i}.linear_attn.in_proj.weight u_gdn gdn_step_kernel\nunused: u_ple\nunused: u_hc\nunused: u_gdn\nunused: u_nope\n", ms, perUnit)
	if strings.Join(p.Unused, " ") != "u_hc u_ple" {
		t.Fatalf("unused: %v (a unit an op line uses, or one not in the data, is not unused)", p.Unused)
	}
	if !strings.Contains(p.Block(), "2 unit(s) whose kernels this model's forward does not need, so the end-to-end run is not asked to launch them: u_hc u_ple") {
		t.Fatalf("%s", p.Block())
	}
	if all := parseIntegrationModel("unused: u_gdn\nunused: u_hc\nunused: u_ple\n", ms, perUnit); all.Unused != nil {
		t.Fatalf("every unit unused: %v", all.Unused)
	}
	// the end-to-end gates ask only the used units' kernels
	integrationAnswersMu.Lock()
	integrationAnswers["uint_t"] = p
	integrationAnswersMu.Unlock()
	defer func() {
		integrationAnswersMu.Lock()
		delete(integrationAnswers, "uint_t")
		integrationAnswersMu.Unlock()
	}()
	br := EvalBenchRun{Bench: EvalBench{File: "e2e.sh"}, Kernels: []string{"gdn_step_kernel"}}
	full := perUnit
	used := map[string][]string{}
	for u, k := range full {
		if !hasString(p.Unused, u) {
			used[u] = k
		}
	}
	if k, _ := e2ePortGate(br, full); k != "integration_e2e_not_port" {
		t.Fatalf("every unit asked: %q", k)
	}
	if k, txt := e2ePortGate(br, used); k != "" {
		t.Fatalf("the used units' kernels ran: %q %s", k, txt)
	}
}

func TestThePartsOfTheProgramAreGivenWithTheSpawnLimits(t *testing.T) {
	p := parseIntegrationModel("part: loader: maps the safetensors shards and uploads each tensor\npart: : nothing\npart: gdn layers: linear attention with the gdn kernels\n", ModelShape{}, nil)
	if len(p.Parts) != 2 {
		t.Fatalf("%v", p.Parts)
	}
	pol := DefaultSpawnPolicy()
	blk := p.Block()
	for _, w := range []string{fmt.Sprintf("in the planner's 2 part(s) (the conversation commands start at most %d work conversations from this one, %d in its tree)", pol.MaxChildren, pol.MaxTree),
		"  loader: maps the safetensors shards and uploads each tensor\n", "  gdn layers: linear attention with the gdn kernels\n"} {
		if !strings.Contains(blk, w) {
			t.Fatalf("missing %q in\n%s", w, blk)
		}
	}
}

func TestTheAnswersCountsAreWhatWasTakenAndDropped(t *testing.T) {
	ms := ModelShape{Tensors: []ModelTensor{{Name: "m.0.w", Dtype: "BF16", Shape: []int64{2}}}}
	perUnit := map[string][]string{"u1": {"k1"}, "u2": {"k2"}}
	p := parseIntegrationModel("op: m.{i}.w u1 k1\nop: m.{i}.w u1 nope\nmissing: m.{i}.w x\nmissing: zz y\nunused: u2\nunused: u9\npart: a: b\npart: c\nnot a line\n", ms, perUnit)
	if got := p.counts(); got != "op 1, missing 1, unused 1 (u2), part 1, dropped 4" {
		t.Fatalf("%q", got)
	}
}

// 11/22 the integration conversation started none of the planner's parts: the kernel starts
// them under it, one work conversation each, as far as the spawn policy allows, once
func TestTheKernelStartsThePartsUnderTheIntegrationConversation(t *testing.T) {
	oldFork, oldID, oldWait, oldFinal := spawnForkFn, spawnConvID, spawnWaitFn, spawnFinal
	defer func() { spawnForkFn, spawnConvID, spawnWaitFn, spawnFinal = oldFork, oldID, oldWait, oldFinal }()
	var prompts []string
	next := int64(9000)
	spawnForkFn = func(role, prompt string) (int64, error) {
		if role != "spawn_work" {
			t.Fatalf("role %q", role)
		}
		prompts = append(prompts, prompt)
		next++
		return next, nil
	}
	spawnConvID = func(pid int64) string { return fmt.Sprintf("%08d-0000-0000-0000-000000000000", pid) }
	spawnWaitFn = func(int64) {}
	spawnFinal = func(int64) string { return "" }
	parent := "aaaabbbb-0000-0000-0000-00000000000f"
	RegisterConv(0, parent, "r", "dev_fix_5_uintegration_t", 44)
	parts := []string{"loader: maps the shards", "gdn layers: linear attention", "moe layers: experts"}
	got := startIntegrationParts("uintegration_t", parent, parts, "[MODEL — measured]\n")
	if len(got) != 3 || len(prompts) != 3 {
		t.Fatalf("started %d (%d prompts)", len(got), len(prompts))
	}
	for i, p := range prompts {
		if !strings.Contains(p, "[SCOPE] "+parts[i]) || !strings.Contains(p, "[MODEL — measured]") {
			t.Fatalf("part %d's prompt:\n%s", i, p)
		}
	}
	if again := startIntegrationParts("uintegration_t", parent, parts, "[MODEL — measured]\n"); again != nil || len(prompts) != 3 {
		t.Fatalf("the same parts were started again: %v", again)
	}
	if none := startIntegrationParts("uintegration_t", "", []string{"x: y"}, ""); none != nil {
		t.Fatal("no parent, and parts started")
	}
}

// 11/22 14:09:43 the planner declined to answer in the kernel's lines, and its text was kept in
// plan.json as the answer and given back on every start
func TestAnAnswerTheKernelReadsNothingOfIsNotKept(t *testing.T) {
	old := devPlannerRunner
	defer func() { devPlannerRunner = old }()
	asked := 0
	refusal := "I can see what's happening here, and I want to be straightforward about it. I won't."
	devPlannerRunner = func(int64, string, string) string { asked++; return refusal }
	direct := func(f func()) { f() }
	takes := func(a string) bool { return strings.Contains(a, "op:") }
	plan := &DevPlan{}
	out, fresh := askIntegrationModel(0, "r", plan, "data-untaken", "t", 1, 1, direct, takes)
	if out != refusal || fresh || len(plan.IntegrationModel) != 0 || asked != 1 {
		t.Fatalf("%q fresh=%v plan=%v asked=%d", out, fresh, plan.IntegrationModel, asked)
	}
	// not asked again in this run
	if _, _ = askIntegrationModel(0, "r", plan, "data-untaken", "t", 1, 1, direct, takes); asked != 1 {
		t.Fatalf("asked again after a declined answer: %d", asked)
	}
	// one kept in plan.json before is dropped when read
	integrationModelMu.Lock()
	integrationModelMem = map[string]string{}
	integrationModelMu.Unlock()
	key := integrationModelKey("data-untaken", "t")
	plan.IntegrationModel = map[string]string{key: refusal}
	devPlannerRunner = func(int64, string, string) string { asked++; return "op: a u k\n" }
	out, fresh = askIntegrationModel(0, "r", plan, "data-untaken", "t", 1, 1, direct, takes)
	if out != "op: a u k\n" || !fresh || asked != 2 || plan.IntegrationModel[key] != "op: a u k\n" {
		t.Fatalf("the stored refusal was used: %q fresh=%v asked=%d plan=%v", out, fresh, asked, plan.IntegrationModel)
	}
	if got := firstSentence(refusal); got != "I can see what's happening here, and I want to be straightforward about it" {
		t.Fatalf("%q", got)
	}
}

// 11/23 01:53:37 six parts were started at once beside the integration conversation and at
// 02:19:23 the server refused one for the account's concurrency: parts run at most
// integrationPartsAtOnce at once, the next started when one ends, an errored one started again
func TestThePartsRunAFewAtOnceAndAnErroredOneAgain(t *testing.T) {
	oldFork, oldID, oldWait, oldFinal, oldAt, oldPoll := spawnForkFn, spawnConvID, spawnWaitFn, spawnFinal, integrationPartsAtOnce, integrationPartsPoll
	defer func() {
		spawnForkFn, spawnConvID, spawnWaitFn, spawnFinal, integrationPartsAtOnce, integrationPartsPoll = oldFork, oldID, oldWait, oldFinal, oldAt, oldPoll
	}()
	integrationPartsAtOnce = func() int { return 2 }
	integrationPartsPoll = 10 * time.Millisecond
	var mu sync.Mutex
	var started []string
	release := map[int64]chan string{}
	next := int64(9100)
	spawnForkFn = func(role, prompt string) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		next++
		for _, p := range []string{"embed", "layer", "head"} {
			if strings.Contains(prompt, "[SCOPE] "+p+":") {
				started = append(started, p)
			}
		}
		release[next] = make(chan string, 1)
		return next, nil
	}
	spawnConvID = func(pid int64) string { return fmt.Sprintf("%08d-0000-0000-0000-00000000000a", pid) }
	finals := map[int64]string{}
	spawnWaitFn = func(pid int64) {
		mu.Lock()
		ch := release[pid]
		mu.Unlock()
		out := <-ch
		mu.Lock()
		finals[pid] = out
		mu.Unlock()
	}
	spawnFinal = func(pid int64) string { mu.Lock(); defer mu.Unlock(); return finals[pid] }
	parent := "aaaabbbb-0000-0000-0000-0000000000a1"
	RegisterConv(0, parent, "r", "dev_fix_7_uintegration_q", 44)
	got := startIntegrationParts("uintegration_q", parent, []string{"embed: a", "layer: b", "head: c"}, "body\n")
	if len(got) != 2 {
		t.Fatalf("started at once: %d", len(got))
	}
	snapshot := func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(started, " ") }
	time.Sleep(50 * time.Millisecond)
	if s := snapshot(); s != "embed layer" {
		t.Fatalf("a third part started with two running: %s", s)
	}
	mu.Lock()
	release[9101] <- "[Error] {\"type\":\"error\"}" // embed ended with an error
	mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && snapshot() != "embed layer head" {
		time.Sleep(10 * time.Millisecond)
	}
	if s := snapshot(); s != "embed layer head" {
		t.Fatalf("the next part was not started when one ended: %s", s)
	}
	mu.Lock()
	release[9102] <- "done" // layer ended
	mu.Unlock()
	for time.Now().Before(deadline) && snapshot() != "embed layer head embed" {
		time.Sleep(10 * time.Millisecond)
	}
	if s := snapshot(); s != "embed layer head embed" {
		t.Fatalf("the errored part was not started again: %s", s)
	}
	mu.Lock()
	for _, ch := range release {
		select {
		case ch <- "done":
		default:
		}
	}
	mu.Unlock()
}

// 11/23 06:29:25 the part 8f60a081 ended with an error and was not started again: its text ends
// with the error, it does not begin with it
func TestAConversationsErrorIsReadFromTheEndOfItsText(t *testing.T) {
	out := "I read the cache's header and the slot arena. Next I…\n[Error] {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"Internal server error\"}}"
	if why := conversationError(out); !strings.HasPrefix(why, "[Error] {\"type\":\"error\"") {
		t.Fatalf("%q", why)
	}
	if why := conversationError("done: the sampler's tests pass"); why != "" {
		t.Fatalf("%q", why)
	}
	if why := conversationError("worked\n[API Error] Post \"…/completion\": status 502"); !strings.HasPrefix(why, "[API Error]") {
		t.Fatalf("%q", why)
	}
}

// 11/23 06:29:35 the queued parts were refused "this conversation already started 6 (limit 6)":
// the parts the kernel starts are bounded by how many run at once, not by the six children a
// conversation may start itself
func TestMoreThanSixPartsStartOneAfterAnother(t *testing.T) {
	oldFork, oldID, oldWait, oldFinal, oldAt, oldPoll := spawnForkFn, spawnConvID, spawnWaitFn, spawnFinal, integrationPartsAtOnce, integrationPartsPoll
	defer func() {
		spawnForkFn, spawnConvID, spawnWaitFn, spawnFinal, integrationPartsAtOnce, integrationPartsPoll = oldFork, oldID, oldWait, oldFinal, oldAt, oldPoll
	}()
	integrationPartsAtOnce = func() int { return 6 }
	integrationPartsPoll = 10 * time.Millisecond
	var mu sync.Mutex
	started := 0
	next := int64(9300)
	spawnForkFn = func(role, prompt string) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		next++
		started++
		return next, nil
	}
	spawnConvID = func(pid int64) string { return fmt.Sprintf("%08d-0000-0000-0000-00000000000b", pid) }
	spawnWaitFn = func(int64) {} // each part ends at once
	spawnFinal = func(int64) string { return "done" }
	parent := "aaaabbbb-0000-0000-0000-0000000000b1"
	RegisterConv(0, parent, "r", "dev_fix_7_uintegration_r", 44)
	var parts []string
	for i := 0; i < 10; i++ {
		parts = append(parts, fmt.Sprintf("p%d: part %d", i, i))
	}
	startIntegrationParts("uintegration_r", parent, parts, "body\n")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := started
		mu.Unlock()
		if n == 10 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("%d of 10 parts started", started)
}

// 11/23 the planner was asked once at 01:53; the verifications after it never reached it: its
// data carries the integration unit's last verification and its own previous answer, and the
// answer it gives is the one the next question carries
func TestThePlannerIsToldTheLastVerificationAndItsPreviousAnswer(t *testing.T) {
	fb := integrationFeedback(DevFailure{Kind: "integration_direction_closed", Text: "the processes of e2e.sh are the direction python3.10 pytools/e2e_infer.py [port], closed"}, "op: a u k\npart: loader: reads the shards")
	for _, w := range []string{"## the integration unit's last verification", "gate: integration_direction_closed", "pytools/e2e_infer.py [port], closed", "## the previous answer to this question", "part: loader: reads the shards"} {
		if !strings.Contains(fb, w) {
			t.Fatalf("missing %q in\n%s", w, fb)
		}
	}
	if integrationFeedback(DevFailure{}, "") != "" {
		t.Fatal("nothing to tell, and something told")
	}
	old := devPlannerRunner
	defer func() { devPlannerRunner = old }()
	devPlannerRunner = func(int64, string, string) string { return "op: a u k\n" }
	plan := &DevPlan{}
	direct := func(f func()) { f() }
	takes := func(a string) bool { return strings.Contains(a, "op:") }
	askIntegrationModel(0, "r", plan, "data-feedback-1", "t", 1, 1, direct, takes)
	if plan.IntegrationModelLast == "" || plan.IntegrationModel[plan.IntegrationModelLast] != "op: a u k\n" {
		t.Fatalf("the answer read is not the last: %q %v", plan.IntegrationModelLast, plan.IntegrationModel)
	}
}
