package kernel

import (
	"fmt"
	"log"
	"os"
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

// gitStartGuard runs a git command with a 2-second start guard.
// Spawns git in background, a monitor sleeps 2s then checks if git
// produced any output. No output in 2s = TLS handshake hung → kill & return false.
// Mimics the human ctrl+c strategy on stuck git commands.
func gitStartGuard(gitCmd string, workDir string) (string, bool) {
	script := "tmpout=$(mktemp)\n" +
		gitCmd + " >\"$tmpout\" 2>&1 &\n" +
		"gpid=$!\n" +
		"sleep 5\n" +
		"if [ ! -s \"$tmpout\" ]; then\n" +
		"  kill $gpid 2>/dev/null; wait $gpid 2>/dev/null\n" +
		"  rm -f \"$tmpout\"\n" +
		"  exit 124\n" +
		"fi\n" +
		"wait $gpid\n" +
		"code=$?\n" +
		"cat \"$tmpout\"\n" +
		"rm -f \"$tmpout\"\n" +
		"exit $code"

	pid, err := ForkExec(script, workDir)
	if err != nil {
		return "", false
	}
	Wait(pid)
	result := GetResult(pid)
	if strings.TrimSpace(result) == "" {
		return "", false // exit 124 produces no stdout → start timeout
	}
	return result, true
}

// GitCloneOrReuse ensures dir is a usable clone of ownerRepo, synced to remote HEAD.
// If .git exists → fetch + reset (TLS may fail, retry with 2s start guard).
// If .git missing or corrupt → remove residue, clone fresh (2s start guard).
// 10 attempts — each start-timeout attempt only costs 2s, not 30-60s.
func GitCloneOrReuse(ownerRepo, dir string) error {
	var lastErr error
	for attempt := 1; attempt <= 10; attempt++ {
		if _, err := os.Stat(dir + "/.git"); err == nil {
			// .git exists — fetch, then reset locally.
			// do NOT rm -rf: the local repo is valid, only the network failed.
			fetchCmd := fmt.Sprintf("git -C %q fetch origin", dir)
			result, started := gitStartGuard(fetchCmd, dir)
			if !started {
				log.Printf("git_clone_or_reuse: %s attempt %d/10 fetch start timeout (5s)", ownerRepo, attempt)
				lastErr = fmt.Errorf("fetch start timeout")
				continue // no sleep — only waited 2s
			}
			if strings.Contains(result, "GnuTLS") || strings.Contains(result, "TLS handshake") ||
				strings.Contains(result, "the remote end hung up") {
				lastErr = fmt.Errorf("fetch TLS error: %s", truncStr(result, 200))
				log.Printf("git_clone_or_reuse: %s attempt %d/10 fetch TLS error", ownerRepo, attempt)
				continue
			}
			// fetch ok — reset to remote HEAD (local operation, no guard needed)
			resetCmd := fmt.Sprintf("cd %q && git reset --hard origin/HEAD 2>&1", dir)
			rpid, rErr := ForkExec(resetCmd, dir)
			if rErr == nil {
				Wait(rpid)
			}
			log.Printf("git_clone_or_reuse: %s synced (attempt %d)", ownerRepo, attempt)
			return nil
		}

		// .git missing or corrupt — clean residue, clone fresh
		os.RemoveAll(dir)
		cloneCmd := fmt.Sprintf("git clone %q %q", CloneURL(ownerRepo), dir)
		_, started := gitStartGuard(cloneCmd, "/tmp")
		if !started {
			os.RemoveAll(dir)
			log.Printf("git_clone_or_reuse: %s attempt %d/10 clone start timeout (5s)", ownerRepo, attempt)
			lastErr = fmt.Errorf("clone start timeout")
			continue // no sleep — only waited 2s
		}
		if _, err := os.Stat(dir + "/.git"); err != nil {
			os.RemoveAll(dir)
			lastErr = fmt.Errorf("clone failed")
			log.Printf("git_clone_or_reuse: %s attempt %d/10 clone failed", ownerRepo, attempt)
			continue
		}
		log.Printf("git_clone_or_reuse: %s cloned (attempt %d)", ownerRepo, attempt)
		return nil
	}
	return fmt.Errorf("GitCloneOrReuse %s failed after 10 attempts: %v", ownerRepo, lastErr)
}

// GitCheckoutOrCreate checks out a branch, creating it if needed.
// No fetch — caller (GitCloneOrReuse) already synced to remote HEAD.
// An extra fetch here hangs on TLS-flaky networks with no timeout.
func GitCheckoutOrCreate(repoDir, branch, baseBranch string) error {
	cmd := fmt.Sprintf("cd %q && "+
		"git checkout %s 2>/dev/null || (git checkout %s 2>/dev/null || git checkout -b %s 2>/dev/null; git checkout -b %s 2>/dev/null)",
		repoDir, branch, baseBranch, baseBranch, branch)
	pid, err := ForkExec(cmd, repoDir)
	if err != nil {
		return err
	}
	Wait(pid)
	return nil
}

// GitCommit stages all and commits with message. Returns true if commit created.
func GitCommit(repoDir, message string) bool {
	cmd := fmt.Sprintf("cd %q && "+
		"git config user.email 'einsteinOS@kernel' 2>/dev/null; "+
		"git config user.name 'einsteinOS' 2>/dev/null; "+
		"git add -A && git commit -m %q --allow-empty 2>&1",
		repoDir, message)
	pid, err := ForkExec(cmd, repoDir)
	if err != nil {
		return false
	}
	Wait(pid)
	return true
}

// GitPushRetry pushes a branch with retry on TLS/network failures.
// Uses 2s start guard — if TLS hangs, kill and retry immediately (no 10s sleep).
// 10 attempts: each start-timeout costs 2s, each real failure gets 10s backoff.
func GitPushRetry(repoDir, ownerRepo, branch string, maxRetry int) error {
	pushURL := CloneURL(ownerRepo)
	if maxRetry < 10 {
		maxRetry = 10 // with 2s start guard, attempts are cheap
	}
	for attempt := 1; attempt <= maxRetry; attempt++ {
		pushCmd := fmt.Sprintf("cd %q && git push %q %s", repoDir, pushURL, branch)
		result, started := gitStartGuard(pushCmd, repoDir)
		if !started {
			log.Printf("git_push: attempt %d/%d start timeout (5s), retrying", attempt, maxRetry)
			continue // no sleep — only waited 2s
		}
		// success indicators
		if strings.Contains(result, "->") || strings.Contains(result, "Everything up-to-date") || strings.Contains(result, "already exists") {
			log.Printf("git_push: %s:%s ok (attempt %d)", ownerRepo, branch, attempt)
			return nil
		}
		log.Printf("git_push: attempt %d/%d failed: %s", attempt, maxRetry, truncStr(result, 200))
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

	for attempt := 1; attempt <= 3; attempt++ {
		jsonBody := fmt.Sprintf(`{"title":%q,"body":%q,"head":%q,"base":%q}`, title, body, head, base)
		cmd := fmt.Sprintf(`curl -s -w "\n%%{http_code}" -X POST `+
			`-H "Authorization: token %s" `+
			`-H "Accept: application/vnd.github.v3+json" `+
			`"https://api.github.com/repos/%s/%s/pulls" `+
			`-d '%s' 2>&1`, token, owner, repo, jsonBody)

		pid, err := ForkExec(cmd, "/tmp")
		if err != nil {
			log.Printf("create_pr: attempt %d/3 fork error: %v", attempt, err)
			time.Sleep(10 * time.Second)
			continue
		}
		Wait(pid)
		result := GetResult(pid)

		lines := strings.Split(strings.TrimSpace(result), "\n")
		httpCode := ""
		if len(lines) > 0 {
			httpCode = lines[len(lines)-1]
		}

		if httpCode == "201" || httpCode == "422" {
			// 201 = created, 422 = already exists
			prBody := strings.Join(lines[:len(lines)-1], "\n")
			log.Printf("create_pr: %s/%s %s→%s: HTTP %s", owner, repo, head, base, httpCode)
			return prBody, nil
		}

		log.Printf("create_pr: attempt %d/3 got HTTP %s, retrying", attempt, httpCode)
		if attempt < 3 {
			time.Sleep(10 * time.Second)
		}
	}
	return "", fmt.Errorf("PR creation failed after 3 attempts")
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
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
