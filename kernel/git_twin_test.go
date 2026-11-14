package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSplitSegments(t *testing.T) {
	cases := map[string]string{
		"cd /tmp && git clone u d && ls":                         "cd /tmp|git clone u d|ls",
		"git log | head -5; echo done":                           "git log|head -5|echo done",
		"echo 'a && b' && git pull":                              "echo 'a && b'|git pull",
		"git clone u\ncd d\nls":                                  "git clone u|cd d|ls",
		"git checkout x && cat > f <<'EOF'\ngit clone evil\nEOF": "git checkout x|cat > f",
	}
	for in, want := range cases {
		if got := strings.Join(splitSegments(in), "|"); got != want {
			t.Errorf("%q → %q want %q", in, got, want)
		}
	}
}

func TestParseGitTwinActions(t *testing.T) {
	cases := []struct {
		cmd  string
		want string // kind:dir:url:branch joined by ;
	}{
		{"git clone https://github.com/openai/math.git /tmp/openai_math", "clone:/tmp/openai_math:https://github.com/openai/math.git:"},
		{"cd /tmp && git clone https://github.com/openai/math.git", "clone:/tmp/math:https://github.com/openai/math.git:"},
		{"git clone --depth 1 -b main https://x/y.git /tmp/y", "clone:/tmp/y:https://x/y.git:main"},
		{"git clone --branch=dev https://x/y.git /tmp/y", "clone:/tmp/y:https://x/y.git:dev"},
		{"git clone https://TOKEN@github.com/o/r.git /tmp/r && ls /tmp/r", "clone:/tmp/r:https://TOKEN@github.com/o/r.git:"},
		{"GIT_SSH_COMMAND='ssh -i k' git clone git@github.com:o/r.git /tmp/r", "clone:/tmp/r:git@github.com:o/r.git:"},
		{"cd /tmp/devrepo && git checkout theory", "read:/tmp/devrepo:git checkout theory"},
		{"git -C /tmp/devrepo pull --ff-only", "read:/tmp/devrepo:git pull --ff-only"},
		{"git -C /tmp/devrepo push origin x", ""},
		{"cd /tmp/d && git commit -m x && git push", ""},
		{"cd $WORK && git clone https://x/y.git", ""},
		{"git --git-dir=/x/.git log", ""},
		{"echo hi", ""},
	}
	for _, c := range cases {
		var parts []string
		for _, a := range parseGitTwinActions(c.cmd, "") {
			if a.Kind == "clone" {
				parts = append(parts, "clone:"+a.Dir+":"+a.URL+":"+a.Branch)
			} else {
				parts = append(parts, "read:"+a.Dir+":git "+strings.Join(a.Args, " "))
			}
		}
		if got := strings.Join(parts, ";"); got != c.want {
			t.Errorf("%q → %q want %q", c.cmd, got, c.want)
		}
	}
	// a relative clone target needs a known cwd
	if a := parseGitTwinActions("git clone https://x/y.git", ""); len(a) != 0 {
		t.Errorf("an unknown cwd must not be guessed: %v", a)
	}
	if a := parseGitTwinActions("git clone https://x/y.git", "/tmp/work"); len(a) != 1 || a[0].Dir != "/tmp/work/y" {
		t.Errorf("default dir comes from the URL under the shadow's work dir: %v", a)
	}
}

func TestNormTwinURLDropsTheChildsCredentials(t *testing.T) {
	cases := map[string]string{
		"https://ghp_SECRET@github.com/openai/math.git":   "https://github.com/openai/math.git",
		"https://x-access-token:ghp_S@github.com/o/r.git": "https://github.com/o/r.git",
		"git@github.com:openai/math.git":                  "https://github.com/openai/math.git",
		"ssh://git@github.com/o/r":                        "https://github.com/o/r.git",
		"https://github.com/o/r":                          "https://github.com/o/r.git",
	}
	for in, want := range cases {
		got, key, ok := normTwinURL(in)
		if !ok || got != want {
			t.Errorf("%q → %q (%v) want %q", in, got, ok, want)
		}
		if strings.Contains(got, "SECRET") || strings.Contains(got, "ghp_") {
			t.Errorf("a token survived in %q", got)
		}
		if key != originKey(want) {
			t.Errorf("key %q differs from the origin key of the clone URL %q", key, originKey(want))
		}
	}
	if _, _, ok := normTwinURL("not a url"); ok {
		t.Error("garbage accepted")
	}
}

func TestTwinDirAllowed(t *testing.T) {
	RegisterDevRepo("o", "twinmirror", "")
	t.Cleanup(func() {
		dev_repos_mu.Lock()
		delete(dev_repos, "o/twinmirror")
		dev_repos_mu.Unlock()
	})
	for dir, want := range map[string]bool{
		"/tmp/openai_math":              true,
		"/tmp/devrepo":                  true,
		"/home/claude/scratch":          true,
		"/tmp":                          false,
		"/etc/x":                        false,
		"relative/x":                    false,
		"/tmp/ref_store_baloon":         false, // the kernel's own clone
		"/tmp/chunk_1_files":            false,
		TheoryOutputsDir:                false,
		TheoryOutputsDir + "/sub":       false,
		"/home/claude/twinmirror":       false, // the dev mirror must never hold a .git
		"/home/claude/twinmirror/inner": false,
	} {
		if got := twinDirAllowed(dir); got != want {
			t.Errorf("%s → %v want %v", dir, got, want)
		}
	}
}

// ---- lifecycle against a local "GitHub" ------------------------------------------------------

func twinGitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type fakeUpstream struct {
	bare, work, url string
}

func newUpstream(t *testing.T) *fakeUpstream {
	root := t.TempDir()
	u := &fakeUpstream{bare: filepath.Join(root, "up.git"), work: filepath.Join(root, "work")}
	twinGitRun(t, root, "init", "-q", "--bare", "-b", "main", u.bare)
	twinGitRun(t, root, "init", "-q", "-b", "main", u.work)
	u.url = "file://" + u.bare
	twinGitRun(t, u.work, "remote", "add", "origin", u.bare)
	u.commit(t, "Sorting.lean", "theorem sort_ok : True := trivial")
	return u
}

func (u *fakeUpstream) commit(t *testing.T, file, content string) string {
	os.WriteFile(filepath.Join(u.work, file), []byte(content), 0o644)
	twinGitRun(t, u.work, "add", "-A")
	twinGitRun(t, u.work, "commit", "-q", "-m", "c "+file)
	twinGitRun(t, u.work, "push", "-q", "origin", "main")
	return twinGitRun(t, u.work, "rev-parse", "HEAD")
}

func TestEnsureTwinLifecycle(t *testing.T) {
	up := newUpstream(t)
	dir := filepath.Join(t.TempDir(), "openai_math")

	var clones, fetches, lsremotes int32
	oldGit := twinGit
	twinGit = func(d, cmdline string, to time.Duration) (int, string) {
		switch {
		case strings.HasPrefix(cmdline, "git clone"):
			atomic.AddInt32(&clones, 1)
		case strings.Contains(cmdline, "git fetch"):
			atomic.AddInt32(&fetches, 1)
		case strings.HasPrefix(cmdline, "git ls-remote"):
			atomic.AddInt32(&lsremotes, 1)
		}
		return oldGit(d, cmdline, to)
	}
	t.Cleanup(func() { twinGit = oldGit })
	t.Cleanup(func() { twinMu.Lock(); delete(twinDirs, dir); twinMu.Unlock() })

	act := twinAction{Kind: "clone", URL: up.url, Dir: dir}

	// 1. first clone chunk: the kernel clones, shallow, and marks it as its own
	if note := ensureTwin(act); !strings.Contains(note, "cloned") {
		t.Fatalf("first call: %s", note)
	}
	if clones != 1 || !isTwin(dir) {
		t.Fatalf("clones=%d twin=%v", clones, isTwin(dir))
	}
	if b, err := os.ReadFile(filepath.Join(dir, "Sorting.lean")); err != nil || !strings.Contains(string(b), "sort_ok") {
		t.Fatalf("clone content missing: %v", err)
	}
	if out := twinGitRun(t, dir, "rev-parse", "--is-shallow-repository"); out != "true" {
		t.Fatalf("clone must be shallow, got %q", out)
	}

	// 2. the next chunk 'clones' the same thing: HEAD matches ls-remote → NO new clone
	if note := ensureTwin(act); !strings.Contains(note, "skipped") {
		t.Fatalf("second call: %s", note)
	}
	if clones != 1 || fetches != 0 {
		t.Fatalf("re-clone per chunk is what this rule prevents: clones=%d fetches=%d", clones, fetches)
	}
	if lsremotes < 1 {
		t.Fatal("the comparison must ask the remote")
	}

	// 3. the upstream moves: the twin (ours) is brought up to date, not re-cloned
	sha := up.commit(t, "Sorting.lean", "theorem sort_ok2 : True := trivial")
	if note := ensureTwin(act); !strings.Contains(note, "updated") {
		t.Fatalf("after upstream moved: %s", note)
	}
	if clones != 1 || fetches != 1 {
		t.Fatalf("clones=%d fetches=%d", clones, fetches)
	}
	if got := twinGitRun(t, dir, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("twin at %s, upstream at %s", got, sha)
	}

	// 4. a different origin in the same directory is never touched
	other := newUpstream(t)
	if note := ensureTwin(twinAction{Kind: "clone", URL: other.url, Dir: dir}); !strings.Contains(note, "different origin") {
		t.Fatalf("%s", note)
	}
}

func TestEnsureTwinLeavesForeignAndForbiddenDirectoriesAlone(t *testing.T) {
	up := newUpstream(t)
	// a directory with files that is not a git repository
	plain := filepath.Join(t.TempDir(), "plain")
	os.MkdirAll(plain, 0o755)
	os.WriteFile(filepath.Join(plain, "keep.txt"), []byte("mine"), 0o644)
	if note := ensureTwin(twinAction{Kind: "clone", URL: up.url, Dir: plain}); !strings.Contains(note, "not a git repository") {
		t.Fatalf("%s", note)
	}
	if _, err := os.Stat(filepath.Join(plain, "keep.txt")); err != nil {
		t.Fatal("a foreign directory was modified")
	}
	// a clone that is the kernel's... not a twin: same origin, behind → left alone
	foreign := filepath.Join(t.TempDir(), "foreign")
	twinGitRun(t, filepath.Dir(foreign), "clone", "-q", up.url, foreign)
	up.commit(t, "New.lean", "x")
	if note := ensureTwin(twinAction{Kind: "clone", URL: up.url, Dir: foreign}); !strings.Contains(note, "not a twin of ours") {
		t.Fatalf("%s", note)
	}
	// forbidden places
	for _, d := range []string{"/etc/evil", TheoryOutputsDir + "/x", "/tmp/ref_store_x"} {
		if note := ensureTwin(twinAction{Kind: "clone", URL: up.url, Dir: d}); !strings.Contains(note, "refused") {
			t.Errorf("%s: %s", d, note)
		}
	}
}

func TestReplayReadOnlyInsideTwins(t *testing.T) {
	up := newUpstream(t)
	dir := filepath.Join(t.TempDir(), "r")
	t.Cleanup(func() { twinMu.Lock(); delete(twinDirs, dir); twinMu.Unlock() })
	ensureTwin(twinAction{Kind: "clone", URL: up.url, Dir: dir})
	twinGitRun(t, up.work, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(up.work, "F.lean"), []byte("feature"), 0o644)
	twinGitRun(t, up.work, "add", "-A")
	twinGitRun(t, up.work, "commit", "-q", "-m", "feature work")
	twinGitRun(t, up.work, "push", "-q", "origin", "feature")

	if note := replayRead(twinAction{Kind: "read", Dir: dir, Args: []string{"fetch", "--depth", "1", "origin", "feature"}}); !strings.Contains(note, "exit 0") {
		t.Fatalf("%s", note)
	}
	// a directory that is not a twin: nothing runs
	if note := replayRead(twinAction{Kind: "read", Dir: t.TempDir(), Args: []string{"pull"}}); note != "" {
		t.Fatalf("replay outside a twin: %q", note)
	}
}

func TestGitTwinSyncAndTouches(t *testing.T) {
	up := newUpstream(t)
	dir := filepath.Join(t.TempDir(), "openai_math")
	t.Cleanup(func() { twinMu.Lock(); delete(twinDirs, dir); twinMu.Unlock() })

	cmd := "cd " + filepath.Dir(dir) + " && git clone " + up.url + " " + filepath.Base(dir) + " && ls"
	GitTwinSync(cmd, "")
	gitTwinWG.Wait() // the work is in the background
	if !isTwin(dir) {
		t.Fatal("the clone did not happen")
	}
	// a read inside the twin is answered by the sandbox; an unrelated command still runs locally
	if !GitTwinTouches("cd " + dir + "/lean && find . -name '*.lean' | head -5") {
		t.Fatal("a read inside the twin should be answered by the sandbox")
	}
	if GitTwinTouches("ls /mnt/user-data/outputs") {
		t.Fatal("an unrelated command must still run locally")
	}
}

// the 2026/11/02 12:26 and 13:10 log lines: "Cloning into '/home/claude/2>&1'" and
// "cloned … into /home/claude/baloon)"
func TestTwinParsingIgnoresRedirectionsAndBrackets(t *testing.T) {
	cases := []struct{ cmd, want string }{
		{"git clone https://x/y.git 2>&1", "/tmp/w/y"},
		{"git clone https://x/y.git /tmp/y 2>&1 | tail -5", "/tmp/y"},
		{"(git clone https://x/y.git /tmp/y)", "/tmp/y"},
		{"(cd /tmp && git clone https://x/y.git y2)", "/tmp/y2"},
		{"git clone https://x/y.git /tmp/y > /dev/null 2>&1", "/tmp/y"},
		{"git clone https://x/y.git >/tmp/log", "/tmp/w/y"},
		{"git clone -q https://x/y.git /tmp/y &>/dev/null", "/tmp/y"},
		{"git clone https://x/y.git /tmp/y 2> err.txt", "/tmp/y"},
		{"git clone https://x/y.git '/tmp/a)b'", ""}, // a bracket is not part of a directory name
	}
	for _, c := range cases {
		acts := parseGitTwinActions(c.cmd, "/tmp/w")
		got := ""
		if len(acts) > 0 {
			got = acts[0].Dir
		}
		if got != c.want {
			t.Errorf("%q → %q want %q", c.cmd, got, c.want)
		}
	}
	for _, d := range []string{"2>&1", "baloon)", "a;b", "x|y", "<in"} {
		if resolveDir("/tmp", d) != "" {
			t.Errorf("%q must not resolve to a directory", d)
		}
	}
	if resolveDir("/tmp", "a b") != "/tmp/a b" {
		t.Error("a quoted name with a space is a legitimate directory")
	}
}

func TestACloneRunsInTheBackgroundAndBlocksNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "slow")
	t.Cleanup(func() { twinMu.Lock(); delete(twinDirs, dir); twinMu.Unlock() })
	oldGit := twinGit
	started := make(chan struct{})
	twinGit = func(d, cmdline string, to time.Duration) (int, string) {
		if strings.HasPrefix(cmdline, "git clone") {
			close(started)
			time.Sleep(600 * time.Millisecond) // a TLS handshake that takes its time
			os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
			return 0, ""
		}
		return oldGit(d, cmdline, to)
	}
	t.Cleanup(func() { twinGit = oldGit })

	t0 := time.Now()
	GitTwinSync("git clone file:///nonexistent/up.git "+dir, "")
	if time.Since(t0) > 200*time.Millisecond {
		t.Fatalf("GitTwinSync took %s: it must return at once", time.Since(t0))
	}
	<-started
	// while the clone runs: the directory already counts as a twin, and asking is instant
	t1 := time.Now()
	if !GitTwinTouches("cat " + dir + "/x") {
		t.Fatal("a directory whose clone has started must already be answered by the sandbox")
	}
	if time.Since(t1) > 100*time.Millisecond {
		t.Fatalf("GitTwinTouches waited %s for the clone: the shadow asks it for every command", time.Since(t1))
	}
	gitTwinWG.Wait()
	if !isTwinMarked(dir) {
		t.Fatal("the clone finished but the twin was not marked")
	}
}

func isTwinMarked(dir string) bool { return isTwin(dir) }

func TestAFailedCloneIsNotATwinAndLocalCommandsRunAgain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "unreachable")
	oldGit := twinGit
	twinGit = func(d, cmdline string, to time.Duration) (int, string) {
		if strings.HasPrefix(cmdline, "git clone") {
			return 128, "fatal: unable to access: gnutls_handshake() failed"
		}
		return oldGit(d, cmdline, to)
	}
	t.Cleanup(func() { twinGit = oldGit })
	GitTwinSync("git clone file:///nonexistent/up.git "+dir, "")
	gitTwinWG.Wait()
	if GitTwinTouches("cat " + dir + "/x") {
		t.Fatal("after a failed clone the directory is no twin: a command reading it must run locally and say so")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a failed clone must not leave a directory behind")
	}
}
