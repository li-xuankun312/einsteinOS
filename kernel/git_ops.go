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

// GitCloneOrReuse clones a repo to dir if not already there, returns the dir.
func GitCloneOrReuse(ownerRepo, dir string) error {
	if _, err := os.Stat(dir + "/.git"); err == nil {
		return nil // already cloned
	}
	cmd := fmt.Sprintf("git clone %q %q 2>&1", CloneURL(ownerRepo), dir)
	pid, err := ForkExec(cmd, "/tmp")
	if err != nil {
		return fmt.Errorf("clone fork: %w", err)
	}
	Wait(pid)
	result := GetResult(pid)
	if _, err := os.Stat(dir + "/.git"); err != nil {
		return fmt.Errorf("clone failed: %s", result)
	}
	return nil
}

// GitCheckoutOrCreate checks out a branch, creating it if needed.
func GitCheckoutOrCreate(repoDir, branch, baseBranch string) error {
	cmd := fmt.Sprintf("cd %q && git fetch origin 2>/dev/null; "+
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
func GitPushRetry(repoDir, ownerRepo, branch string, maxRetry int) error {
	pushURL := CloneURL(ownerRepo)
	for attempt := 1; attempt <= maxRetry; attempt++ {
		cmd := fmt.Sprintf("cd %q && git push %q %s 2>&1", repoDir, pushURL, branch)
		pid, err := ForkExec(cmd, repoDir)
		if err != nil {
			log.Printf("git_push: attempt %d/%d fork error: %v", attempt, maxRetry, err)
			time.Sleep(10 * time.Second)
			continue
		}
		Wait(pid)
		result := GetResult(pid)
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
