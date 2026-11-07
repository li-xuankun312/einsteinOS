package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- helpers --------------------------------------------------------------------------------

type openCall struct {
	title, file string
	keep        bool
}

// liveEnv turns live on in screen mode (without a real screen) and records the windows
// that WOULD open.
func liveEnv(t *testing.T, after string) (*[]openCall, *sync.Mutex, string) {
	t.Helper()
	bootKernel()
	dir := t.TempDir()
	t.Setenv("EINSTEINOS_LIVE", "screen")
	t.Setenv("STY", "123.test")
	t.Setenv("EINSTEINOS_LIVE_DIR", dir)
	t.Setenv("EINSTEINOS_LIVE_AFTER", after)
	t.Setenv("EINSTEINOS_LIVE_MAX", "4")
	liveMu.Lock()
	liveOpenCount, liveFailedKept = 0, 0
	liveMu.Unlock()
	var calls []openCall
	var mu sync.Mutex
	old := liveOpenWindow
	liveOpenWindow = func(cfg liveCfg, title, file string, keep bool) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, openCall{title, file, keep})
		return nil
	}
	t.Cleanup(func() { liveOpenWindow = old })
	return &calls, &mu, dir
}

func runLive(t *testing.T, command string) (stdout string, exit int) {
	t.Helper()
	pid, err := ForkExec(command, "")
	if err != nil {
		t.Fatal(err)
	}
	cpid, code, err := WaitTimeout(pid, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // finish() runs in the same goroutine that closes Done; let it settle
	return GetResult(cpid), code
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- configuration -----------------------------------------------------------------------------

func TestLiveIsOffByDefaultAndChangesNothing(t *testing.T) {
	bootKernel()
	t.Setenv("EINSTEINOS_LIVE", "")
	dir := t.TempDir()
	t.Setenv("EINSTEINOS_LIVE_DIR", dir)
	if liveBegin(1, "", "echo hi") != nil {
		t.Fatal("live must be off unless asked for")
	}
	out, code := runLive(t, "echo hi; exit 2")
	if out != "hi\n" || code != 2 {
		t.Fatalf("a command's result and exit code must be exactly what they were: %q %d", out, code)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("files created while off: %v", ents)
	}
}

func TestLiveModeSelection(t *testing.T) {
	cases := []struct{ live, sty, tmux, want string }{
		{"", "x", "x", ""},
		{"off", "x", "x", ""},
		{"screen", "", "", ""}, // asked for screen, not in one: nothing opens, nothing breaks
		{"screen", "1.n", "", "screen"},
		{"tmux", "1.n", "", ""},
		{"tmux", "", "/tmp/tmux-0/default,1,0", "tmux"},
		{"auto", "1.n", "/tmp/tmux", "screen"},
		{"auto", "", "/tmp/tmux", "tmux"},
		{"auto", "", "", ""},
		{"AUTO ", "1.n", "", "screen"},
	}
	for _, c := range cases {
		t.Setenv("EINSTEINOS_LIVE", c.live)
		t.Setenv("STY", c.sty)
		t.Setenv("TMUX", c.tmux)
		if got := liveConfig().mode; got != c.want {
			t.Errorf("live=%q STY=%q TMUX=%q → %q want %q", c.live, c.sty, c.tmux, got, c.want)
		}
	}
	t.Setenv("EINSTEINOS_LIVE_AFTER", "250ms")
	t.Setenv("EINSTEINOS_LIVE_MAX", "7")
	if c := liveConfig(); c.after != 250*time.Millisecond || c.max != 7 {
		t.Fatalf("%+v", c)
	}
	t.Setenv("EINSTEINOS_LIVE_AFTER", "garbage")
	if liveConfig().after != 3*time.Second {
		t.Fatal("a bad value falls back to the default")
	}
}

// ---- the follow-file ------------------------------------------------------------------------------

func TestLiveWriterBuffersLinesAndRedacts(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "w")
	w := &liveWriter{f: f}
	w.Write([]byte("compiling a.cu\ncompil"))
	w.Write([]byte("ing b.cu\ncurl -b provertools_session=abcdefabcdefabcdefabcdefabcdef x\nlast line no newline"))
	w.close()
	b, _ := os.ReadFile(f.Name())
	got := string(b)
	for _, want := range []string{"compiling a.cu\n", "compiling b.cu\n", "last line no newline\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "abcdefabcdefabcdefabcdefabcdef") {
		t.Fatalf("a secret reached the follow-file: %q", got)
	}
	// writing after close is harmless
	if _, err := w.Write([]byte("late\n")); err != nil {
		t.Fatal(err)
	}
}

func TestLiveLabel(t *testing.T) {
	if got := liveLabel(88123, "", "export PATH=/usr/local/corex/bin:$PATH; bash .einsteinOS/verify/u1a2b3c4d5e/build.sh 2>&1"); got != "pid88123 verify u1a2b3c4d5e build" {
		t.Fatalf("a verification names its unit: %q", got)
	}
	if got := liveLabel(7, "", "make -j8\nall"); got != "pid7 make -j8 all" {
		t.Fatalf("%q", got)
	}
	long := liveLabel(9, "x"+strings.Repeat("y", 200), "")
	if len(long) > 40 {
		t.Fatalf("title %d chars", len(long))
	}
	if strings.ContainsAny(liveLabel(1, "a\x1bb\tc", ""), "\x1b\t") {
		t.Fatal("control characters in a window title")
	}
	if strings.Contains(liveLabel(1, "curl -b provertools_session=abcdefabcdefabcdefabcdefabcdef", ""), "abcdefabcdef") {
		t.Fatal("a secret in a window title")
	}
	// screen expands $VAR in -X arguments: a title must not carry one
	if got := liveLabel(1, "echo $HOME `id`", ""); strings.ContainsAny(got, "$`") {
		t.Fatalf("%q", got)
	}
}

// ---- when windows open --------------------------------------------------------------------------------

func TestOnlySlowCommandsGetAWindow(t *testing.T) {
	calls, mu, dir := liveEnv(t, "300ms")

	// a quick command that works: no window, no file left behind
	out, code := runLive(t, "echo quick")
	if out != "quick\n" || code != 0 {
		t.Fatalf("%q %d", out, code)
	}
	time.Sleep(400 * time.Millisecond) // past the timer: it was stopped
	mu.Lock()
	n := len(*calls)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("a quick command opened a window")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("a quick successful command left files: %v", ents)
	}

	// a slow one: one window, and its file has the command and its output
	out, code = runLive(t, "echo start; sleep 0.9; echo end")
	if out != "start\nend\n" || code != 0 {
		t.Fatalf("%q %d", out, code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*calls) != 1 {
		t.Fatalf("a slow command must open exactly one window, got %d", len(*calls))
	}
	c := (*calls)[0]
	b, _ := os.ReadFile(c.file)
	if !strings.HasPrefix(string(b), "$ echo start; sleep 0.9; echo end\n") || !strings.Contains(string(b), "start\nend\n") {
		t.Fatalf("follow-file: %q", b)
	}
	if e, _ := os.ReadFile(c.file + ".exit"); strings.TrimSpace(string(e)) != "0" {
		t.Fatalf(".exit = %q: the window waits for it", e)
	}
	if !strings.HasPrefix(c.title, "pid") {
		t.Fatalf("title %q", c.title)
	}
}

func TestAFailedCommandKeepsItsFileAndItsWindow(t *testing.T) {
	calls, mu, _ := liveEnv(t, "100ms")
	out, code := runLive(t, "echo compiling; sleep 0.4; echo 'error: undefined symbol' >&2; exit 3")
	if code != 3 || !strings.Contains(out, "compiling") {
		t.Fatalf("the exit code and result must be untouched: %d %q", code, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*calls) != 1 || !(*calls)[0].keep {
		t.Fatalf("a window that will hold a failure must be told to stay: %+v", *calls)
	}
	b, _ := os.ReadFile((*calls)[0].file)
	if !strings.Contains(string(b), "error: undefined symbol") {
		t.Fatalf("stderr must be in the follow-file too: %q", b)
	}
	if e, _ := os.ReadFile((*calls)[0].file + ".exit"); strings.TrimSpace(string(e)) != "3" {
		t.Fatalf(".exit = %q", e)
	}
	if liveFailedKept != 1 {
		t.Fatalf("failed windows kept = %d", liveFailedKept)
	}
}

func TestPastTheCapFailuresCloseOnTheirOwn(t *testing.T) {
	calls, mu, _ := liveEnv(t, "50ms")
	liveMu.Lock()
	liveFailedKept = liveKeepFailedMax
	liveMu.Unlock()
	runLive(t, "sleep 0.3; exit 1")
	mu.Lock()
	defer mu.Unlock()
	if len(*calls) != 1 || (*calls)[0].keep {
		t.Fatalf("beyond %d kept failures a window must close like a success: %+v", liveKeepFailedMax, *calls)
	}
}

func TestNoMoreThanMaxWindowsAtOnce(t *testing.T) {
	calls, mu, _ := liveEnv(t, "100ms")
	t.Setenv("EINSTEINOS_LIVE_MAX", "2")
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runLive(t, "sleep 0.8; echo done")
		}()
	}
	wg.Wait()
	mu.Lock()
	n := len(*calls)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("5 slow commands at once with max 2 must open 2 windows, got %d", n)
	}
	liveMu.Lock()
	open := liveOpenCount
	liveMu.Unlock()
	if open != 0 {
		t.Fatalf("open count must return to 0 when the commands end, got %d", open)
	}
}

func TestAWindowThatCannotOpenGivesBackItsSlotAndNothingBreaks(t *testing.T) {
	_, _, _ = liveEnv(t, "100ms")
	liveOpenWindow = func(liveCfg, string, string, bool) error { return os.ErrPermission }
	out, code := runLive(t, "sleep 0.4; echo fine")
	if out != "fine\n" || code != 0 {
		t.Fatalf("a failing window must not touch the command: %q %d", out, code)
	}
	liveMu.Lock()
	defer liveMu.Unlock()
	if liveOpenCount != 0 {
		t.Fatalf("open count %d", liveOpenCount)
	}
}

func TestOldFollowFilesAreCleanedUp(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.log")
	fresh := filepath.Join(dir, "fresh.log")
	os.WriteFile(old, nil, 0o600)
	os.WriteFile(fresh, nil, 0o600)
	os.Chtimes(old, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))
	liveCleanup(dir, 24*time.Hour)
	if _, err := os.Stat(old); err == nil {
		t.Fatal("a two-day-old file must go")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("a fresh file must stay")
	}
}

// ---- the window itself ---------------------------------------------------------------------------------

func runWindowScript(t *testing.T, exitCode string, keep string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(f, []byte("$ make\ncompiling kernels\n"), 0o600)
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.WriteFile(f, []byte("$ make\ncompiling kernels\nlast line\n"), 0o600)
		os.WriteFile(f+".exit", []byte(exitCode), 0o600)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", liveWindowScript, "_", f, "0", "pid1 make", keep)
	cmd.Stdin = strings.NewReader("") // "press Enter" returns at once
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the window script hung: %q", out)
	}
	_ = err
	return string(out)
}

func TestWindowScriptFollowsThenClosesOnSuccess(t *testing.T) {
	out := runWindowScript(t, "0", "1")
	if !strings.Contains(out, "compiling kernels") || !strings.Contains(out, "[exit 0") || !strings.Contains(out, "closing") {
		t.Fatalf("%q", out)
	}
	if strings.Contains(out, "FAILED") {
		t.Fatalf("a success is not a failure: %q", out)
	}
}

func TestWindowScriptStaysOnFailureOnlyWhenAskedTo(t *testing.T) {
	out := runWindowScript(t, "5", "1")
	if !strings.Contains(out, "[exit 5") || !strings.Contains(out, "FAILED") || !strings.Contains(out, "press Enter") {
		t.Fatalf("a kept failure says so and waits for Enter: %q", out)
	}
	out = runWindowScript(t, "5", "0")
	if strings.Contains(out, "press Enter") || !strings.Contains(out, "closing") {
		t.Fatalf("past the cap a failure closes like a success: %q", out)
	}
}

// ---- the real thing, in a real screen ---------------------------------------------------------------------

var screenNameRe = regexp.MustCompile(`(\d+\.livee2e\w*)\s`)

func TestRealScreenWindowOpensForASlowCommandAndClosesAfterwards(t *testing.T) {
	if _, err := exec.LookPath("screen"); err != nil {
		t.Skip("screen is not installed")
	}
	bootKernel()
	name := "livee2e" + strings.ReplaceAll(t.Name(), "/", "")[:0]
	if err := exec.Command("screen", "-dmS", name+"a", "bash", "-c", "sleep 60").Run(); err != nil {
		t.Skipf("cannot start a detached screen here: %v", err)
	}
	var sty string
	waitFor(t, "the screen session", func() bool {
		out, _ := exec.Command("screen", "-ls").CombinedOutput()
		if m := screenNameRe.FindStringSubmatch(string(out)); m != nil {
			sty = m[1]
			return true
		}
		return false
	})
	t.Cleanup(func() { exec.Command("screen", "-S", sty, "-X", "quit").Run() })

	t.Setenv("EINSTEINOS_LIVE", "screen")
	t.Setenv("STY", sty)
	t.Setenv("EINSTEINOS_LIVE_DIR", t.TempDir())
	t.Setenv("EINSTEINOS_LIVE_AFTER", "300ms")
	t.Setenv("EINSTEINOS_LIVE_LINGER", "1s")
	liveMu.Lock()
	liveOpenCount, liveFailedKept = 0, 0
	liveMu.Unlock()

	windows := func() string {
		out, _ := exec.Command("screen", "-S", sty, "-Q", "windows").CombinedOutput()
		return string(out)
	}
	pid, err := ForkExec("for i in 1 2 3 4 5 6; do echo line $i; sleep 0.3; done", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the live window to appear while the command runs", func() bool { return strings.Contains(windows(), "pid") })
	if _, code, err := WaitTimeout(pid, time.Minute); err != nil || code != 0 {
		t.Fatalf("exit %d %v", code, err)
	}
	waitFor(t, "the window to close itself after the command and the linger", func() bool { return !strings.Contains(windows(), "pid") })
}

func TestTheWindowScriptIsAFileWithNoDollarsPassedThroughScreen(t *testing.T) {
	dir := t.TempDir()
	p, err := liveWindowScriptFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != liveWindowScript {
		t.Fatal("the file must hold the script")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	// everything that goes through screen's -X: no $ for it to expand
	for _, a := range []string{p, "/tmp/einsteinos_live/12_345.log", "3", "pid12 make -j8", "1"} {
		if strings.Contains(a, "$") {
			t.Fatalf("%q would be rewritten by screen", a)
		}
	}
	// a second call keeps the file as it is
	if p2, _ := liveWindowScriptFile(dir); p2 != p {
		t.Fatal(p2)
	}
}

func screenSession(t *testing.T) (sty string, windows func() string) {
	t.Helper()
	if _, err := exec.LookPath("screen"); err != nil {
		t.Skip("screen is not installed")
	}
	bootKernel()
	if err := exec.Command("screen", "-dmS", "livee2e", "bash", "-c", "sleep 60").Run(); err != nil {
		t.Skipf("cannot start a detached screen here: %v", err)
	}
	waitFor(t, "the screen session", func() bool {
		out, _ := exec.Command("screen", "-ls").CombinedOutput()
		if m := screenNameRe.FindStringSubmatch(string(out)); m != nil {
			sty = m[1]
			return true
		}
		return false
	})
	t.Cleanup(func() { exec.Command("screen", "-S", sty, "-X", "quit").Run() })
	t.Setenv("EINSTEINOS_LIVE", "screen")
	t.Setenv("STY", sty)
	t.Setenv("EINSTEINOS_LIVE_DIR", t.TempDir())
	t.Setenv("EINSTEINOS_LIVE_AFTER", "300ms")
	t.Setenv("EINSTEINOS_LIVE_LINGER", "1s")
	liveMu.Lock()
	liveOpenCount, liveFailedKept = 0, 0
	liveMu.Unlock()
	return sty, func() string {
		out, _ := exec.Command("screen", "-S", sty, "-Q", "windows").CombinedOutput()
		return string(out)
	}
}

// the one that matters when a build breaks: the window must still be there, saying FAIL
func TestRealScreenFailedCommandLeavesItsWindowUntilEnter(t *testing.T) {
	sty, windows := screenSession(t)
	pid, err := ForkExec("echo compiling quant.cu; sleep 0.8; echo 'quant.cu(3): error: undefined symbol' >&2; exit 2", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the window", func() bool { return strings.Contains(windows(), "pid") })
	if _, code, _ := WaitTimeout(pid, time.Minute); code != 2 {
		t.Fatalf("exit %d", code)
	}
	waitFor(t, "the window title to say FAIL", func() bool { return strings.Contains(windows(), "FAIL") })
	time.Sleep(2500 * time.Millisecond) // well past the linger a success would get
	if !strings.Contains(windows(), "FAIL") {
		t.Fatalf("a failed command's window must stay until Enter, windows: %q", windows())
	}
	// pressing Enter in that window closes it (window numbers: 0 is the bash, 1 the live window)
	exec.Command("screen", "-S", sty, "-p", "1", "-X", "stuff", "\r").Run()
	waitFor(t, "the window to close on Enter", func() bool { return !strings.Contains(windows(), "FAIL") })
}

func TestRealTmuxWindowOpensForASlowCommandAndClosesAfterwards(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	bootKernel()
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	if err := exec.Command("tmux", "-S", sock, "new-session", "-d", "-s", "livee2e", "sleep 60").Run(); err != nil {
		t.Skipf("cannot start tmux here: %v", err)
	}
	t.Cleanup(func() { exec.Command("tmux", "-S", sock, "kill-server").Run() })
	// $TMUX tells the process which server it is in: socket path,pid,session
	t.Setenv("TMUX", sock+",1,0")
	t.Setenv("STY", "")
	t.Setenv("EINSTEINOS_LIVE", "tmux")
	t.Setenv("EINSTEINOS_LIVE_DIR", t.TempDir())
	t.Setenv("EINSTEINOS_LIVE_AFTER", "300ms")
	t.Setenv("EINSTEINOS_LIVE_LINGER", "1s")
	liveMu.Lock()
	liveOpenCount, liveFailedKept = 0, 0
	liveMu.Unlock()
	windows := func() string {
		out, _ := exec.Command("tmux", "-S", sock, "list-windows", "-F", "#{window_name}").CombinedOutput()
		return string(out)
	}
	pid, err := ForkExec("for i in 1 2 3 4 5 6; do echo line $i; sleep 0.3; done", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tmux window", func() bool { return strings.Contains(windows(), "pid") })
	if _, code, _ := WaitTimeout(pid, time.Minute); code != 0 {
		t.Fatalf("exit %d", code)
	}
	waitFor(t, "the tmux window to close itself", func() bool { return !strings.Contains(windows(), "pid") })
}

// the real machine verification is the longest command there is: it must get a window named for its unit
func TestUnitVerificationGetsItsOwnNamedWindow(t *testing.T) {
	calls, mu, _ := liveEnv(t, "200ms")
	ws := t.TempDir()
	build, test := devVerifyScripts("uabc123")
	mustWrite(ws, build, "echo compiling quant_1.cu\nsleep 0.7\necho built\n")
	mustWrite(ws, test, "echo running tests\nsleep 0.5\necho ok\n")
	oldRunner := devVerifyRunner
	// the real runner: ForkExec on this machine (the other tests replace it)
	devVerifyRunner = func(command, dir string, timeout time.Duration) (int, string, error) {
		pid, err := ForkExec(command, dir)
		if err != nil {
			return -1, "", err
		}
		cpid, code, err := WaitTimeout(pid, timeout)
		if err != nil {
			return -1, "", err
		}
		return code, GetResult(cpid) + GetStderr(cpid), nil
	}
	t.Cleanup(func() { devVerifyRunner = oldRunner })

	r := runUnitVerify(ws, "uabc123", "", time.Minute, time.Minute)
	if !r.Passed() {
		t.Fatalf("%+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*calls) != 2 {
		t.Fatalf("build and test are two slow commands: %d windows", len(*calls))
	}
	if !strings.Contains((*calls)[0].title, "verify uabc123 build") || !strings.Contains((*calls)[1].title, "verify uabc123 test") {
		t.Fatalf("titles: %q, %q", (*calls)[0].title, (*calls)[1].title)
	}
}
