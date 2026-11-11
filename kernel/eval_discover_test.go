package kernel

import (
	"strings"
	"testing"
	"time"
)

// The search runs even though the task names its reference (ref_discover returns before it
// then), the reference and the dev repo are not candidates, a candidate that does not clone
// is asked again with the same prompt, and only accepted clones are kept.
func TestEvalDiscoverRunsTheSealedSearch(t *testing.T) {
	oa, oc, os := evalAskFn, evalCloneFn, evalSizeFn
	defer func() { evalAskFn, evalCloneFn, evalSizeFn = oa, oc, os }()
	evalSizeFn = func(owner, name string) (int64, error) { return 1024, nil }
	var prompts []string
	replies := []string{"Niko1221/Strata\nggml-org/llama.cpp\nnobody/missing\n", "vllm-project/vllm\n"}
	evalAskFn = func(p string) (string, error) {
		prompts = append(prompts, p)
		r := replies[0]
		replies = replies[1:]
		return r, nil
	}
	var cloned []string
	evalCloneFn = func(owner, name, dir string) error {
		if name == "missing" {
			return errNotExist
		}
		cloned = append(cloned, dir)
		return nil
	}
	task := "项目在 zeng-bin3123/baloon，直接参考 github.com/Niko1221/Strata"
	got, _ := evalDiscover(task, []string{"Niko1221/Strata"}, func(repo string) (bool, string) {
		return repo != "vllm-project/vllm", "measured"
	})
	if len(got) != 1 || got[0].Owner+"/"+got[0].Name != "ggml-org/llama.cpp" {
		t.Fatalf("kept %v", got)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[0], task) || !strings.Contains(prompts[0], "reference repositories on github") {
		t.Fatalf("prompts: %q", prompts)
	}
	if strings.Contains(strings.Join(cloned, " "), "Strata") || !strings.Contains(strings.Join(cloned, " "), "/tmp/eval_ggml-org_llama.cpp.git") {
		t.Fatalf("clones: %v", cloned)
	}
}

var errNotExist = &notExistErr{}

type notExistErr struct{}

func (*notExistErr) Error() string { return "repository not found" }

// 11/10 the search named llvm/llvm-project (GitHub size about 2.6 GiB) and cloned it until the
// clone was killed. A repository larger than the limit is not cloned; one whose size cannot be
// read is.
func TestALargeRepositoryIsNotCloned(t *testing.T) {
	oa, oc, os := evalAskFn, evalCloneFn, evalSizeFn
	defer func() { evalAskFn, evalCloneFn, evalSizeFn = oa, oc, os }()
	evalAskFn = func(string) (string, error) { return "llvm/llvm-project\ntriton-lang/triton\nsomeone/unknown\n", nil }
	evalSizeFn = func(owner, name string) (int64, error) {
		switch name {
		case "llvm-project":
			return 2700000, nil
		case "triton":
			return 150000, nil
		}
		return 0, errNotExist
	}
	var cloned []string
	evalCloneFn = func(owner, name, dir string) error { cloned = append(cloned, owner+"/"+name); return nil }
	got, tr := evalDiscover("task", nil, func(string) (bool, string) { return true, "measured" })
	if strings.Join(cloned, " ") != "triton-lang/triton someone/unknown" {
		t.Fatalf("cloned: %v", cloned)
	}
	if len(got) != 2 || !strings.Contains(strings.Join(tr.NotTaken, " "), "llvm/llvm-project: too large to clone here (GitHub size 2636 MiB") {
		t.Fatalf("kept %v, not taken %v", got, tr.NotTaken)
	}
}

// A command whose child keeps the output pipe open is killed with its child at the timeout,
// and the call returns then — not when the child ends.
func TestATimedOutCommandIsKilledWithItsChildren(t *testing.T) {
	start := time.Now()
	_, err := runKillingGroup(time.Second, "sh", "-c", "sleep 30 & sleep 30")
	if err == nil || !strings.Contains(err.Error(), "killed after 1s") {
		t.Fatalf("err: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("returned after %v", d)
	}
	if out, err := runKillingGroup(5*time.Second, "sh", "-c", "echo ok"); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("a command that ends: %q %v", out, err)
	}
}

// A repository the search did not take is not cloned again when the search runs again.
func TestARepositoryNotTakenIsNotMeasuredAgain(t *testing.T) {
	oa, oc, os := evalAskFn, evalCloneFn, evalSizeFn
	defer func() { evalAskFn, evalCloneFn, evalSizeFn = oa, oc, os }()
	evalNotTakenMu.Lock()
	evalNotTaken = map[string]string{}
	evalNotTakenMu.Unlock()
	evalAskFn = func(string) (string, error) { return "llvm/llvm-project\n", nil }
	evalSizeFn = func(string, string) (int64, error) { return 0, errNotExist }
	clones := 0
	evalCloneFn = func(owner, name, dir string) error { clones++; return errNotExist }
	for i := 0; i < 2; i++ {
		evalDiscover("task", nil, func(string) (bool, string) { return true, "" })
	}
	if clones != 1 {
		t.Fatalf("cloned %d times", clones)
	}
	_, tr := evalDiscover("task", nil, func(string) (bool, string) { return true, "" })
	if !strings.Contains(strings.Join(tr.NotTaken, " "), "llvm/llvm-project: does not clone (repository not found) (measured before in this run)") {
		t.Fatalf("not taken: %v", tr.NotTaken)
	}
}
