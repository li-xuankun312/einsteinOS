package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// 11/03: units 3, 4 and 5 each failed once (nothing_written, missing, smoke_fail) and then
// passed; the final summary said fix=0 for all of them.
func TestARetryIsStillOnRecordAfterThePass(t *testing.T) {
	p := newDevPlan("o/r", "s", []DevModule{{Name: "A.lean", Hash: "h", Bytes: 10}, {Name: "B.lean", Hash: "g", Bytes: 10}},
		[][]string{{"A.lean"}, {"B.lean"}}, "p")
	u3, u4 := p.Leaves()[0], p.Leaves()[1]
	p.Fail(u3.ID, DevFailure{Kind: "nothing_written"})
	p.MarkUnitPass(u3)
	p.Fail(u4.ID, DevFailure{Kind: "missing"})
	p.Fail(u4.ID, DevFailure{Kind: "smoke_fail"})
	p.MarkUnitPass(u4)

	s := p.Summary()
	if !strings.Contains(s, "id="+u3.ID+" PASS") || !strings.Contains(s, "attempts=2 failed: nothing_written") {
		t.Fatalf("%s", s)
	}
	if !strings.Contains(s, "attempts=3 failed: missing,smoke_fail") {
		t.Fatalf("%s", s)
	}
	// stored on the branch with the plan
	b, _ := json.Marshal(p)
	var back DevPlan
	json.Unmarshal(b, &back)
	if strings.Join(back.FailHistory[u4.ID], ",") != "missing,smoke_fail" {
		t.Fatalf("%v", back.FailHistory)
	}
	// a unit that passed first time says so
	q := newDevPlan("o/r", "s", []DevModule{{Name: "A.lean", Hash: "h", Bytes: 10}}, [][]string{{"A.lean"}}, "p")
	q.MarkUnitPass(q.Leaves()[0])
	if !strings.Contains(q.Summary(), "attempts=1\n") {
		t.Fatalf("%s", q.Summary())
	}
}
