package kernel

import (
	"strings"
	"testing"
)

// An answer that names only repositories the search does not compare is asked again, and the
// prompt asked again carries what was not taken and why: 11/20 01:51:53 conversation 1db7c0db
// answered Niko1221/Strata (the reference) and zeng-bin3123/baloon (the dev repository), and
// the search ended there with nothing to compare.
func TestAnAnswerOfOnlyExcludedRepositoriesIsAskedAgain(t *testing.T) {
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
	replies := []string{"Niko1221/Strata\nzeng-bin3123/baloon\n", "ggml-org/llama.cpp\n"}
	evalAskFn = func(p string) (string, error) {
		prompts = append(prompts, p)
		if len(replies) == 0 {
			return "", nil
		}
		r := replies[0]
		replies = replies[1:]
		return r, nil
	}
	got, tr := evalDiscover("{}", []string{"Niko1221/Strata"}, func(string) (bool, string) { return true, "measured" })
	if len(prompts) < 2 || len(got) != 1 || got[0].Owner+"/"+got[0].Name != "ggml-org/llama.cpp" {
		t.Fatalf("asked %d times, kept %v, not taken %v", len(prompts), got, tr.NotTaken)
	}
	for _, want := range []string{"Niko1221/Strata: " + evalNotTakenReference, "zeng-bin3123/baloon: " + evalNotTakenDevRepo} {
		if !strings.Contains(prompts[1], want) || !strings.Contains(strings.Join(tr.NotTaken, "\n"), want) {
			t.Fatalf("asked again without %q:\n%s\nnot taken %v", want, prompts[1], tr.NotTaken)
		}
	}
}
