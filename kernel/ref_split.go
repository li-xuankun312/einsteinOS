package kernel

import (
	"fmt"
	"log"
	"strings"
	"sync"
)

type SplitNode struct {
	Pid      int64
	Parent   int64
	Depth    int
	Files    []string
	SHA      string
	Owner    string
	Repo     string
	Result   string
	Children []int64
}

var (
	split_table [NR_SPLITS]SplitNode
	nr_splits   int
	split_mu    sync.Mutex
)

const NR_SPLITS = 256
const SPLIT_CHUNK = 40

func ref_split(parent_pid int64, owner, repo, sha string, files []string, task string, depth int) string {
	if len(files) <= SPLIT_CHUNK {
		return ref_split_leaf(parent_pid, owner, repo, sha, files, task, depth)
	}

	var chunks [][]string
	for i := 0; i < len(files); i += SPLIT_CHUNK {
		end := i + SPLIT_CHUNK
		if end > len(files) {
			end = len(files)
		}
		chunks = append(chunks, files[i:end])
	}

	log.Printf("ref_split: %s/%s@%s depth=%d %d files -> %d chunks",
		owner, repo, short_sha(sha), depth, len(files), len(chunks))

	node := split_alloc(parent_pid, depth, owner, repo, sha, files)

	var child_pids []int64
	for i, chunk := range chunks {
		child_pid := ref_split_fork(parent_pid, owner, repo, sha, chunk, task, depth+1, i, len(chunks))
		if child_pid > 0 {
			child_pids = append(child_pids, child_pid)
		}
	}

	if node >= 0 {
		split_mu.Lock()
		split_table[node].Children = child_pids
		split_mu.Unlock()
	}

	var results []string
	for _, cpid := range child_pids {
		Wait(cpid)
		results = append(results, GetResult(cpid))
	}

	merged := strings.Join(results, "\n\n")
	log.Printf("ref_split: %s/%s@%s depth=%d merged %d children -> %d bytes",
		owner, repo, short_sha(sha), depth, len(child_pids), len(merged))
	return merged
}

func ref_split_fork(parent_pid int64, owner, repo, sha string, files []string, task string, depth int, chunk_idx int, chunk_total int) int64 {
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, repo)

	var src strings.Builder
	src.WriteString(fmt.Sprintf("[TASK] %s\n\n", task))
	src.WriteString(fmt.Sprintf("[REFERENCE] %s/%s commit %s\n", owner, repo, short_sha(sha)))
	src.WriteString(fmt.Sprintf("[CHUNK %d/%d] %d files (depth %d)\n\n", chunk_idx+1, chunk_total, len(files), depth))

	for _, f := range files {
		content, err := RefFileAt(owner, repo, sha, f)
		if err != nil {
			continue
		}
		if len(content) > 30000 {
			content = content[:30000]
		}
		src.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", f, strings.Count(content, "\n")+1))
		src.WriteString(content)
		src.WriteString("\n\n")
	}

	src_text := src.String()
	if len(src_text) > 100000 {
		src_text = src_text[:100000]
	}

	prompt := src_text +
		"for each semantic unit in these files write pure LaTeX mathematics.\n" +
		"no code, no function names, no variable names. only math.\n" +
		"the repo is at " + dir + " if you need git.\n" +
		"if any single file is too large to fully analyze, " +
		"list its functions and write math for the most important ones."

	pid, err := Fork(prompt, ".")
	if err != nil {
		log.Printf("ref_split_fork: %v", err)
		return 0
	}

	split_alloc(parent_pid, depth, owner, repo, sha, files)

	log.Printf("ref_split_fork: pid=%d chunk=%d/%d depth=%d files=%d",
		pid, chunk_idx+1, chunk_total, depth, len(files))
	return pid
}

func ref_split_leaf(parent_pid int64, owner, repo, sha string, files []string, task string, depth int) string {
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, repo)

	var src strings.Builder
	src.WriteString(fmt.Sprintf("[TASK] %s\n\n", task))
	src.WriteString(fmt.Sprintf("[REFERENCE] %s/%s commit %s\n", owner, repo, short_sha(sha)))
	src.WriteString(fmt.Sprintf("[LEAF] %d files (depth %d)\n\n", len(files), depth))

	for _, f := range files {
		content, err := RefFileAt(owner, repo, sha, f)
		if err != nil {
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

	prompt := src_text +
		"for each semantic unit write pure LaTeX mathematics.\n" +
		"no code, no function names, no variable names. only math.\n" +
		"the repo is at " + dir + " if you need git."

	pid, err := Fork(prompt, ".")
	if err != nil {
		return fmt.Sprintf("ref_split_leaf: %v", err)
	}

	log.Printf("ref_split_leaf: pid=%d depth=%d files=%d", pid, depth, len(files))
	Wait(pid)
	result := GetResult(pid)
	theorem_store(owner+"/"+repo, sha, result, pid)
	return result
}

func split_alloc(parent int64, depth int, owner, repo, sha string, files []string) int {
	split_mu.Lock()
	defer split_mu.Unlock()
	if nr_splits >= NR_SPLITS {
		return -1
	}
	idx := nr_splits
	split_table[idx] = SplitNode{
		Parent: parent,
		Depth:  depth,
		Owner:  owner,
		Repo:   repo,
		SHA:    sha,
		Files:  files,
	}
	nr_splits++
	return idx
}

func ref_split_commit(parent_pid int64, owner, repo string, commits []RefCommit, task string) string {
	if len(commits) <= 1 {
		if len(commits) == 0 {
			return ""
		}
		c := commits[0]
		files, _ := RefTreeAt(owner, repo, c.SHA)
		if len(files) > SPLIT_CHUNK {
			return ref_split(parent_pid, owner, repo, c.SHA, files, task, 0)
		}
		return ref_split_leaf(parent_pid, owner, repo, c.SHA, files, task, 0)
	}

	log.Printf("ref_split_commit: %s/%s %d commits", owner, repo, len(commits))

	var pids []int64
	for _, c := range commits {
		_, changed_files, _ := RefReadCommit(owner, repo, c.SHA)
		if len(changed_files) == 0 {
			files, _ := RefTreeAt(owner, repo, c.SHA)
			changed_files = files
		}

		if len(changed_files) > SPLIT_CHUNK {
			go func(commit RefCommit, files []string) {
				ref_split(parent_pid, owner, repo, commit.SHA, files, task, 0)
			}(c, changed_files)
			continue
		}

		pid := ref_split_fork(parent_pid, owner, repo, c.SHA, changed_files, task, 0, 0, 1)
		if pid > 0 {
			pids = append(pids, pid)
		}
	}

	var results []string
	for _, pid := range pids {
		Wait(pid)
		results = append(results, GetResult(pid))
	}

	merged := strings.Join(results, "\n\n")
	log.Printf("ref_split_commit: %s/%s merged %d commits -> %d bytes",
		owner, repo, len(commits), len(merged))
	return merged
}

func SplitStatus() string {
	split_mu.Lock()
	defer split_mu.Unlock()
	if nr_splits == 0 {
		return "no splits"
	}
	out := fmt.Sprintf("  %-6s %-6s %-5s %-30s %-6s %s\n",
		"IDX", "PARENT", "DEPTH", "REPO", "FILES", "CHILDREN")
	for i := 0; i < nr_splits; i++ {
		s := split_table[i]
		out += fmt.Sprintf("  %-6d %-6d %-5d %-30s %-6d %v\n",
			i, s.Parent, s.Depth,
			s.Owner+"/"+s.Repo, len(s.Files), s.Children)
	}
	return out
}
