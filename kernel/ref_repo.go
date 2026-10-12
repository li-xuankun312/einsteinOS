package kernel

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type RefCommit struct {
	SHA     string
	Message string
	Author  string
	Date    time.Time
	Files   []string
}

type RefRepoState struct {
	Owner    string
	Name     string
	CloneDir string
	Commits  []RefCommit
	ClonedAt time.Time
}

var (
	refRepos = make(map[string]*RefRepoState)
	refMu    sync.Mutex
)

func RefClone(owner, name, cloneDir string) (*RefRepoState, error) {
	key := owner + "/" + name
	refMu.Lock()
	if r, ok := refRepos[key]; ok {
		refMu.Unlock()
		return r, nil
	}
	refMu.Unlock()

	if _, statErr := os.Stat(cloneDir); statErr == nil {
		verify := exec.Command("git", "--git-dir", cloneDir, "rev-parse", "HEAD")
		if verify.Run() == nil {
			log.Printf("ref_clone: reuse %s", cloneDir)
		} else {
			log.Printf("ref_clone: invalid %s, removing", cloneDir)
			os.RemoveAll(cloneDir)
		}
	}
	if _, statErr := os.Stat(cloneDir); statErr != nil {
		url := fmt.Sprintf("https://github.com/%s/%s.git", owner, name)
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "clone", "--bare", "--filter=blob:none", url, cloneDir)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			os.RemoveAll(cloneDir)
			return nil, fmt.Errorf("git clone %s: %v\n%s", url, err, string(out))
		}
	}

	r := &RefRepoState{
		Owner:    owner,
		Name:     name,
		CloneDir: cloneDir,
		ClonedAt: time.Now(),
	}

	refMu.Lock()
	refRepos[key] = r
	refMu.Unlock()

	return r, nil
}

func RefListCommits(owner, name string) ([]RefCommit, error) {
	key := owner + "/" + name
	refMu.Lock()
	r := refRepos[key]
	refMu.Unlock()
	if r == nil {
		return nil, fmt.Errorf("ref repo %s not cloned", key)
	}

	if len(r.Commits) > 0 {
		return r.Commits, nil
	}

	cmd := exec.Command("git", "--git-dir", r.CloneDir,
		"log", "--format=%H|%an|%aI|%s", "--reverse")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %v", err)
	}

	var commits []RefCommit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		t, _ := time.Parse(time.RFC3339, parts[2])
		commits = append(commits, RefCommit{
			SHA:     parts[0],
			Author:  parts[1],
			Date:    t,
			Message: parts[3],
		})
	}

	refMu.Lock()
	r.Commits = commits
	refMu.Unlock()

	return commits, nil
}

func RefReadCommit(owner, name, sha string) (string, []string, error) {
	key := owner + "/" + name
	refMu.Lock()
	r := refRepos[key]
	refMu.Unlock()
	if r == nil {
		return "", nil, fmt.Errorf("ref repo %s not cloned", key)
	}

	diffCmd := exec.Command("git", "--git-dir", r.CloneDir,
		"diff-tree", "-p", "--stat", sha)
	diffOut, err := diffCmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("git diff-tree %s: %v", sha, err)
	}

	filesCmd := exec.Command("git", "--git-dir", r.CloneDir,
		"diff-tree", "--no-commit-id", "-r", "--name-only", sha)
	filesOut, err := filesCmd.Output()
	if err != nil {
		return string(diffOut), nil, nil
	}

	var files []string
	for _, f := range strings.Split(strings.TrimSpace(string(filesOut)), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}

	return string(diffOut), files, nil
}

func RefFirstCommit(owner, name string) (*RefCommit, string, error) {
	commits, err := RefListCommits(owner, name)
	if err != nil {
		return nil, "", err
	}
	if len(commits) == 0 {
		return nil, "", fmt.Errorf("no commits in %s/%s", owner, name)
	}

	first := &commits[0]
	diff, files, err := RefReadCommit(owner, name, first.SHA)
	if err != nil {
		return first, "", err
	}
	first.Files = files
	return first, diff, nil
}

func RefShowCommit(owner, name, sha string) (string, error) {
	key := owner + "/" + name
	refMu.Lock()
	r := refRepos[key]
	refMu.Unlock()
	if r == nil {
		return "", fmt.Errorf("ref repo %s not cloned", key)
	}

	cmd := exec.Command("git", "--git-dir", r.CloneDir,
		"show", "--stat", "--format=%H%n%an%n%aI%n%B", sha)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show %s: %v", sha, err)
	}
	return string(out), nil
}

func RefFileAt(owner, name, sha, path string) (string, error) {
	key := owner + "/" + name
	refMu.Lock()
	r := refRepos[key]
	refMu.Unlock()
	if r == nil {
		return "", fmt.Errorf("ref repo %s not cloned", key)
	}

	cmd := exec.Command("git", "--git-dir", r.CloneDir,
		"show", sha+":"+path)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show %s:%s: %v", sha, path, err)
	}
	return string(out), nil
}

func RefTreeAt(owner, name, sha string) ([]string, error) {
	key := owner + "/" + name
	refMu.Lock()
	r := refRepos[key]
	refMu.Unlock()
	if r == nil {
		return nil, fmt.Errorf("ref repo %s not cloned", key)
	}

	cmd := exec.Command("git", "--git-dir", r.CloneDir,
		"ls-tree", "-r", "--name-only", sha)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree %s: %v", sha, err)
	}

	var files []string
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

func RefList() string {
	refMu.Lock()
	defer refMu.Unlock()

	if len(refRepos) == 0 {
		return "no reference repos loaded"
	}

	out := fmt.Sprintf("  %-30s %-8s %s\n", "REPO", "COMMITS", "CLONED")
	for key, r := range refRepos {
		out += fmt.Sprintf("  %-30s %-8d %s\n",
			key, len(r.Commits),
			r.ClonedAt.Format("2006-01-02 15:04"))
	}
	return out
}
