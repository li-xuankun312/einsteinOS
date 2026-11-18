package kernel

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
)

// A dev conversation is told where its repo is (/home/claude/<repo>) and how to get it in
// its own sandbox: "git clone --branch <dev branch> <url> /home/claude/<repo>". On this
// machine that path is the mirror the kernel primed from its own clone, and git's network
// commands are no-ops here (shadow's gitShim). So "rm -rf /home/claude/baloon && git clone
// … /home/claude/baloon" made the sandbox's tree the branch tip again and left this machine
// with no mirror at all: 11/18 01:47:03 dev_fix_1 of uf7cdc0dcca (7ad91890), after which
// every verification failed on "delta: lstat /home/claude/baloon: no such file or directory"
// until the run re-entered the step at 03:50 — 2h10m of two conversations.
//
// The clone's effect is known: the tree of the commit the mirror was primed from. The
// kernel makes that effect here itself, from its own clone.

type mirrorTip struct{ cloneDir, sha string }

var (
	mirrorTipsMu sync.Mutex
	mirrorTips   = map[string]mirrorTip{}
)

// RegisterMirrorTip records that mirror was primed from commit sha of the kernel's clone
// cloneDir, for as long as a conversation works in it.
func RegisterMirrorTip(mirror, cloneDir, sha string) {
	mirrorTipsMu.Lock()
	defer mirrorTipsMu.Unlock()
	mirrorTips[path.Clean(mirror)] = mirrorTip{cloneDir: cloneDir, sha: sha}
}

// UnregisterMirrorTip forgets mirror.
func UnregisterMirrorTip(mirror string) {
	mirrorTipsMu.Lock()
	defer mirrorTipsMu.Unlock()
	delete(mirrorTips, path.Clean(mirror))
}

func mirrorTipOf(dir string) (mirrorTip, bool) {
	mirrorTipsMu.Lock()
	defer mirrorTipsMu.Unlock()
	t, ok := mirrorTips[path.Clean(dir)]
	return t, ok
}

// isRegisteredMirror: dir is a mirror a conversation works in now (not a place for twins).
func isRegisteredMirror(dir string) bool {
	mirrorTipsMu.Lock()
	defer mirrorTipsMu.Unlock()
	dir = path.Clean(dir)
	for m := range mirrorTips {
		if dir == m || strings.HasPrefix(dir, m+"/") {
			return true
		}
	}
	return false
}

// ReprimeClonedMirror is called after a git command of a conversation has run here. When
// the command clones into a registered mirror and the mirror is now missing or empty, the
// mirror is primed again from the commit it was primed from — what the clone made in the
// sandbox. A clone into a mirror that still holds files fails in the sandbox too ("already
// exists and is not an empty directory"), so then nothing is done. It returns the line to
// log, or "".
func ReprimeClonedMirror(command, workDir string) string {
	for _, a := range parseGitTwinActions(command, workDir) {
		if a.Kind != "clone" {
			continue
		}
		dir := path.Clean(a.Dir)
		tip, ok := mirrorTipOf(dir)
		if !ok {
			continue
		}
		if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
			return ""
		}
		if err := primeMirrorFromCommit(tip.cloneDir, tip.sha, dir); err != nil {
			return fmt.Sprintf("mirror_clone: the conversation cloned into %s and the mirror is gone here; priming it again from %s failed: %v", dir, shortSHA(tip.sha), err)
		}
		return fmt.Sprintf("mirror_clone: the conversation cloned into %s, which held nothing here (git's network commands are no-ops on this machine) — primed again from %s, the commit the sandbox's clone gave it", dir, shortSHA(tip.sha))
	}
	return ""
}

// primeMirrorFromCommit makes mirror exactly the tree of sha in cloneDir (no .git), whatever
// cloneDir has checked out now.
func primeMirrorFromCommit(cloneDir, sha, mirror string) error {
	if err := wipeDirContents(mirror); err != nil {
		return err
	}
	c := exec.Command("bash", "-c", fmt.Sprintf("set -o pipefail; git -C %q archive %q | tar -x -C %q", cloneDir, sha, mirror))
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, truncStr(strings.TrimSpace(string(out)), 200))
	}
	return nil
}

// gitHeadOf is the commit cloneDir has checked out.
func gitHeadOf(cloneDir string) string {
	out, err := exec.Command("git", "-C", cloneDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// MirrorCloneShellFunc is a bash function, __eos_mirror_clone <dir>, that a command run here
// calls where it clones (shadow's gitShim): when dir is a registered mirror that is missing or
// empty, it is made the tree of the commit it was primed from, at that point of the command —
// so what the command does next in it ("&& cd /home/claude/baloon && printf … >> …") happens
// here as in the sandbox. Any other dir: nothing.
func MirrorCloneShellFunc() string {
	mirrorTipsMu.Lock()
	defer mirrorTipsMu.Unlock()
	var arms strings.Builder
	for m, t := range mirrorTips {
		fmt.Fprintf(&arms, "%s) if [ -z \"$(ls -A %s 2>/dev/null)\" ]; then mkdir -p %s && command git -C %s archive %s | tar -x -C %s; fi;; ",
			shQuote(m), shQuote(m), shQuote(m), shQuote(t.cloneDir), shQuote(t.sha), shQuote(m))
	}
	return "__eos_mirror_clone() { local d; d=\"$(realpath -m -- \"$1\" 2>/dev/null)\"; case \"$d\" in " + arms.String() + "*) ;; esac; }\n"
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
