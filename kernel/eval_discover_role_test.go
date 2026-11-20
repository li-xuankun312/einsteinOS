package kernel

import (
	"strings"
	"testing"
)

// The evaluation's repository search is forked with its role and ends as the search
// ref_discover is: 11/20 01:51:53 conversation 1db7c0db was forked without one, so it ran
// under the default termination — Or(BashExitZero("git push"), MaxRounds(20)) — ran a
// script of its own on this machine for 44 minutes and ended after pushing three commits to
// the dev repository's main.
func TestTheEvaluationSearchIsForkedWithItsRole(t *testing.T) {
	var roles []string
	m := useSeqModel(t, func(prompt string) string {
		roles = append(roles, ConsumeForkRole())
		return "Niko1221/Strata\nzeng-bin3123/baloon"
	})
	got, err := evalAskFn("search prompt")
	if err != nil || len(m.prompts) != 1 || len(roles) != 1 || roles[0] != "eval_discover" {
		t.Fatalf("err=%v prompts=%d roles=%v", err, len(m.prompts), roles)
	}
	if !strings.Contains(got, "Niko1221/Strata") {
		t.Fatalf("its answer: %q", got)
	}
	s := TerminationForRole("eval_discover").String()
	if strings.Contains(s, "git push") || s != TerminationForRole("ref_discover").String() {
		t.Fatalf("eval_discover ends as ref_discover does: %s vs %s", s, TerminationForRole("ref_discover"))
	}
}
