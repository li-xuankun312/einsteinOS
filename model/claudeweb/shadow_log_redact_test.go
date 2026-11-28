package claudeweb

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// What this machine runs is logged without the credentials it carries. 11/20 01:53:07 and
// every two minutes after, the log said "[pid 898] exec: TOKEN=ghp_cMgQ… python3 …" and
// "[pid 898] still running after 26m0s: TOKEN=ghp_cMgQ…" — the token in plain text.
func TestTheLocalCommandIsLoggedWithoutItsToken(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	cmd := "TOKEN=ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789\n# Get ALL new commits\npython3 << 'EOF'\nimport urllib.request\nEOF"
	logExec(898, cmd)
	logStillRunning(898, 26*time.Minute, cmd)
	if strings.Contains(buf.String(), "ghp_AbCd") || !strings.Contains(buf.String(), "[pid 898] exec:") || !strings.Contains(buf.String(), "still running after 26m0s") {
		t.Fatalf("%s", buf.String())
	}
}
