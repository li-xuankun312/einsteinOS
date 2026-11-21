package claudeweb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 11/17 11:28:09 review_conv (ada945f4) ran "curl -s --connect-timeout 5 https://axle.axiommath.ai/"
// to see whether Axle was reachable: its 142-byte HTML page was taken for a broken answer, the
// command was run 30 times, and the conversation was told the network had failed. A page asked
// for as a page is returned the first time.
func TestAPageOfAxleIsReturnedTheFirstTime(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	fake := "#!/bin/sh\necho x >> " + calls + "\necho '<!DOCTYPE html><html><head><title>Axle</title></head></html>'\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	out := axleExecWithRetryCurl("curl -s --connect-timeout 5 https://axle.axiommath.ai/ 2>&1 | head -5", "")
	b, _ := os.ReadFile(calls)
	if n := strings.Count(string(b), "x"); n != 1 {
		t.Fatalf("the page was asked for %d times", n)
	}
	if !strings.Contains(out, "<title>Axle</title>") || strings.HasPrefix(out, axleCurlExhaustedPrefix) {
		t.Fatalf("%q", out)
	}
}
