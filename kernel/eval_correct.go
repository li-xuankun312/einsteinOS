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
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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

// teacherForcedRun runs the teacher-forcing script with python (a variable for tests).
var teacherForcedRun = func(python, script, spec string) (string, error) {
	return runKillingGroup(evalBuildTimeout, python, script, spec)
}

// teacherForcedWith: the first-ranked tokens python's transformers gives, the class it ran,
// and why it could not be run.
func teacherForcedWith(python, modelDir, arch string, prompt, generated []int) (argmax []int, class, unavailable string) {
	dir, err := os.MkdirTemp("", "einsteinos_correct_")
	if err != nil {
		return nil, "", err.Error()
	}
	defer os.RemoveAll(dir)
	spec, _ := json.Marshal(map[string]any{"model": modelDir, "arch": arch, "prompt": prompt, "generated": generated})
	specPath, script := filepath.Join(dir, "spec.json"), filepath.Join(dir, evalCorrectScriptName)
	os.WriteFile(specPath, spec, 0o644)
	os.WriteFile(script, []byte(evalCorrectScript), 0o644)
	out, err := teacherForcedRun(python, script, specPath)
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

// judgeEnvForFn: the judge environment for the model, measured and built (eval_judge.go); a
// variable for tests.
var judgeEnvForFn = func(modelDir, arch string) (python, versions string, err error) {
	key := modelDir + "|" + arch
	if why, ok := judgeFailedRecently(key, time.Now()); ok {
		return "", "", why
	}
	started := time.Now()
	python, versions, err = judgeEnvFor(modelDir, arch)
	if err != nil {
		judgeNoteFailed(key, err, started, time.Now())
	}
	return python, versions, err
}

// judgeFailHold: how long a judge environment that could not be measured or built is not tried
// again. 11/22 16:31:53-16:58:29 the integration's verification spent 27 minutes fetching
// transformers wheels through the proxy until one ended in EOF, and the next verification of
// the same tree began the same.
const judgeFailHold = time.Hour

// %[1]v the error, %[2]s when that attempt started, %[3]s how long it took, %[4]s until when
const judgeFailedFmt = "%[1]v (the attempt at %[2]s, %[3]s, failed so; not tried again before %[4]s)"

var (
	judgeFailMu sync.Mutex
	judgeFail   = map[string]struct {
		err      error
		at, took time.Time
	}{}
)

func judgeNoteFailed(key string, err error, started, ended time.Time) {
	judgeFailMu.Lock()
	defer judgeFailMu.Unlock()
	judgeFail[key] = struct {
		err      error
		at, took time.Time
	}{err, started, ended}
}

// judgeFailedRecently: the stored failure of key, when it is younger than judgeFailHold.
func judgeFailedRecently(key string, now time.Time) (error, bool) {
	judgeFailMu.Lock()
	defer judgeFailMu.Unlock()
	f, ok := judgeFail[key]
	if !ok || now.Sub(f.at) >= judgeFailHold {
		return nil, false
	}
	return fmt.Errorf(judgeFailedFmt, f.err, f.at.Format("15:04:05"), f.took.Sub(f.at).Round(time.Second), f.at.Add(judgeFailHold).Format("15:04:05")), true
}

// judgeEnvFor measures the judge's environment for the model and builds it.
func judgeEnvFor(modelDir, arch string) (python, versions string, err error) {
	out, err := judgeRunFn(os.Environ(), "python3", "-c", "import sys; print('%d.%d.%d' % sys.version_info[:3])")
	if err != nil {
		return "", "", fmt.Errorf("python3's version: %v: %s", err, tailText(out, 200))
	}
	mt := modelType(modelDir)
	if mt == "" {
		return "", "", fmt.Errorf("%s/config.json names no model_type", modelDir)
	}
	spec, err := judgeSpecFor(mt, strings.TrimSpace(out))
	if err != nil {
		return "", "", err
	}
	log.Printf("eval_correct: %s is shipped from transformers %s; the judge runs %s (Requires-Python %s, PyTorch >= %s) for python %s",
		mt, spec.FirstWithModel, spec.Transformers, spec.RequiresPython, spec.Torch, spec.Python)
	return buildJudgeEnv(spec, arch)
}

// evalTeacherForcedFn runs the model's named implementation (a variable for tests): the
// first-ranked token at each generated position, the class run, and why it could not be run.
// It runs only in the judge environment built for it (eval_judge.go), a venv of the kernel's
// own under its home: never this machine's python3, whose packages the conversations write to.
// 11/22 23:47 the integration's conversation wrote pytools/patch_import_utils.py into e2e.sh to
// change this machine's transformers until it loaded the model on torch 2.1 — the judge, which
// tried this machine's python3 first, would have checked the port against what the
// conversation had made of the model's implementation.
var evalTeacherForcedFn = func(modelDir, arch string, prompt, generated []int) (argmax []int, class, unavailable string) {
	python, versions, err := judgeEnvForFn(modelDir, arch)
	if err != nil {
		return nil, "", fmt.Sprintf("the judge environment: %v", err)
	}
	argmax, class, un := teacherForcedWith(python, modelDir, arch, prompt, generated)
	if un != "" {
		return nil, "", fmt.Sprintf("%s (%s): %s", python, versions, un)
	}
	log.Printf("eval_correct: the judge ran %s in %s (%s)", arch, python, versions)
	return argmax, fmt.Sprintf("%s (%s, on the CPU)", class, versions), ""
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
// evalTokensAgreed: the generated tokens, from the first, that the last correctness check
// found agreeing with the model's own implementation (a direction's measure, eval_direction.go).
var evalTokensAgreed int

func evalCorrectGate(br EvalBenchRun, modelDir string) (kind, text string) {
	evalTokensAgreed = 0
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
	evalTokensAgreed = first
	if diff == 0 {
		evalTokensAgreed = len(want)
		return "", ""
	}
	return "integration_wrong_tokens", fmt.Sprintf(gateWrongTokensFmt, class, diff, len(want), first, br.GeneratedIDs[first], want[first])
}
