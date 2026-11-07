package kernel

import (
	"strings"
	"testing"
)

// The search runs even though the task names its reference (ref_discover returns before it
// then), the reference and the dev repo are not candidates, a candidate that does not clone
// is asked again with the same prompt, and only accepted clones are kept.
func TestEvalDiscoverRunsTheSealedSearch(t *testing.T) {
	oa, oc := evalAskFn, evalCloneFn
	defer func() { evalAskFn, evalCloneFn = oa, oc }()
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
	got := evalDiscover(task, []string{"Niko1221/Strata"}, func(repo string) (bool, string) {
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
