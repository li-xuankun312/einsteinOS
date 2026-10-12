package kernel

import (
	"fmt"
	"log"
	"strings"
	"sync"
)

type Theorem struct {
	Repo   string
	SHA    string
	Theory string
	ReadBy int64
}

var (
	theoremTable [NR_THEOREMS]Theorem
	theoremCount int
	theoremMu    sync.Mutex
)

const NR_THEOREMS = 256

func RefInit(task string, refs []RefRepo) []RefRepo {
	if len(refs) > 0 {
		for _, ref := range refs {
			refCloneLocal(ref.Owner, ref.Name)
		}
		return refs
	}
	refs = refDiscover(task)
	for _, ref := range refs {
		refCloneLocal(ref.Owner, ref.Name)
	}
	return refs
}

func refCloneLocal(owner, name string) {
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, name)
	_, err := RefClone(owner, name, dir)
	if err != nil {
		log.Printf("ref_init: clone %s/%s: %v", owner, name, err)
		return
	}
	log.Printf("ref_init: %s/%s -> %s", owner, name, dir)
}

func refDiscover(task string) []RefRepo {
	prompt := task + "\n" +
		"what reference repositories on github should be studied " +
		"before developing this? " +
		"reply with ONLY owner/name lines, nothing else."

	pid, err := Fork(prompt, ".")
	if err != nil {
		log.Printf("ref_discover: fork: %v", err)
		return nil
	}
	log.Printf("ref_discover: forked pid %d", pid)

	Wait(pid)
	raw := GetResult(pid)
	return parseRefRepos(raw)
}

func RefReadFirst(pid int64, owner, name string) string {
	commit, diff, err := RefFirstCommit(owner, name)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	stdin := fmt.Sprintf("repo: %s/%s\ncommit: %s %s\nfiles: %d\n\n%s",
		owner, name, commit.SHA, commit.Message,
		len(commit.Files), truncDiff(diff, 8000))

	childPrompt := stdin + "\n" +
		"read every line above. " +
		"extract pure mathematical definitions and theorems. " +
		"no code references, no function names. " +
		"only math: sets, maps, partial orders, invariants, bounds. " +
		"each theorem must stand alone without any programming language."

	childPid, err := Fork(childPrompt, ".")
	if err != nil {
		return fmt.Sprintf("ref_read_first fork: %v", err)
	}
	log.Printf("ref_theorize: pid %d forked child %d for %s/%s first commit",
		pid, childPid, owner, name)

	Wait(childPid)
	theory := GetResult(childPid)

	theoremStore(owner+"/"+name, commit.SHA, theory, childPid)

	log.Printf("ref_theorize: %s/%s first commit -> %d bytes",
		owner, name, len(theory))
	return theory
}

func RefReadCommitTheory(pid int64, owner, name, sha string) string {
	diff, files, err := RefReadCommit(owner, name, sha)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	stdin := fmt.Sprintf("repo: %s/%s\ncommit: %s\nfiles: %s\n\n%s",
		owner, name, sha,
		strings.Join(files, ", "),
		truncDiff(diff, 8000))

	childPrompt := stdin + "\n" +
		"read every line. " +
		"extract pure mathematical theory: " +
		"definitions, theorems, invariants, cost bounds. " +
		"no code, no function names, only math."

	childPid, err := Fork(childPrompt, ".")
	if err != nil {
		return fmt.Sprintf("ref_read_commit fork: %v", err)
	}
	log.Printf("ref_theorize: pid %d forked child %d for %s",
		pid, childPid, sha[:12])

	Wait(childPid)
	theory := GetResult(childPid)

	theoremStore(owner+"/"+name, sha, theory, childPid)
	return theory
}

func theoremStore(repo, sha, theory string, childPid int64) {
	theoremMu.Lock()
	defer theoremMu.Unlock()
	if theoremCount < NR_THEOREMS {
		theoremTable[theoremCount] = Theorem{
			Repo:   repo,
			SHA:    sha,
			Theory: theory,
			ReadBy: childPid,
		}
		theoremCount++
	}
}

func GetTheorem(repo, sha string) string {
	theoremMu.Lock()
	defer theoremMu.Unlock()
	for i := 0; i < theoremCount; i++ {
		t := theoremTable[i]
		if t.Repo == repo && (sha == "" || strings.HasPrefix(t.SHA, sha)) {
			return t.Theory
		}
	}
	return ""
}

func GetAllTheorems(repo string) []Theorem {
	theoremMu.Lock()
	defer theoremMu.Unlock()
	var out []Theorem
	for i := 0; i < theoremCount; i++ {
		if theoremTable[i].Repo == repo || repo == "" {
			out = append(out, theoremTable[i])
		}
	}
	return out
}

func TheoremList() string {
	theoremMu.Lock()
	defer theoremMu.Unlock()
	if theoremCount == 0 {
		return "no theorems extracted"
	}
	out := fmt.Sprintf("  %-30s %-14s %-8s %s\n",
		"REPO", "SHA", "BYTES", "CHILD")
	for i := 0; i < theoremCount; i++ {
		t := theoremTable[i]
		sha := t.SHA
		if len(sha) > 12 {
			sha = sha[:12]
		}
		out += fmt.Sprintf("  %-30s %-14s %-8d pid=%d\n",
			t.Repo, sha, len(t.Theory), t.ReadBy)
	}
	return out
}

func RefInitSummary() string {
	theoremMu.Lock()
	defer theoremMu.Unlock()
	refMu.Lock()
	defer refMu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("ref repos: %d  theorems: %d/%d\n",
		len(refRepos), theoremCount, NR_THEOREMS))
	for key, r := range refRepos {
		sb.WriteString(fmt.Sprintf("  %s: %d commits\n", key, len(r.Commits)))
	}
	return sb.String()
}
