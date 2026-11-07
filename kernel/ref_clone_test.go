package kernel

import "testing"

// 11/05 03:17:40 dev_unit_14 cloned the reference; the kernel tried to mirror it
func TestACloneOfTheReferenceIsCountedNotMirrored(t *testing.T) {
	prMu.Lock()
	prTable[987654] = &PRProcess{RefRepos: []RefRepo{{Owner: "Niko1221", Name: "Strata"}}}
	prMu.Unlock()
	defer func() { prMu.Lock(); delete(prTable, 987654); prMu.Unlock() }()
	if !isReferenceURL("https://github.com/Niko1221/Strata.git") || isReferenceURL("https://github.com/zeng-bin3123/baloon") {
		t.Fatal("reference recognised by URL")
	}
	n := ReferenceClones()
	GitTwinSync("cd /home/claude && git clone https://github.com/Niko1221/Strata /home/claude/Strata", "/home/claude")
	if ReferenceClones() != n+1 || func() bool { twinMu.Lock(); defer twinMu.Unlock(); return twinDirs["/home/claude/Strata"] }() {
		t.Fatal("counted, not mirrored")
	}
}
