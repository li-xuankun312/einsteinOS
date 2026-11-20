package kernel

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuf is a log sink the verification's goroutines can write to while the test reads it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// 11/20 01:25:04 the integration unit's fourth verification started and at 01:30:38 its build
// had run; its compile/smoke/gate line never came: the line is written when the verification
// returns, and the integration's evaluation, which runs inside it, was still building the
// reference and then searching at 02:49. That its gates had passed and the evaluation had
// started is logged before the evaluation runs.
func TestTheEvaluationsStartIsLoggedBeforeItRuns(t *testing.T) {
	bootKernel()
	ws := t.TempDir()
	d := filepath.Join(ws, ".einsteinOS/verify/uint1")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "build.sh"), []byte("exit 0\n"), 0o755)
	os.WriteFile(filepath.Join(d, "test.sh"), []byte("exit 0\n"), 0o755)
	var buf syncBuf
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	seen := ""
	RegisterIntegrationEval("uint1", func(string, []EvalBench) (string, string) {
		seen = buf.String()
		return "", ""
	})
	defer UnregisterIntegrationEval("uint1")
	res := runUnitVerifyWith(ws, "uint1", "", UnitRequirements{}, []string{".einsteinOS/verify/uint1/build.sh"}, time.Minute, time.Minute)
	if res.Smoke != "pass" {
		t.Fatalf("smoke %s: %s", res.Smoke, res.TestOut)
	}
	if !strings.Contains(seen, "dev_verify: unit uint1: compile=pass smoke=pass, every gate passed — the integration's evaluation starts") {
		t.Fatalf("logged before the evaluation:\n%s", seen)
	}
}
