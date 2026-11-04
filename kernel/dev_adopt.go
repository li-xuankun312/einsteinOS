package kernel

// dev_adopt.go — a restart must not lose the units that already passed.
//
// The dev branch used to be einsteinOS/dev-<pid>; it is now named by what it develops
// (devBranchFor). A run that was started before that change has its plan and its
// passed units on a pid-named branch the new name does not point at, so a restart with
// the new code would begin again from unit 1. When the stable branch does not exist yet,
// look for such a legacy branch FOR THE SAME REFERENCE COMMIT, take the one with the
// most units passed, and publish it under the stable name. Nothing is copied or
// rewritten: the stable branch simply starts at the legacy branch's tip.

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var legacyDevBranchRe = regexp.MustCompile(`^einsteinOS/dev-(\d+)$`)

// gitOut runs git in dir and returns its output.
var gitOut = func(dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	out, err := c.Output()
	return string(out), err
}

// adoptLegacyDevBranch publishes the best legacy branch as stable. It returns the legacy
// branch it adopted ("" when there was nothing to adopt or nothing went wrong enough to say).
func adoptLegacyDevBranch(devRepo, repo, refSHA, stable string) string {
	if refSHA == "" {
		return ""
	}
	dir, err := devRepoLocalDir(devRepo)
	if err != nil {
		return ""
	}
	heads, err := gitLsRemoteHeads(dir, devRepo, "refs/heads/einsteinOS/dev-*", "refs/heads/"+stable)
	if err != nil {
		log.Printf("dev_adopt: %s could not list the remote branches: %v", repo, err)
		return ""
	}
	var legacy []string
	for _, line := range strings.Split(heads, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "refs/heads/")
		if name == stable {
			return "" // the stable branch exists: nothing to adopt
		}
		if legacyDevBranchRe.MatchString(name) {
			legacy = append(legacy, name)
		}
	}
	best, bestPassed, bestPid := "", 0, -1
	for _, b := range legacy {
		if _, ferr := gitOut(dir, 120*time.Second, "fetch", "-q", "origin", b); ferr != nil {
			continue
		}
		raw, serr := gitOut(dir, 30*time.Second, "show", "FETCH_HEAD:"+devPlanFile)
		if serr != nil {
			continue
		}
		var p DevPlan
		if json.Unmarshal([]byte(raw), &p) != nil || p.Version != devPlanVersion || p.Ref != repo || p.RefSHA != refSHA {
			continue // another repository, another commit, or not a plan
		}
		passed := 0
		for _, l := range p.Leaves() {
			if p.UnitPass(l) {
				passed++
			}
		}
		pid, _ := strconv.Atoi(legacyDevBranchRe.FindStringSubmatch(b)[1])
		if passed > bestPassed || (passed == bestPassed && passed > 0 && pid > bestPid) {
			best, bestPassed, bestPid = b, passed, pid
		}
	}
	if best == "" || bestPassed == 0 {
		return ""
	}
	if _, ferr := gitOut(dir, 120*time.Second, "fetch", "-q", "origin", best); ferr != nil {
		return ""
	}
	if _, cerr := gitOut(dir, 60*time.Second, "checkout", "-q", "-B", stable, "FETCH_HEAD"); cerr != nil {
		log.Printf("dev_adopt: %s checkout of %s failed: %v", repo, best, cerr)
		return ""
	}
	if perr := devGitPush(dir, devRepo, stable, 20); perr != nil {
		log.Printf("dev_adopt: %s could not publish %s as %s: %v", repo, best, stable, perr)
		return ""
	}
	log.Printf("dev_adopt: %s adopted %s (%d unit(s) already passed) as %s — a restart continues where it stopped",
		repo, best, bestPassed, stable)
	return best
}
