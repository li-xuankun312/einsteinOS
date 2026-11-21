package kernel

import (
	"strings"
	"testing"
)

// 11/20 01:25–02:49 the integration unit's port passed every gate, the kernel built the
// reference for 21 minutes and the search ran 56 more — and the unit had never written
// e2e.sh: whatever they measured, the verification could only end integration_e2e_missing.
// A missing e2e.sh is judged before the reference is built or anything is searched.
func TestAMissingE2EIsJudgedBeforeTheReferenceIsMeasured(t *testing.T) {
	bootKernel()
	work := t.TempDir()
	oldFmt, oldAsk := refBareDirFmt, evalAskFn
	defer func() { refBareDirFmt, evalAskFn = oldFmt, oldAsk }()
	refBareDirFmt = work + "/ref_%[1]s_%[2]s.git"
	asked := false
	evalAskFn = func(string) (string, error) { asked = true; return "", nil }
	sha := "01529aaa82b9aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fn := evalIntegration(0, "Niko1221/Strata", sha, "o/nodev", "uintegration01529aaa82b9", nil, func(s string) string { return s })
	k, txt := fn(t.TempDir(), nil)
	if k != "integration_e2e_missing" || !strings.Contains(txt, ".einsteinOS/verify/uintegration01529aaa82b9/e2e.sh") {
		t.Fatalf("gate %q %s", k, txt)
	}
	if asked {
		t.Fatal("the repositories were searched for an integration with no e2e.sh")
	}
	evalBaseMu.Lock()
	_, measured := evalBaseCache["Niko1221/Strata@"+sha]
	evalBaseMu.Unlock()
	if measured {
		t.Fatal("the reference was measured for an integration with no e2e.sh")
	}
}
