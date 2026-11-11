package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// The evaluation record says what the search and the comparison did. 11/09 the first record on
// the Iluvatar machine (zeng-bin3123/baloon einsteinOS/state, .einsteinOS/eval/6801c77f…json)
// held "external": null and "compared": null, and could not tell whether the search had run.
func TestTheRecordSaysWhatTheSearchAndTheComparisonDid(t *testing.T) {
	resetEvalSearch()
	oa, oc := evalAskFn, evalCloneFn
	defer func() { evalAskFn, evalCloneFn = oa, oc }()
	replies := []string{"Niko1221/Strata\nggml-org/llama.cpp\nnobody/missing\nvllm-project/vllm\n", "\n"}
	evalAskFn = func(string) (string, error) {
		if len(replies) == 0 {
			return "", nil
		}
		r := replies[0]
		replies = replies[1:]
		return r, nil
	}
	evalCloneFn = func(owner, name, dir string) error {
		if name == "missing" {
			return errNotExist
		}
		return nil
	}
	taken, tr := evalDiscover("task", []string{"Niko1221/Strata"}, func(repo string) (bool, string) {
		if repo == "vllm-project/vllm" {
			return false, "no runnable benchmark measured"
		}
		return true, "1 benchmark(s)"
	})
	if len(taken) != 1 || tr.Asked < 1 || len(tr.Named) != 4 || len(tr.Taken) != 1 || len(tr.NotTaken) != 3 {
		t.Fatalf("taken %v trace %+v", taken, tr)
	}
	joined := strings.Join(tr.NotTaken, "\n")
	for _, want := range []string{"Niko1221/Strata: the reference", "nobody/missing: does not clone", "vllm-project/vllm: no runnable benchmark measured"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("not taken lacks %q:\n%s", want, joined)
		}
	}

	cmp, ct := evalComparisons(1, "o/r", EvalConditions{}, EvalBenchRun{}, nil, nil)
	if cmp != nil || ct.Asked || ct.NotAsked == "" {
		t.Fatalf("no numbers: the planner is not asked, and the record says why: %+v", ct)
	}
	b, _ := json.Marshal(EvalRecord{Discover: tr, Compare: ct})
	for _, k := range []string{`"discover":{"asked"`, `"not_taken":[`, `"compare":{"published":0`, `"not_asked":"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("record lacks %s: %s", k, b)
		}
	}
}
