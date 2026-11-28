package kernel

import "testing"

// Each integrated commit has its record; its previous record is the commit integrated before
// it; the first has none; writing a commit again keeps its place in the order.
func TestEvalRecordsKeepTheIntegrationOrder(t *testing.T) {
	dir := t.TempDir()
	if prevEvalRecord(dir, "aaa") != nil {
		t.Fatal("no record yet")
	}
	m := func(v float64) EvalRun {
		return EvalRun{Benches: []EvalBenchRun{{Metrics: []Metric{{Label: "decode", Value: v, Unit: "tok/s", Better: "higher"}}}}}
	}
	for i, sha := range []string{"aaa", "bbb", "ccc"} {
		if err := writeEvalRecord(dir, EvalRecord{Repo: "o/r", RefSHA: sha, Dev: m(float64(10 * (i + 1)))}); err != nil {
			t.Fatal(err)
		}
	}
	writeEvalRecord(dir, EvalRecord{Repo: "o/r", RefSHA: "bbb", Dev: m(21)})
	if idx := readEvalIndex(dir); len(idx) != 3 || idx[1] != "bbb" {
		t.Fatalf("index %v", idx)
	}
	if prevEvalRecord(dir, "aaa") != nil {
		t.Fatal("the first commit has no previous record")
	}
	p := prevEvalRecord(dir, "ccc")
	if p == nil || p.RefSHA != "bbb" || p.Dev.Metrics()[0].Value != 21 {
		t.Fatalf("previous of ccc: %+v", p)
	}
	if p := prevEvalRecord(dir, "ddd"); p == nil || p.RefSHA != "ccc" {
		t.Fatalf("a commit not yet integrated follows the last one: %+v", p)
	}
}
