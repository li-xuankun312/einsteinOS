package kernel

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// git_ops.go — Go-native git operations for einsteinOS kernel.
//
// replaces bash heredoc string manipulation in ref_store_chunk and pr_exec
// with typed Go functions. retry logic lives in Go, not in bash scripts.
// ---------------------------------------------------------------------------

// GetGHToken reads the GitHub token from /tmp/.gh_token (written at startup).
func GetGHToken() string {
	data, err := os.ReadFile("/tmp/.gh_token")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// CloneURL builds https://TOKEN@github.com/owner/repo.git
func CloneURL(ownerRepo string) string {
	token := GetGHToken()
	if token != "" {
		return fmt.Sprintf("https://%s@github.com/%s.git", token, ownerRepo)
	}
	return fmt.Sprintf("https://github.com/%s.git", ownerRepo)
}

// GitCloneOrReuse ensures dir is a usable clone of ownerRepo, synced to remote HEAD.
// If .git exists → fetch + reset. If .git is missing or corrupt → remove residue,
// clone fresh. Every network step runs under gitGuard: a process with no sign of life
// after 10 s is killed and a new one started (see git_guard.go), up to
// gitNetAttempts() times. Success is the process's exit code, not whether it printed
// anything: an up-to-date `git fetch` prints nothing.
func GitCloneOrReuse(ownerRepo, dir string) error {
	attempts := gitNetAttempts()
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if _, err := os.Stat(dir + "/.git"); err == nil {
			// .git exists — fetch, then reset locally.
			// do NOT rm -rf: the local repo is valid, only the network failed.
			r := gitGuard(fmt.Sprintf("git -C %q fetch origin", dir), dir, gitStartWindow, gitMaxRun)
			switch {
			case !r.Started:
				log.Printf("git_clone_or_reuse: %s attempt %d/%d fetch hung (no output for %s), killed", ownerRepo, attempt, attempts, gitStartWindow)
				lastErr = fmt.Errorf("fetch hung")
				continue
			case r.TimedOut:
				log.Printf("git_clone_or_reuse: %s attempt %d/%d fetch still running after %s, killed", ownerRepo, attempt, attempts, gitMaxRun)
				lastErr = fmt.Errorf("fetch timed out")
				continue
			case r.Code != 0:
				lastErr = fmt.Errorf("fetch exit %d: %s", r.Code, truncStr(r.Out, 200))
				log.Printf("git_clone_or_reuse: %s attempt %d/%d fetch failed (exit %d): %s", ownerRepo, attempt, attempts, r.Code, truncStr(strings.TrimSpace(r.Out), 120))
				continue
			}
			// fetch ok (exit 0, possibly silent) — reset to remote HEAD (local, no guard needed)
			if out, rerr := exec.Command("bash", "-c", fmt.Sprintf("cd %q && git reset --hard origin/HEAD 2>&1", dir)).CombinedOutput(); rerr != nil {
				log.Printf("git_clone_or_reuse: %s reset after fetch: %v: %s", ownerRepo, rerr, truncStr(strings.TrimSpace(string(out)), 120))
			}
			log.Printf("git_clone_or_reuse: %s synced (attempt %d)", ownerRepo, attempt)
			return nil
		}

		// .git missing or corrupt — clean residue, clone fresh
		os.RemoveAll(dir)
		r := gitGuard(fmt.Sprintf("git clone %q %q", CloneURL(ownerRepo), dir), "/tmp", gitStartWindow, gitCloneMaxRun)
		if !r.Started || r.TimedOut || r.Code != 0 {
			os.RemoveAll(dir)
			lastErr = fmt.Errorf("clone failed (started=%v timed_out=%v exit=%d)", r.Started, r.TimedOut, r.Code)
			log.Printf("git_clone_or_reuse: %s attempt %d/%d clone failed (started=%v timed_out=%v exit=%d)", ownerRepo, attempt, attempts, r.Started, r.TimedOut, r.Code)
			continue
		}
		if _, err := os.Stat(dir + "/.git"); err != nil {
			os.RemoveAll(dir)
			lastErr = fmt.Errorf("clone exited 0 but left no .git")
			log.Printf("git_clone_or_reuse: %s attempt %d/%d clone left no .git", ownerRepo, attempt, attempts)
			continue
		}
		log.Printf("git_clone_or_reuse: %s cloned (attempt %d)", ownerRepo, attempt)
		return nil
	}
	return fmt.Errorf("GitCloneOrReuse %s failed after %d attempts: %v", ownerRepo, attempts, lastErr)
}

// devRepoLocalDir returns a local clone of the dev repo that the kernel can run
// git in.
//
// dev_store_theory, dev_store_conv and the merge step assumed
// /home/claude/<repo> — the path of the clone inside Claude's REMOTE sandbox. On
// the machine that runs einsteinOS it does not exist, so ForkExec failed with
// "chdir /home/claude/baloon: no such file or directory": the theory was never
// stored on the branch, the dev child found no .einsteinOS/theory.md and built
// nothing (Strata run, 01:23:41). Use that path only if a real clone is there;
// otherwise use the kernel's own clone (the one ref_store_chunk maintains).
func devRepoLocalDir(ownerRepo string) (string, error) {
	name := ownerRepo
	if i := strings.LastIndex(ownerRepo, "/"); i >= 0 {
		name = ownerRepo[i+1:]
	}
	legacy := "/home/claude/" + name
	if _, err := os.Stat(legacy + "/.git"); err == nil {
		return legacy, nil
	}
	dir := "/tmp/ref_store_" + name
	if err := GitCloneOrReuse(ownerRepo, dir); err != nil {
		return "", fmt.Errorf("no local clone of %s: %w", ownerRepo, err)
	}
	return dir, nil
}

// gitAuthFailure reports output that means GitHub rejected the credentials.
// Retrying cannot change that, so callers stop at once instead of repeating a
// failure that is deterministic (the Strata run retried 20 pushes).
func gitAuthFailure(out string) bool {
	o := strings.ToLower(out)
	for _, m := range []string{
		"could not read password", "authentication failed", "invalid username or password",
		"bad credentials", "http 401", "error: 401", "returned error: 401",
	} {
		if strings.Contains(o, m) {
			return true
		}
	}
	return false
}

// gitUseBranchFromRemote puts repoDir on `branch` at the REMOTE's tip when the
// remote has it, otherwise starts it from the remote's `base`. GitCloneOrReuse has
// just fetched, so origin/* is current. Without this, a second store to the same
// branch (dev_from_theorems re-runs after a dev_fix) was based on the local reset
// main, diverged from the remote branch and was rejected.
func gitUseBranchFromRemote(repoDir, branch, base string) error {
	// fetch first: without this, "origin/<branch>" is whatever this clone last
	// saw — possibly stale if another process pushed to the shared branch since
	// (exactly the case GitMerge's caller in ref_store_chunk needs to handle: one
	// chunk's clone catching up on another chunk's merge). Fetch both refs
	// explicitly rather than `git fetch origin` (no refspec) so a repo with many
	// unrelated branches (conv/<id> per chunk, per run) doesn't pay for all of
	// them on every call.
	cmd := fmt.Sprintf("cd %q && "+
		"git fetch origin %q %q 2>/dev/null; ("+
		"if git rev-parse --verify -q origin/%s >/dev/null; then git checkout -q -B %s origin/%s; "+
		"elif git rev-parse --verify -q origin/%s >/dev/null; then git checkout -q -B %s origin/%s; "+
		"else git checkout -q -B %s; fi)",
		repoDir, branch, base, branch, branch, branch, base, branch, base, branch)
	return runLocalGit(cmd, repoDir)
}

// GitCheckoutOrCreate checks out a branch, creating it if needed.
// No fetch — caller (GitCloneOrReuse) already synced to remote HEAD.
// An extra fetch here hangs on TLS-flaky networks with no timeout.
func GitCheckoutOrCreate(repoDir, branch, baseBranch string) error {
	cmd := fmt.Sprintf("cd %q && "+
		"git checkout %s 2>/dev/null || (git checkout %s 2>/dev/null || git checkout -b %s 2>/dev/null; git checkout -b %s 2>/dev/null)",
		repoDir, branch, baseBranch, baseBranch, branch)
	return runLocalGit(cmd, repoDir)
}

// runLocalGit runs a local (no network) git command. Like the ForkExec these
// helpers used, a non-zero exit is not an error: the commands are written as
// "try this, else that" chains. Only failing to start is. It needs no booted
// kernel, so it can run anywhere (and be tested).
func runLocalGit(cmd, dir string) error {
	c := exec.Command("bash", "-c", cmd)
	c.Dir = dir
	if _, err := c.CombinedOutput(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil
		}
		return err
	}
	return nil
}

// GitMerge merges branchToMerge into repoDir's CURRENT branch with --no-ff (so
// the merge always leaves a real commit, never a silent fast-forward that could
// be mistaken for "nothing to merge"), and commits with message.
//
// Unlike runLocalGit (used for the idempotent "try this else that" checkout
// chains, where a non-zero exit is expected and not an error), a merge that
// fails is a REAL failure — most likely a conflict — and must not be silently
// treated as success: the caller is about to push whatever state this leaves,
// and a half-merged working tree pushed as-is would corrupt the target branch.
// On failure this runs `git merge --abort` so the clone is left clean for the
// next chunk/run that reuses it, and returns the conflict output.
func GitMerge(repoDir, branchToMerge, message string) error {
	cmd := exec.Command("bash", "-c", fmt.Sprintf(
		"git config user.email 'einsteinOS@kernel' 2>/dev/null; "+
			"git config user.name 'einsteinOS' 2>/dev/null; "+
			"git merge --no-ff %q -m %q 2>&1", branchToMerge, message))
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	// leave the clone usable for whoever reuses it next
	abort := exec.Command("bash", "-c", "git merge --abort 2>/dev/null")
	abort.Dir = repoDir
	abort.Run()
	return fmt.Errorf("merge %s: %w: %s", branchToMerge, err, truncStr(strings.TrimSpace(string(out)), 300))
}

// GitCommit stages all and commits with message. Returns true if commit created.
func GitCommit(repoDir, message string) bool {
	cmd := fmt.Sprintf("cd %q && "+
		"git config user.email 'einsteinOS@kernel' 2>/dev/null; "+
		"git config user.name 'einsteinOS' 2>/dev/null; "+
		"git add -A && git commit -m %q --allow-empty 2>&1",
		repoDir, message)
	return runLocalGit(cmd, repoDir) == nil
}

// GitPushRetry pushes a branch with retry on TLS/network failures.
// Uses the 10 s start guard — if TLS hangs, kill and retry immediately (no 10s sleep).
// each hung attempt costs 10 s, each real failure gets a 10 s backoff.
func GitPushRetry(repoDir, ownerRepo, branch string, maxRetry int) error {
	pushURL := CloneURL(ownerRepo)
	if maxRetry < 20 {
		maxRetry = 20 // with 10s start guard, attempts are cheap; network needs 200s window
	}
	for attempt := 1; attempt <= maxRetry; attempt++ {
		// --progress forces git to emit "Counting objects...", "Writing objects..."
		// etc to stderr even though stdout/stderr are not a terminal. Without it a
		// push that is slow but succeeding prints NOTHING until it is completely
		// done (verified: 0 bytes at 300ms, vs 462 bytes of progress with
		// --progress at the same point) — so the "no output = hung" guard was
		// killing pushes that were actually working, not just ones that never
		// connected.
		pushCmd := fmt.Sprintf("cd %q && git push --progress %q %s", repoDir, pushURL, branch)
		r := gitGuard(pushCmd, repoDir, gitStartWindow, gitPushMaxRun)
		if !r.Started {
			log.Printf("git_push: attempt %d/%d start timeout (%s, no progress output at all — likely never connected), retrying",
				attempt, maxRetry, gitStartWindow)
			continue // no sleep — only waited the start window
		}
		if r.TimedOut {
			// it DID produce progress output (otherwise Started would be false),
			// so it connected and was moving — just too slowly to finish within
			// gitPushMaxRun. Worth a fresh retry (a new connection often does
			// better), but this is a different failure mode than "never
			// connected" and is logged as such.
			log.Printf("git_push: attempt %d/%d connected and made progress but did not finish within %s, retrying: %s",
				attempt, maxRetry, gitPushMaxRun, truncStr(lastProgressLine(r.Out), 160))
			continue
		}
		result := r.Out
		// a rejected push prints "! [rejected] a -> a (non-fast-forward)": that line
		// contains "->" too, and used to be read as success. Retrying the same push
		// cannot change a rejection, so stop with the reason.
		if strings.Contains(result, "[rejected]") || strings.Contains(result, "failed to push some refs") ||
			strings.Contains(result, "non-fast-forward") {
			return fmt.Errorf("push of %s rejected by the remote (%s) — not retrying: %s", branch,
				"non-fast-forward / rejected", truncStr(strings.TrimSpace(result), 160))
		}
		// success indicators
		if strings.Contains(result, "->") || strings.Contains(result, "Everything up-to-date") || strings.Contains(result, "already exists") {
			log.Printf("git_push: %s:%s ok (attempt %d)", ownerRepo, branch, attempt)
			return nil
		}
		log.Printf("git_push: attempt %d/%d failed: %s", attempt, maxRetry, truncStr(result, 200))
		if gitAuthFailure(result) {
			return fmt.Errorf("push rejected by GitHub (credentials invalid, expired, or without write access to %s) — not retrying: %s",
				ownerRepo, truncStr(strings.TrimSpace(result), 160))
		}
		if attempt < maxRetry {
			time.Sleep(10 * time.Second)
		}
	}
	return fmt.Errorf("push failed after %d attempts", maxRetry)
}

// CreateGitHubPR opens a pull request via the GitHub API. Returns PR URL or error.
func CreateGitHubPR(owner, repo, head, base, title, body string) (string, error) {
	token := GetGHToken()
	if token == "" {
		return "", fmt.Errorf("no GitHub token")
	}

	const maxAttempts = 10
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		jsonBody := fmt.Sprintf(`{"title":%q,"body":%q,"head":%q,"base":%q}`, title, body, head, base)
		cmd := fmt.Sprintf(`curl -s -w "\n%%{http_code}" -X POST `+
			`-H "Authorization: token %s" `+
			`-H "Accept: application/vnd.github.v3+json" `+
			`"https://api.github.com/repos/%s/%s/pulls" `+
			`-d '%s' 2>&1`, token, owner, repo, jsonBody)

		result, started := gitStartGuard(cmd, "/tmp")
		if !started {
			log.Printf("create_pr: attempt %d/%d start timeout (TLS), retrying", attempt, maxAttempts)
			continue
		}

		lines := strings.Split(strings.TrimSpace(result), "\n")
		httpCode := ""
		if len(lines) > 0 {
			httpCode = lines[len(lines)-1]
		}
		prBody := strings.Join(lines[:len(lines)-1], "\n")

		if httpCode == "201" {
			log.Printf("create_pr: %s/%s %s→%s: HTTP %s", owner, repo, head, base, httpCode)
			return prBody, nil
		}
		// 422 "already exists" is success; 422 "head invalid" means conv branch
		// didn't push — retry (the branch might land on a later attempt).
		if httpCode == "422" {
			if strings.Contains(prBody, "already exists") {
				log.Printf("create_pr: %s/%s %s→%s: HTTP 422 (already exists)", owner, repo, head, base)
				return prBody, nil
			}
			log.Printf("create_pr: attempt %d/%d HTTP 422 head invalid — conv branch may not have pushed, retrying",
				attempt, maxAttempts)
			if attempt < maxAttempts {
				time.Sleep(10 * time.Second)
			}
			continue
		}

		if httpCode == "401" {
			return "", fmt.Errorf("create PR rejected by GitHub (HTTP 401: token invalid or expired) — not retrying: %s", truncStr(prBody, 160))
		}
		log.Printf("create_pr: attempt %d/%d got HTTP %s, retrying", attempt, maxAttempts, httpCode)
		if attempt < maxAttempts {
			time.Sleep(10 * time.Second)
		}
	}
	return "", fmt.Errorf("PR creation failed after %d attempts", maxAttempts)
}

// PriorChunksContext builds the [PRIOR WORK] section for a theory chunk prompt.
// Returns empty string if no prior results.
func PriorChunksContext(priorCount int, devRepo string) string {
	if priorCount == 0 || devRepo == "" {
		return ""
	}
	return fmt.Sprintf(
		"[PRIOR WORK — %d earlier chunks already produced theory for other files]\n"+
			"you can READ prior theory for context:\n"+
			"  git clone %s /tmp/devrepo 2>/dev/null\n"+
			"  cd /tmp/devrepo && git checkout einsteinOS/theory 2>/dev/null\n"+
			"  ls .einsteinOS/chunks/\n"+
			"this is READ-ONLY context. do NOT git push, git commit, or store anything there.\n"+
			"the kernel stores your output automatically after you finish.\n"+
			"produce theory for YOUR assigned files, then stop.\n\n",
		priorCount, CloneURL(devRepo))
}

func truncStr(s string, n int) string {
	s = redactSecrets(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
