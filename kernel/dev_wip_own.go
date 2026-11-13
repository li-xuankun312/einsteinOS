package kernel

import (
	"fmt"
	"log"
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
	log.Printf("dev_wip: %s %s is at %.12s, not at %.12s the kernel pushed (%s commit(s) not the kernel's) — set back",
		devRepo, wip, tip, sha, n)
	if err := devGitPush(cloneDir, devRepo, fmt.Sprintf("+%s:refs/heads/%s", sha, wip), 20); err != nil {
		log.Printf("dev_wip: %s setting %s back: %v", devRepo, wip, err)
		return false
	}
	return true
}
