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

type theorem_entry struct {
	repo    string
	sha     string
	theory  string
	read_by int64
}

var (
	theorem_table [NR_THEOREMS]theorem_entry
	nr_theorems   int
	theorem_mu    sync.Mutex
)

const NR_THEOREMS = 256

var theorem_writeback func(repo, sha, theory string)

func SetTheoremWriteback(fn func(string, string, string)) { theorem_writeback = fn }

func ref_clone(owner, name string) {
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, name)
	_, err := RefClone(owner, name, dir)
	if err != nil {
		log.Printf("ref_init: %s/%s: %v", owner, name, err)
	}
}

const MAX_REFS = 3

func ref_discover(task string) []RefRepo {
	prompt := task + "\n" +
		"what are the top 3 most relevant reference repositories on github " +
		"for developing this? " +
		"reply with ONLY owner/name lines, max 3, nothing else."

	var good []RefRepo
	var tried int

	for tried < 3 {
		tried++
		pid, err := Fork(prompt, ".")
		if err != nil {
			log.Printf("ref_discover: fork: %v", err)
			return good
		}
		log.Printf("ref_discover: attempt %d pid %d", tried, pid)
		Wait(pid)

		candidates := parseRefRepos(GetResult(pid))
		if len(candidates) > MAX_REFS {
			candidates = candidates[:MAX_REFS]
		}
		if len(candidates) == 0 {
			return good
		}

		var notfound []string
		for _, r := range candidates {
			ref_clone(r.Owner, r.Name)
			refMu.Lock()
			_, ok := refRepos[r.Owner+"/"+r.Name]
			refMu.Unlock()
			if ok {
				good = append(good, r)
			} else if ref_clone_is_notfound(r.Owner, r.Name) {
				notfound = append(notfound, r.Owner+"/"+r.Name)
			} else {
				log.Printf("ref_discover: %s/%s network error, skip", r.Owner, r.Name)
			}
		}

		if len(notfound) == 0 {
			return good
		}

		log.Printf("ref_discover: not found: %v", notfound)
		prompt = fmt.Sprintf(
			"these repos do not exist on github: %s\n"+
				"correct the owner/name. reply with ONLY owner/name lines.",
			strings.Join(notfound, ", "))
	}
	return good
}

func ref_clone_is_notfound(owner, name string) bool {
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, name)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote",
		fmt.Sprintf("https://github.com/%s/%s.git", owner, name))
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	err := cmd.Run()
	os.RemoveAll(dir)
	return err != nil
}

func ref_theorize(parent int64, owner, name, sha, diff string, nfiles int, msg string) string {
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, name)

	files, err := RefTreeAt(owner, name, sha)
	if err != nil {
		files = nil
	}

	var src strings.Builder
	src.WriteString(fmt.Sprintf("commit %s: %s\n%d files\n\n", sha[:12], msg, nfiles))

	for _, f := range files {
		content, e := RefFileAt(owner, name, sha, f)
		if e != nil {
			continue
		}
		if len(content) > 50000 {
			content = content[:50000]
		}
		src.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", f, strings.Count(content, "\n")+1))
		src.WriteString(content)
		src.WriteString("\n\n")
	}

	src_text := src.String()
	if len(src_text) > 120000 {
		src_text = src_text[:120000]
	}

	prompt := src_text + "\n" +
		"above is every file in commit " + sha[:12] + " of " + owner + "/" + name + ".\n" +
		"for each semantic unit write pure LaTeX mathematics: " +
		"definitions, theorems, lemmas, proof sketches.\n" +
		"no code, no function names, no variable names. only math.\n" +
		"the repo is also at " + dir + " if you need git for anything else."

	child, err := Fork(prompt, ".")
	if err != nil {
		return fmt.Sprintf("ref_theorize fork: %v", err)
	}
	log.Printf("ref_theorize: parent=%d child=%d %s/%s %s (%d files fed)",
		parent, child, owner, name, sha[:12], len(files))
	Wait(child)
	theory := GetResult(child)
	theorem_store(owner+"/"+name, sha, theory, child)
	return theory
}

func theorem_store(repo, sha, theory string, child int64) {
	theorem_mu.Lock()
	defer theorem_mu.Unlock()
	if nr_theorems < NR_THEOREMS {
		theorem_table[nr_theorems] = theorem_entry{
			repo: repo, sha: sha, theory: theory, read_by: child,
		}
		nr_theorems++
	}
	if theorem_writeback != nil {
		go theorem_writeback(repo, sha, theory)
	}
}

func theorem_get(repo, sha string) string {
	theorem_mu.Lock()
	defer theorem_mu.Unlock()
	for i := 0; i < nr_theorems; i++ {
		t := theorem_table[i]
		if t.repo == repo && (sha == "" || strings.HasPrefix(t.sha, sha)) {
			return t.theory
		}
	}
	return ""
}

func theorem_get_all(repo string) string {
	theorem_mu.Lock()
	defer theorem_mu.Unlock()
	var sb strings.Builder
	for i := 0; i < nr_theorems; i++ {
		t := theorem_table[i]
		if t.repo == repo && t.theory != "" {
			sb.WriteString(fmt.Sprintf("\n%% === commit %s ===\n", short_sha(t.sha)))
			sb.WriteString(t.theory)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func theorem_count(repo string) int {
	theorem_mu.Lock()
	defer theorem_mu.Unlock()
	n := 0
	for i := 0; i < nr_theorems; i++ {
		if theorem_table[i].repo == repo {
			n++
		}
	}
	return n
}

func theorem_list() string {
	theorem_mu.Lock()
	defer theorem_mu.Unlock()
	if nr_theorems == 0 {
		return "no theorems"
	}
	out := fmt.Sprintf("  %-30s %-14s %-8s %s\n", "REPO", "SHA", "BYTES", "PID")
	for i := 0; i < nr_theorems; i++ {
		t := theorem_table[i]
		sha := t.sha
		if len(sha) > 12 {
			sha = sha[:12]
		}
		out += fmt.Sprintf("  %-30s %-14s %-8d %d\n",
			t.repo, sha, len(t.theory), t.read_by)
	}
	return out
}

func truncDiff(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func RefInitSummary() string {
	theorem_mu.Lock()
	defer theorem_mu.Unlock()
	refMu.Lock()
	defer refMu.Unlock()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("refs: %d  theorems: %d/%d\n",
		len(refRepos), nr_theorems, NR_THEOREMS))
	for key, r := range refRepos {
		sb.WriteString(fmt.Sprintf("  %s: %d commits\n", key, len(r.Commits)))
	}
	return sb.String()
}
