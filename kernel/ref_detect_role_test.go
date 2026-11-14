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

// The repository conversations read the task with its credentials masked: 11/12 the
// dev-repository question pushed with the token the task carried.
func TestTheRepositoryConversationsReadNoToken(t *testing.T) {
	var prompts []string
	useSeqModel(t, func(prompt string) string {
		ConsumeForkRole()
		prompts = append(prompts, prompt)
		return ""
	})
	tok := "ghp_" + strings.Repeat("A1b2", 9)
	detect_dev_repos("项目在 github.com/zeng-bin3123/baloon，参考 github.com/Niko1221/Strata，api限流就用这个: 20210820209@stu.usc.edu.cn zeng-bin3123 " + tok)
	if len(prompts) != 1 {
		t.Fatalf("prompts=%d", len(prompts))
	}
	if strings.Contains(prompts[0], tok) || strings.Contains(prompts[0], "20210820209@") {
		t.Fatalf("the token reached the conversation:\n%s", prompts[0])
	}
	if !strings.Contains(prompts[0], "zeng-bin3123/baloon") {
		t.Fatal("the repositories are still there")
	}
}
