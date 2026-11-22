package kernel

import (
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
