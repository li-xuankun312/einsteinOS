package kernel

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// dev_wip_own.go — a unit's wip branch holds what the kernel pushed there, nothing else.
//
// 11/13 04:26:55 a dev conversation pushed a new root commit to
// einsteinOS/dev-af57b5e66f-wip-u7f1ccea730 and, until 05:57, 105 commits on top of it. The
// kernel committed its verified tree onto that history ("refusing to merge unrelated
// histories", three times) and the next attempt started from it.

var kernelPushedMu sync.Mutex
var kernelPushed = map[string]string{} // devRepo + " " + branch -> the commit the kernel pushed

func noteKernelPush(devRepo, branch, sha string) {
	if sha == "" {
		return
	}
	kernelPushedMu.Lock()
	kernelPushed[devRepo+" "+branch] = sha
	kernelPushedMu.Unlock()
}

// KernelPushed: the commit the kernel last pushed to devRepo's branch in this run, or "".
func KernelPushed(devRepo, branch string) string {
	kernelPushedMu.Lock()
	defer kernelPushedMu.Unlock()
	return kernelPushed[devRepo+" "+branch]
}

func gitLine(dir string, args ...string) string {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// keepKernelWip sets wip back to sha, the commit the kernel pushed there, when its tip is
// anything else. Returns whether it did.
func keepKernelWip(cloneDir, devRepo, wip, sha string) bool {
	if or := originOwnerRepo(cloneDir); or != "" {
		if _, err := gitFetchRefs(cloneDir, or, wip); err != nil {
			log.Printf("dev_wip: %s fetch of %s: %v", devRepo, wip, err)
			return false
		}
	} else {
		gitLine(cloneDir, "fetch", "-q", "origin", "+refs/heads/"+wip+":refs/remotes/origin/"+wip)
	}
	tip := gitLine(cloneDir, "rev-parse", "-q", "--verify", "refs/remotes/origin/"+wip)
	if tip == "" || tip == sha {
		return false
	}
	n := gitLine(cloneDir, "rev-list", "--count", sha+".."+tip)
	if madeHere(cloneDir, sha, tip) {
		// commits on top of the kernel's, made on this machine: its operator's, not a
		// conversation's (a conversation's git runs in its sandbox; here its pushes are no-ops)
		log.Printf(wipAdoptedFmt, devRepo, wip, tip, sha, n, machineHost())
		noteKernelPush(devRepo, wip, tip)
		return false
	}
	log.Printf("dev_wip: %s %s is at %.12s, not at %.12s the kernel pushed (%s commit(s) not the kernel's) — set back",
		devRepo, wip, tip, sha, n)
	if err := devGitPush(cloneDir, devRepo, fmt.Sprintf("+%s:refs/heads/%s", sha, wip), 20); err != nil {
		log.Printf("dev_wip: %s setting %s back: %v", devRepo, wip, err)
		return false
	}
	return true
}

// %[1]s repo, %[2]s branch, %[3]s tip, %[4]s the kernel's, %[5]s count, %[6]s host
const wipAdoptedFmt = "dev_wip: %[1]s %[2]s is at %.12[3]s, %[5]s commit(s) on top of %.12[4]s the kernel pushed, each committed on this machine (%[6]s) — kept, the attempt starts from it"

// machineHost: this machine's host name, as git writes it into a committer address here.
var machineHost = func() string {
	h, _ := os.Hostname()
	return h
}

// madeHere: tip descends from sha and every commit between was committed on this machine (its
// host name in the committer's address) — the machine's operator's commits, not a conversation's,
// whose git runs in its own sandbox. 11/24 02:41:15 the operator's bb0fd0a8, on top of the
// kernel's e9679789, restored the 13 files a command had left empty and the port map's 168
// rows, and was set back to e9679789: the attempt started again from the empty files.
func madeHere(cloneDir, sha, tip string) bool {
	host := machineHost()
	if host == "" || exec.Command("git", "-C", cloneDir, "merge-base", "--is-ancestor", sha, tip).Run() != nil {
		return false
	}
	out := gitLine(cloneDir, "log", "--format=%ce", sha+".."+tip)
	if out == "" {
		return false
	}
	for _, e := range strings.Split(out, "\n") {
		_, domain, ok := strings.Cut(strings.TrimSpace(e), "@")
		if !ok || (domain != host && !strings.HasPrefix(domain, host+".")) {
			return false
		}
	}
	return true
}
