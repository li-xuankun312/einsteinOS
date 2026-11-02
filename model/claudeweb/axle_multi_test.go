package claudeweb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsMultiRequestCommand(t *testing.T) {
	cases := map[string]bool{
		// the 2026/11/02 09:25 command: a loop with a curl in it
		`cd /mnt/user-data/outputs; for f in *.lean; do echo "== $f"; curl -s https://axle.axiommath.ai/api/v1/verify_proof -d @/tmp/req.json; done`: true,
		`while read f; do curl -s x; done < list`:                     true,
		`ls *.lean | xargs -P4 -I{} curl -s x`:                        true,
		`curl -s a > a.json; curl -s b > b.json`:                      true,  // two requests in one call
		`curl -s https://axle.axiommath.ai/api/v1/verify_proof -d @x`: false, // one request: the old schedule fits
		`cat > f.lean <<'EOF'\ntheorem x : True := trivial\nEOF`:      false,
		`echo "format the transform of the data"`:                     false, // "for" inside a word is not a loop
		`curl -s x | python3 -c 'print(1)'`:                           false,
	}
	for cmd, want := range cases {
		if got := isMultiRequestCommand(cmd); got != want {
			t.Errorf("%q → %v want %v", cmd, got, want)
		}
	}
}

func TestIdleGuardKillsAStallButNotASlowCommandThatKeepsPrinting(t *testing.T) {
	// 5 s in total, but it prints every second: never idle for 2 s
	run := axleRunGuarded(`for i in 1 2 3 4 5; do echo "== $i"; sleep 1; done`, "", false, 30*time.Second, 2*time.Second)
	if run.TimedOut || !strings.Contains(run.Stdout, "== 5") {
		t.Fatalf("a command that keeps printing must not be killed: %+v", run)
	}
	// prints once, then hangs
	t0 := time.Now()
	run = axleRunGuarded(`echo started; sleep 30`, "", false, 60*time.Second, 1*time.Second)
	if !run.TimedOut || !strings.Contains(run.Stdout, "started") {
		t.Fatalf("a stall must be killed and keep what was printed: %+v", run)
	}
	if time.Since(t0) > 6*time.Second {
		t.Fatalf("the stall took %s to be noticed", time.Since(t0))
	}
	// the fixed total still applies when idle is off
	run = axleRunOnce(`sleep 30`, "", false, 1*time.Second)
	if !run.TimedOut {
		t.Fatal("the total timeout must still work")
	}
}

// a fake curl, so no network is needed: the function is defined inside the command and counts its runs
func multiCmd(counter, body string) string {
	return "echo run >> " + counter + "; " + body
}

func TestALoopThatIsSlowButProgressingRunsOnceAndReturnsEverything(t *testing.T) {
	t.Setenv("EINSTEIN_AXLE_IDLE", "2s")
	counter := filepath.Join(t.TempDir(), "runs")
	// 6 "requests" of ~1 s each: 6 s in total, far past the old fixed 15 s? no — but past any short guard we set here
	cmd := multiCmd(counter, `curl(){ sleep 1; echo '{"okay":true}'; }; for f in a b c d e f; do echo "== $f"; curl; done`)
	out := axleExecWithRetryCurl(cmd, "")
	if !strings.HasPrefix(out, "[pid 0 exit=0]") || !strings.Contains(out, "== f") {
		t.Fatalf("the whole loop's output must come back: %q", out)
	}
	if n := strings.Count(mustRead(t, counter), "run"); n != 1 {
		t.Fatalf("the command ran %d times: a loop must run once, not once per retry", n)
	}
}

func TestALoopThatStallsMidwayReturnsWhatItHadAndIsNotRerun(t *testing.T) {
	t.Setenv("EINSTEIN_AXLE_IDLE", "1s")
	counter := filepath.Join(t.TempDir(), "runs")
	cmd := multiCmd(counter, `curl(){ if [ "$1" = c ]; then sleep 30; else echo '{"okay":true}'; fi; }; for f in a b c d; do echo "== $f"; curl $f; done`)
	t0 := time.Now()
	out := axleExecWithRetryCurl(cmd, "")
	if time.Since(t0) > 8*time.Second {
		t.Fatalf("took %s", time.Since(t0))
	}
	for _, want := range []string{"exit=124", "== a", "== b", "== c", "redo only what is missing"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "== d") {
		t.Fatalf("d never ran: %q", out)
	}
	if n := strings.Count(mustRead(t, counter), "run"); n != 1 {
		t.Fatalf("a loop that made progress and then stalled must NOT be run again from the start (ran %d times)", n)
	}
}

func TestACommandThatNeverPrintsIsTriedThreeTimesThenGivesUp(t *testing.T) {
	t.Setenv("EINSTEIN_AXLE_IDLE", "1s")
	counter := filepath.Join(t.TempDir(), "runs")
	cmd := multiCmd(counter, `curl(){ sleep 30; }; for f in a b; do curl $f; done`)
	t0 := time.Now()
	out := axleExecWithRetryCurl(cmd, "")
	if !strings.HasPrefix(out, axleCurlExhaustedPrefix) || !strings.Contains(out, "network problem") {
		t.Fatalf("%q", out)
	}
	if n := strings.Count(mustRead(t, counter), "run"); n != 3 {
		t.Fatalf("zero-output stalls are tried 3 times (preferred path, other path, preferred), ran %d", n)
	}
	if time.Since(t0) > 15*time.Second {
		t.Fatalf("took %s: 3 × the idle guard, not 30 × a fixed guard", time.Since(t0))
	}
}

func TestADeterministicFailureOfALoopIsNotRetried(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs")
	cmd := multiCmd(counter, `for f in a b; do definitely_not_a_command_curl_$f; done`)
	out := axleExecWithRetryCurl(cmd, "")
	if !strings.Contains(out, "exit=") {
		t.Fatalf("%q", out)
	}
	if n := strings.Count(mustRead(t, counter), "run"); n != 1 {
		t.Fatalf("retrying a missing command helps nobody: ran %d times", n)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return string(b)
}
