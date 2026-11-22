package kernel

import (
	"fmt"
	"strings"
	"testing"
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
	out, fresh := askIntegrationModel(0, "r", plan, data, "theory", 1, 1, direct)
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
	if out, fresh := askIntegrationModel(0, "r", plan, data, "theory", 1, 1, direct); out != "op: a u1 k\n" || fresh || asked != 1 {
		t.Fatalf("asked again: %q %v %d", out, fresh, asked)
	}
	if _, _ = askIntegrationModel(0, "r", plan, data+"x", "theory", 1, 1, direct); asked != 2 {
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
