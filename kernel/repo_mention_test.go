package kernel

import "testing"

// A repository name ends where GitHub's name characters end, even with no space after it.
func TestARepoMentionEndsAtTheFirstCharacterGitHubDoesNotAllow(t *testing.T) {
	rs := extractRepoMentions("注意看这个项目：github.com/zeng-bin3123/baloon我们要开发的是 ilu 级别，参考 github.com/Niko1221/Strata。")
	if len(rs) != 2 || rs[0].Owner != "zeng-bin3123" || rs[0].Name != "baloon" || rs[1].Name != "Strata" {
		t.Fatalf("%+v", rs)
	}
}
