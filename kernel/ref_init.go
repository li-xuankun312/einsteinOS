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

const theorize_instruction = `for each function / struct / method in the code above, produce TWO layers:

[LAYER 1 — MATH]
formal specification: types, preconditions, postconditions, invariants.
use LaTeX. example:
  \text{SetPosition}: \mathcal{S} \times \mathbb{Z}_{\geq 0} \to \{\top, \bot\},
  \quad s.\text{pos} \leftarrow p \iff s.\text{handle} \neq \text{null}

[LAYER 2 — FORM TEMPLATE]
rewrite every identifier with its structural role:
  - project namespace  → prjname
  - adjective prefix   → adj
  - object class       → Obj
  - type alias         → typenameType
  - member handle      → objtype
  - local result var   → argname
  - callback/closure   → cbname
  - index/counter      → idx
preserve the code structure, braces, control flow, operators exactly.
example — before:
  bool LocalFileStream::SetPosition(musik::core::sdk::PositionType position) {
      if (!this->file) return false;
      return fseek(this->file, (long)position, SEEK_SET) == 0;
  }
after:
  bool adjObjStream::SetPosition(prjname::core::sdk::typenameType position) {
      if (!this->objtype) return false;
      return fseek(this->objtype, (long)position, SEEK_SET) == 0;
  }

both layers are required for every semantic unit. do not skip either layer.
the form template is not regex — you must understand each identifier's ROLE
(handle vs argument vs return value vs namespace) to assign the right tag.
`

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

var (
	dev_repos    = make(map[string]bool)
	dev_repos_mu sync.Mutex
)

func RegisterDevRepo(owner, name string) {
	dev_repos_mu.Lock()
	dev_repos[owner+"/"+name] = true
	dev_repos_mu.Unlock()
	log.Printf("ref_init: registered dev repo %s/%s", owner, name)
}

func is_dev_repo(owner, name string) bool {
	dev_repos_mu.Lock()
	defer dev_repos_mu.Unlock()
	return dev_repos[owner+"/"+name]
}

func detect_dev_repos(task string) {
	markers := []string{
		"项目在", "项目是", "项目：", "项目:",
		"project at", "project in", "project is",
		"我们的仓库", "我们的项目", "our repo",
		"要开发的", "需要开发",
	}
	for _, marker := range markers {
		idx := strings.Index(task, marker)
		if idx < 0 {
			continue
		}
		after := task[idx+len(marker):]
		repos := extractRepoMentions(after)
		if len(repos) > 0 {
			r := repos[0]
			RegisterDevRepo(r.Owner, r.Name)
			return
		}
	}
}

func ref_discover(task string) []RefRepo {
	detect_dev_repos(task)

	var good []RefRepo

	direct := extractRepoMentions(task)
	for _, r := range direct {
		if is_dev_repo(r.Owner, r.Name) {
			log.Printf("ref_discover: skip dev repo %s/%s", r.Owner, r.Name)
			continue
		}
		ref_clone(r.Owner, r.Name)
		refMu.Lock()
		_, ok := refRepos[r.Owner+"/"+r.Name]
		refMu.Unlock()
		if ok {
			good = append(good, r)
			log.Printf("ref_discover: direct mention %s/%s cloned", r.Owner, r.Name)
		} else {
			log.Printf("ref_discover: direct mention %s/%s clone failed", r.Owner, r.Name)
		}
	}
	if len(good) >= MAX_REFS {
		return good[:MAX_REFS]
	}

	need := MAX_REFS - len(good)
	prompt := fmt.Sprintf("[USER TASK]\n%s\n\n"+
		"based on the task above, list %d relevant open-source "+
		"reference repositories on github.\n"+
		"reply with ONLY owner/name lines, one per line, nothing else.\n"+
		"example:\nNVIDIA/nccl\nkvcache-ai/Mooncake\n",
		truncDiff(task, 3000), need)

	var tried int
	for tried < 3 && len(good) < MAX_REFS {
		tried++
		pid, err := Fork(prompt, ".")
		if err != nil {
			log.Printf("ref_discover: fork: %v", err)
			return good
		}
		log.Printf("ref_discover: attempt %d pid %d", tried, pid)
		Wait(pid)

		raw := GetResult(pid)
		log.Printf("ref_discover: attempt %d raw (%d bytes): %s",
			tried, len(raw), truncDiff(raw, 500))

		candidates := parseRefRepos(raw)
		if len(candidates) > MAX_REFS {
			candidates = candidates[:MAX_REFS]
		}
		if len(candidates) == 0 {
			prompt = fmt.Sprintf(
				"your previous response did not contain valid owner/name pairs.\n"+
					"the task is:\n%s\n\n"+
					"reply with ONLY lines like: owner/name\n"+
					"example:\nNVIDIA/nccl\n", truncDiff(task, 2000))
			continue
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

func extractRepoMentions(text string) []RefRepo {
	seen := make(map[string]bool)
	var repos []RefRepo

	skip := map[string]bool{
		"commit": true, "tree": true, "blob": true,
		"issues": true, "pull": true, "releases": true,
		"actions": true, "settings": true, "wiki": true,
	}

	for _, line := range strings.Split(text, "\n") {
		for _, prefix := range []string{"https://github.com/", "github.com/"} {
			idx := strings.Index(line, prefix)
			for idx >= 0 {
				rest := line[idx+len(prefix):]
				if sp := strings.IndexAny(rest, " \t,;:!?)>\"'\u3002\uff0c\u3001\uff1b\uff1a\uff01\uff1f"); sp > 0 {
					rest = rest[:sp]
				}
				parts := strings.SplitN(rest, "/", 3)
				if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
					owner := parts[0]
					name := strings.TrimRight(parts[1], ".,;:!?\u3002\uff0c\u3001\uff1b\uff1a\uff01\uff1f")
					if !skip[name] {
						key := owner + "/" + name
						if !seen[key] && len(owner) > 1 && len(name) > 1 {
							seen[key] = true
							repos = append(repos, RefRepo{Owner: owner, Name: name})
						}
					}
				}
				line = line[idx+len(prefix):]
				idx = strings.Index(line, prefix)
			}
		}
	}
	return repos
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
	files, err := RefTreeAt(owner, name, sha)
	if err != nil {
		files = nil
	}

	task := ref_theorize_task(parent)

	if len(files) > SPLIT_CHUNK {
		log.Printf("ref_theorize: %s/%s %s: %d files -> split",
			owner, name, sha[:12], len(files))
		return ref_split(parent, owner, name, sha, files, task, 0)
	}

	return ref_theorize_leaf(parent, owner, name, sha, files, task)
}

func ref_theorize_task(pid int64) string {
	ctx := GetCOWContext(pid)
	if ctx != nil && ctx.Len() > 0 {
		return ctx.Read(0)
	}
	metaMu.Lock()
	m := taskMeta[pid]
	metaMu.Unlock()
	if m != nil {
		return strip_credentials(m.Prompt)
	}
	return ""
}

func ref_theorize_leaf(parent int64, owner, name, sha string, files []string, task string) string {
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, name)

	var src strings.Builder
	if task != "" {
		src.WriteString(fmt.Sprintf("[TASK] %s\n\n", task))
	}
	src.WriteString(fmt.Sprintf("[REFERENCE] %s/%s commit %s\n%d files\n\n",
		owner, name, sha[:12], len(files)))

	read := 0
	skipped := 0
	for _, f := range files {
		if isBinaryFile(f) {
			skipped++
			continue
		}
		content, e := RefFileAt(owner, name, sha, f)
		if e != nil {
			continue
		}
		lines := strings.Count(content, "\n") + 1
		src.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", f, lines))
		src.WriteString(content)
		src.WriteString("\n\n")
		read += lines
	}

	log.Printf("ref_theorize: %s/%s %s: %d files (%d binary skipped), %d lines fed",
		owner, name, sha[:12], len(files), skipped, read)

	prompt := src.String() + theorize_instruction +
		"the repo is at " + dir + " if you need git."

	child, err := Fork(prompt, ".")
	if err != nil {
		return fmt.Sprintf("ref_theorize fork: %v", err)
	}
	log.Printf("ref_theorize: parent=%d child=%d %s/%s %s (%d files)",
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

func isBinaryFile(path string) bool {
	lower := strings.ToLower(path)
	for _, ext := range []string{
		".pdf", ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".ico", ".svg",
		".zip", ".tar", ".gz", ".bz2", ".xz", ".zst",
		".bin", ".exe", ".so", ".dylib", ".dll", ".a", ".o", ".obj",
		".woff", ".woff2", ".ttf", ".otf", ".eot",
		".mp3", ".mp4", ".wav", ".avi", ".mkv", ".mov",
		".pyc", ".pyo", ".class", ".wasm",
		".pb", ".onnx", ".pt", ".pth", ".safetensors",
	} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
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
