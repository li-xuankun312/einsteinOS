package kernel

import "testing"

// 11/04 10:16:47 u9dc1be2eae was verified and pushed; Ctrl+C at 10:16:54, before its review.
// The plan records that state so the next run starts with the review, and it survives the
// round trip through plan.json.
func TestAVerifiedUnitWaitingForItsReviewIsResumedAtTheReview(t *testing.T) {
	p := newDevPlan("ref/proj", "sha", mods("A", "B"), [][]string{{"A"}, {"B"}}, "plan")
	id := p.Leaves()[0].ID
	p.MarkReviewPending(id, map[string]string{"compile": "pass", "smoke": "pass", "noise": "x"})
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	q, err := unmarshalDevPlan(b)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := q.ReviewPending[id]
	if !ok || st["compile"] != "pass" || st["noise"] != "" {
		t.Fatalf("pending review lost or not trimmed: %v", q.ReviewPending)
	}
	if _, ok := q.TakeReviewPending(id); !ok {
		t.Fatal("take")
	}
	if _, ok := q.ReviewPending[id]; ok {
		t.Fatal("a taken review is no longer pending")
	}
}
