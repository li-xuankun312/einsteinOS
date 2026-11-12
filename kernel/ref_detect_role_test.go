package kernel

import (
	"strings"
	"testing"
)

// The conversation that tells the dev repository from the references is forked with its
// role, so it ends as an answer: unnamed, on 11/12 it ran the task itself.
func TestTellingTheDevRepositoryIsForkedWithItsRole(t *testing.T) {
	var roles []string
	t.Cleanup(func() {
		dev_repos_mu.Lock()
		delete(dev_repos, "zeng-bin3123/baloon")
		dev_repos_mu.Unlock()
	})
	m := useSeqModel(t, func(prompt string) string {
		roles = append(roles, ConsumeForkRole())
		return "dev=zeng-bin3123/baloon\nbranch=main"
	})
	detect_dev_repos("项目在 zeng-bin3123/baloon，分支是 main，注意看这个项目：github.com/zeng-bin3123/baloon 所以直接参考github.com/Niko1221/Strata  Niko1221/Strata  。")
	if len(m.prompts) != 1 || len(roles) != 1 || roles[0] != "detect_dev" {
		t.Fatalf("prompts=%d roles=%v", len(m.prompts), roles)
	}
	if !is_dev_repo("zeng-bin3123", "baloon") || DevRepoBranch("zeng-bin3123", "baloon") != "main" {
		t.Fatal("its two lines register the dev repository")
	}
	if s := TerminationForRole("detect_dev").String(); !strings.Contains(s, "MaxRounds(1)") {
		t.Fatalf("its answer is one round: %s", s)
	}
}
