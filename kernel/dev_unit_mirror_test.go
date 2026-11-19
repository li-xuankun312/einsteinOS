package kernel

import (
	"strings"
	"testing"
)

// Each unit's conversations work in a mirror of their own, and are told so: two units that run
// side by side never write into each other's tree. Until 11/19 every unit shared
// /home/claude/<repo>. No twin is cloned into a unit's mirror.
func TestEachUnitWorksInAMirrorOfItsOwn(t *testing.T) {
	a := devUnitMirrorDir("zeng-bin3123/baloon", "u316bd6b9ec")
	b := devUnitMirrorDir("zeng-bin3123/baloon", "u52d156705c")
	if a == b || !strings.HasPrefix(a, "/home/claude/baloon-") {
		t.Fatalf("%q %q", a, b)
	}
	blk := newDevRepoFacts("zeng-bin3123/baloon", "einsteinOS/dev-abc123", a).Block()
	if !strings.Contains(blk, a) {
		t.Fatalf("the conversation is not told its mirror:\n%s", blk)
	}
	dev_repos_mu.Lock()
	old, had := dev_repos["zeng-bin3123/baloon"]
	dev_repos["zeng-bin3123/baloon"] = ""
	dev_repos_mu.Unlock()
	defer func() {
		dev_repos_mu.Lock()
		if had {
			dev_repos["zeng-bin3123/baloon"] = old
		} else {
			delete(dev_repos, "zeng-bin3123/baloon")
		}
		dev_repos_mu.Unlock()
	}()
	if twinDirAllowed(a) || twinDirAllowed(a+"/src") {
		t.Fatalf("a twin may be cloned into a unit's mirror")
	}
}
