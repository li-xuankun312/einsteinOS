package kernel

import (
	"strings"
	"testing"
)

// The planner is given the data as a file and names which published numbers correspond to
// which port rate; the kernel compares only those, in the unit's direction; a wrong label,
// a wrong unit or an id out of range is ignored.
func TestPublishedNumbersAreComparedOnlyWhereThePlannerFindsThemCorresponding(t *testing.T) {
	e2e := EvalBenchRun{Tail: "prefill  3562 tokens in 6611.2 ms  ->  538.75 tok/s\ndecode  256 tokens in 2706.4 ms  ->  80.0 tok/s",
		Metrics: []Metric{{"prefill", 538.75, "tok/s", "higher"}, {"decode", 80, "tok/s", "higher"}}}
	clues := []EvalClue{
		{Repo: "Niko1221/Strata", SHA: "6801c77ff6f7", File: "bench/results/2026-09-24-final/matrix.md", Metric: Metric{"output q2_0 4k", 94.6, "tok/s", "higher"}},
		{Repo: "Niko1221/Strata", SHA: "6801c77ff6f7", File: "bench/results/2026-09-24-final/matrix.md", Metric: Metric{"prompt processing q2_0 4k", 500, "tok/s", "higher"}},
		{Repo: "o/other", SHA: "aaaaaaaaaaaa", File: "README.md", Metric: Metric{"latency", 9, "ms", "lower"}},
	}
	cond := EvalConditions{ModelName: "Qwen3.6-35B-A3B", HostMemKiB: 64 << 20}
	old := devPlannerRunner
	defer func() { devPlannerRunner = old }()
	var prompt string
	var files []ForkFile
	devPlannerRunner = func(pid int64, repo, p string) string {
		prompt, files = p, ConsumeForkFiles()
		return "comparable: 0 decode\n`comparable: 1 prefill`\ncomparable: 2 decode\ncomparable: 7 decode\ncomparable: 1 throughput\n"
	}
	cmp, _ := evalComparisons(1, "Niko1221/Strata", cond, e2e, clues, map[string]string{"Niko1221/Strata bench/results/2026-09-24-final/matrix.md": "### Output (tok/s)\n| Model | 4K |\n"})
	if !strings.Contains(prompt, "/mnt/user-data/uploads/eval_compare.md") || len(files) != 1 {
		t.Fatalf("data not given as a file: %q %d", prompt, len(files))
	}
	data := string(files[0].Data)
	for _, want := range []string{"Qwen3.6-35B-A3B", "538.75 tok/s", "0. Niko1221/Strata bench/results/2026-09-24-final/matrix.md", "### Output (tok/s)"} {
		if !strings.Contains(data, want) {
			t.Fatalf("data lacks %q", want)
		}
	}
	if len(cmp) != 2 || !cmp[0].Worse || cmp[1].Worse || cmp[0].Port.Label != "decode" {
		t.Fatalf("comparisons: %+v", cmp)
	}
	if k, txt := evalExternalGate(cmp); k != "integration_below_external" || !strings.Contains(txt, "94.6") {
		t.Fatalf("%q %s", k, txt)
	}
	if k, _ := evalExternalGate(cmp[1:]); k != "" {
		t.Fatal("not worse passes")
	}
	devPlannerRunner = func(int64, string, string) string { t.Fatal("no planner without numbers"); return "" }
	if cmp, _ := evalComparisons(1, "o/r", cond, e2e, nil, nil); cmp != nil {
		t.Fatal("nothing published, nothing compared")
	}
}

// 11/10 23:06, 23:08 and 23:33 the planner was asked the same question for three
// verifications of the integration unit. The answer is reused while the published numbers,
// the conditions and the rates' labels are the same — the rates' values are compared anew.
func TestTheSameComparisonIsAskedOnce(t *testing.T) {
	clues := []EvalClue{{Repo: "Niko1221/Strata", SHA: "e5509707730d", File: "README.md", Metric: Metric{"output q2_0 4k", 94.6, "tok/s", "higher"}}}
	cond := EvalConditions{ModelName: "Qwen3.6-35B-A3B"}
	old := devPlannerRunner
	defer func() { devPlannerRunner = old }()
	asks := 0
	devPlannerRunner = func(int64, string, string) string { asks++; ConsumeForkFiles(); return "comparable: 0 decode\n" }
	run := func(decode float64) []EvalComparison {
		e2e := EvalBenchRun{Metrics: []Metric{{"prefill", 500, "tok/s", "higher"}, {"decode", decode, "tok/s", "higher"}}}
		cmp, _ := evalComparisons(1, "Niko1221/Strata", cond, e2e, clues, nil)
		return cmp
	}
	a, b := run(80), run(120)
	if asks != 1 {
		t.Fatalf("asked %d times", asks)
	}
	if len(a) != 1 || !a[0].Worse || len(b) != 1 || b[0].Worse {
		t.Fatalf("the values are compared each time: %+v %+v", a, b)
	}
	_, tr := evalComparisons(1, "Niko1221/Strata", EvalConditions{ModelName: "other"}, EvalBenchRun{Metrics: []Metric{{"decode", 1, "tok/s", "higher"}}}, clues, nil)
	if asks != 2 || tr.Reused {
		t.Fatalf("other conditions are another question: %d", asks)
	}
}
