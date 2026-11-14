package kernel

// eval_correct.go — what the integration's end-to-end run generated, checked against the
// implementation the model's own config.json names, run here on the same model.
//
// The model directory's config.json names its architecture ("architectures":
// ["Qwen3_5MoeForCausalLM"]) — the class its publisher ships it for, in transformers. The
// kernel runs that class on the same weights, given the prompt the port was given and the
// tokens it generated (teacher forcing: one forward pass over prompt + generated[:-1]), and
// takes at each position the token it ranks first. The port's run passes only when every
// token it generated is that one. Nobody picks the judge: the model names it; nothing is
// tolerated: one different token fails.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// GateReferenceImplMissing: the implementation the model names cannot be run on this
	// machine — a fact about the machine, which no conversation can change (gateIsMachine)
	GateReferenceImplMissing     = "integration_reference_impl_missing"
	gateReferenceImplMissingFmt  = GateReferenceImplMissing + ": %[1]s, the implementation %[2]s/config.json names, could not be run on it here, so what the port generated could not be checked: %[3]s"
	gateWrongTokensFmt           = "integration_wrong_tokens: given the same prompt and the tokens before it, %[1]s (the implementation the model's config.json names) ranks first another token than the port generated at %[2]d of %[3]d position(s); the first, generated token %[4]d: the port %[5]d, %[1]s %[6]d"
	evalCorrectScriptName        = "einsteinos_teacher_forced.py"
	evalCorrectUnavailableMarker = "unavailable:"
)

// evalCorrectScript: one forward pass of the class config.json names over prompt +
// generated[:-1], the first-ranked token at each generated position printed as JSON. Exit 3
// with "unavailable: …" when the class cannot be loaded here.
const evalCorrectScript = `import json, sys
spec = json.load(open(sys.argv[1]))
try:
    import torch, transformers
    cls = getattr(transformers, spec["arch"], None) or transformers.AutoModelForCausalLM
    cuda = torch.cuda.is_available()
    model = cls.from_pretrained(spec["model"], torch_dtype=torch.bfloat16, low_cpu_mem_usage=True,
                                device_map="auto" if cuda else None)
except Exception as e:
    print("unavailable: %s: %s" % (type(e).__name__, e))
    sys.exit(3)
model.eval()
p, g = spec["prompt"], spec["generated"]
ids = torch.tensor([p + g[:-1]], device=model.device)
with torch.no_grad():
    logits = model(input_ids=ids).logits[0]
print(json.dumps({"class": type(model).__name__, "argmax": logits[len(p) - 1:].argmax(-1).tolist()}))
`

// evalTeacherForcedFn runs the model's named implementation (a variable for tests): the
// first-ranked token at each generated position, the class run, and why it could not be run.
var evalTeacherForcedFn = func(modelDir, arch string, prompt, generated []int) (argmax []int, class, unavailable string) {
	dir, err := os.MkdirTemp("", "einsteinos_correct_")
	if err != nil {
		return nil, "", err.Error()
	}
	defer os.RemoveAll(dir)
	spec, _ := json.Marshal(map[string]any{"model": modelDir, "arch": arch, "prompt": prompt, "generated": generated})
	specPath, script := filepath.Join(dir, "spec.json"), filepath.Join(dir, evalCorrectScriptName)
	os.WriteFile(specPath, spec, 0o644)
	os.WriteFile(script, []byte(evalCorrectScript), 0o644)
	out, err := runKillingGroup(evalBuildTimeout, "python3", script, specPath)
	if err != nil {
		return nil, "", tailText(strings.TrimSpace(out)+" ("+err.Error()+")", 1500)
	}
	var res struct {
		Class  string `json:"class"`
		Argmax []int  `json:"argmax"`
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if json.Unmarshal([]byte(lines[len(lines)-1]), &res) != nil {
		return nil, "", tailText(out, 1500)
	}
	return res.Argmax, res.Class, ""
}

// modelArchitecture: the first class the model directory's config.json names.
func modelArchitecture(modelDir string) string {
	b, err := os.ReadFile(filepath.Join(modelDir, "config.json"))
	if err != nil {
		return ""
	}
	var c struct {
		Architectures []string `json:"architectures"`
	}
	if json.Unmarshal(b, &c) != nil || len(c.Architectures) == 0 {
		return ""
	}
	return c.Architectures[0]
}

// evalCorrectGate checks the run's generated tokens against the model's named implementation.
// 11/09 and 11/10 the only check of what the port computed was its own chunked prefill
// against its own sequential prefill; nothing outside the pipeline had looked at its tokens.
func evalCorrectGate(br EvalBenchRun, modelDir string) (kind, text string) {
	arch := modelArchitecture(modelDir)
	if arch == "" {
		arch = "AutoModelForCausalLM"
	}
	want, class, unavailable := evalTeacherForcedFn(modelDir, arch, br.PromptIDs, br.GeneratedIDs)
	if class == "" {
		class = arch
	}
	if unavailable != "" || len(want) != len(br.GeneratedIDs) {
		if unavailable == "" {
			unavailable = fmt.Sprintf("it ranked %d position(s) for %d generated token(s)", len(want), len(br.GeneratedIDs))
		}
		return GateReferenceImplMissing, fmt.Sprintf(gateReferenceImplMissingFmt, arch, modelDir, unavailable)
	}
	first, diff := -1, 0
	for i := range want {
		if want[i] != br.GeneratedIDs[i] {
			diff++
			if first < 0 {
				first = i
			}
		}
	}
	if diff == 0 {
		return "", ""
	}
	return "integration_wrong_tokens", fmt.Sprintf(gateWrongTokensFmt, class, diff, len(want), first, br.GeneratedIDs[first], want[first])
}
