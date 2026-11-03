package kernel

// git_fetch.go — bringing named branches of a clone up to date, judged by progress.
//
// gitUseBranchFromRemote fetched with `git fetch origin <branch> <base> 2>/dev/null`
// through runLocalGit: HTTPS, no sign-of-life check, no progress check, nothing but
// whatever the network stack eventually gave up with. On 2026/11/03 the theory
// branch's fetch before a merge took 2:00, 2:01 and 2:00 for chunks 1, 3 and 4 (2 s
// for chunk 5) — waiting, not transferring. A longer or shorter timeout does not fix
// that: a fetch that is not moving will not move by waiting. What decides is progress:
//
//	no sign of life within gitStartWindow       → that connection never came up: new process
//	connected, no new --progress line for gitPushStallTimeout → it stopped moving: new process
//	exit                                        → done (an up-to-date fetch prints nothing)
//
// the same rule the pushes already use, and over the deploy key first, like
// GitCloneOrReuse's fetch: every SSH push and fetch on that machine went through on the
// first attempt while HTTPS hung.

import (
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// gitRemoteFn is the URL and environment for a fetch of ownerRepo (a variable for tests).
var gitRemoteFn = func(ownerRepo string, ssh bool) (url, env string) {
	if ssh {
		return sshRemoteURL(ownerRepo), fmt.Sprintf("GIT_SSH_COMMAND=%q ", gitSSHCommand())
	}
	return CloneURL(ownerRepo), ""
}

// gitSSHFn: whether the deploy key is there (a variable for tests).
var gitSSHFn = gitSSHAvailable

const gitFetchRefsFmt = "%[1]sgit -C %[2]q fetch --progress %[3]q %[4]s"

var missingRefRe = regexp.MustCompile(`couldn't find remote ref (?:refs/heads/)?(\S+)`)

// gitFetchRefs fetches each named branch into refs/remotes/origin/<branch>. A branch the
// remote does not have is reported in missing — an answer, not a failure to retry.
func gitFetchRefs(repoDir, ownerRepo string, branches ...string) (missing []string, err error) {
	var want []string
	seen := map[string]bool{}
	for _, b := range branches {
		if b != "" && !seen[b] {
			seen[b] = true
			want = append(want, b)
		}
	}
	useSSH := gitSSHFn()
	sshFails := 0
	attempts := gitNetAttempts()
	for attempt := 1; attempt <= attempts && len(want) > 0; attempt++ {
		var specs []string
		for _, b := range want {
			specs = append(specs, shq(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", b, b)))
		}
		url, env := gitRemoteFn(ownerRepo, useSSH)
		transport := map[bool]string{true: "SSH", false: "HTTPS"}[useSSH]
		cmd := fmt.Sprintf(gitFetchRefsFmt, env, repoDir, url, strings.Join(specs, " "))
		started := time.Now()
		r := gitGuardFn(cmd, repoDir, gitStartWindow, gitPushMaxRun, gitPushStallTimeout, func(el time.Duration, line string) {
			log.Printf("git_fetch: %s attempt %d/%d (%s) in progress (%s): %s", ownerRepo, attempt, attempts, transport, el.Round(time.Second), line)
		})
		switch {
		case r.Code == 0:
			log.Printf("git_fetch: %s %v (attempt %d, %s, %s)", ownerRepo, want, attempt, transport, time.Since(started).Round(time.Millisecond))
			return missing, nil
		case !r.Started:
			log.Printf("git_fetch: %s attempt %d/%d (%s): no sign of life in %s, new process", ownerRepo, attempt, attempts, transport, gitStartWindow)
		case r.TimedOut:
			log.Printf("git_fetch: %s attempt %d/%d (%s): no new progress for %s, new process", ownerRepo, attempt, attempts, transport, gitPushStallTimeout)
		default:
			// the remote answered: some branches it does not have
			if ms := missingRefRe.FindAllStringSubmatch(r.Out, -1); len(ms) > 0 {
				gone := map[string]bool{}
				for _, m := range ms {
					gone[strings.TrimSpace(m[1])] = true
				}
				var keep []string
				for _, b := range want {
					if gone[b] {
						missing = append(missing, b)
					} else {
						keep = append(keep, b)
					}
				}
				want = keep
				attempt-- // not a network failure
				continue
			}
			log.Printf("git_fetch: %s attempt %d/%d (%s) failed (exit %d): %s", ownerRepo, attempt, attempts, transport, r.Code, truncStr(strings.TrimSpace(r.Out), 160))
		}
		if useSSH {
			if sshFails++; sshFails >= gitSSHFetchFailuresBeforeHTTPS {
				useSSH = false
			}
		}
	}
	if len(want) == 0 {
		return missing, nil
	}
	return missing, fmt.Errorf("git_fetch %s %v: no attempt finished", ownerRepo, want)
}

var githubRepoRe = regexp.MustCompile(`github\.com[:/]([^/\s]+/[^/\s]+?)(?:\.git)?/?$`)

// originOwnerRepo reads owner/repo from the clone's origin URL ("" if not GitHub).
func originOwnerRepo(repoDir string) string {
	out, err := exec.Command("git", "-C", repoDir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	if m := githubRepoRe.FindStringSubmatch(strings.TrimSpace(string(out))); m != nil {
		return m[1]
	}
	return ""
}
