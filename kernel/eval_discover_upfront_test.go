package kernel

import (
	"strings"
	"testing"
)

// The first prompt of the search already carries the repositories it does not compare and
// why: 11/20 01:51:53 conversation 1db7c0db was given the measured conditions with the
// reference's name in them ("ref_repo": "Niko1221/Strata") and nothing saying the reference
// and the dev repository are not compared, and it answered with exactly those two.
func TestTheSearchIsToldUpFrontWhatItDoesNotCompare(t *testing.T) {
	resetEvalSearch()
	oa, oc, os := evalAskFn, evalCloneFn, evalSizeFn
	defer func() { evalAskFn, evalCloneFn, evalSizeFn = oa, oc, os }()
	RegisterDevRepo("zeng-bin3123", "baloon", "main")
	t.Cleanup(func() {
		dev_repos_mu.Lock()
		delete(dev_repos, "zeng-bin3123/baloon")
		dev_repos_mu.Unlock()
	})
	evalSizeFn = func(owner, name string) (int64, error) { return 1024, nil }
	evalCloneFn = func(owner, name, dir string) error { return nil }
	var prompts []string
	evalAskFn = func(p string) (string, error) {
		prompts = append(prompts, p)
		return "ggml-org/llama.cpp\n", nil
	}
	cond := `{"model_name":"Qwen3.6-35B-A3B","ref_repo":"Niko1221/Strata"}`
	evalDiscover(cond, []string{"Niko1221/Strata"}, func(string) (bool, string) { return true, "measured" })
	if len(prompts) != 1 || !strings.Contains(prompts[0], cond) {
		t.Fatalf("prompts: %q", prompts)
	}
	for _, want := range []string{"Niko1221/Strata: " + evalNotTakenReference, "zeng-bin3123/baloon: " + evalNotTakenDevRepo} {
		if !strings.Contains(prompts[0], want) {
			t.Fatalf("the first prompt lacks %q:\n%s", want, prompts[0])
		}
	}
}
