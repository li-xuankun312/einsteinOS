package kernel

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
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

const theorize_instruction = `for each function / struct / method in the code above, produce THREE layers:

[LAYER 1a — MATH (LaTeX)]
formal specification: types, preconditions, postconditions, invariants.
use LaTeX. example:
  \text{SetPosition}: \mathcal{S} \times \mathbb{Z}_{\geq 0} \to \{\top, \bot\},
  \quad s.\text{pos} \leftarrow p \iff s.\text{handle} \neq \text{null}

[LAYER 1b — VERIFY (Lean 4)]
translate the LAYER 1a spec into a Lean 4 theorem with proof.
the formal_statement has 'sorry', the content has the real proof.
use omega, decide, simp, norm_num — NOT native_decide.
example:
  -- formal_statement
  theorem set_position_spec (handle : Option Ptr) (pos : Nat) :
    (handle.isSome → set_position handle pos = true) ∧
    (handle.isNone → set_position handle pos = false) := by sorry
  -- proof
  theorem set_position_spec (handle : Option Ptr) (pos : Nat) :
    (handle.isSome → set_position handle pos = true) ∧
    (handle.isNone → set_position handle pos = false) := by
    constructor <;> intro h <;> simp [set_position, h]
mark each theorem with: -- axle_verify: formal_statement / content

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

all three layers are required for every semantic unit. do not skip any layer.
LAYER 1b is verified by machine (Lean 4 kernel) — if you are unsure of the proof,
write the formal_statement with sorry and note "proof_needed" so a later pass can fill it.
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
	dev_repos    = make(map[string]string) // key=owner/name, value=branch (empty="main")
	dev_repos_mu sync.Mutex
)

func RegisterDevRepo(owner, name, branch string) {
	dev_repos_mu.Lock()
	dev_repos[owner+"/"+name] = branch
	dev_repos_mu.Unlock()
	if branch != "" {
		log.Printf("ref_init: registered dev repo %s/%s branch=%s", owner, name, branch)
	} else {
		log.Printf("ref_init: registered dev repo %s/%s", owner, name)
	}
}

func is_dev_repo(owner, name string) bool {
	dev_repos_mu.Lock()
	defer dev_repos_mu.Unlock()
	_, ok := dev_repos[owner+"/"+name]
	return ok
}

func DevRepoBranch(owner, name string) string {
	dev_repos_mu.Lock()
	defer dev_repos_mu.Unlock()
	return dev_repos[owner+"/"+name]
}

func GetDevRepos() []string {
	dev_repos_mu.Lock()
	defer dev_repos_mu.Unlock()
	var out []string
	for k := range dev_repos {
		out = append(out, k)
	}
	return out
}

func detect_dev_repos(task string) {
	repos := extractRepoMentions(task)
	if len(repos) <= 1 {
		// only one repo mentioned — it could be either dev or ref,
		// can't tell without context. let ref_discover's child decide.
		return
	}

	// multiple repos mentioned. fork a lightweight child to identify the dev target.
	var repoList strings.Builder
	for _, r := range repos {
		repoList.WriteString(fmt.Sprintf("  %s/%s\n", r.Owner, r.Name))
	}

	prompt := fmt.Sprintf(
		"repos mentioned:\n%s\nuser request:\n%s\n\n"+
			"reply ONLY two lines:\ndev=owner/name\nbranch=branch_name\n"+
			"dev = the repo to BUILD. branch = target branch mentioned by user (or main if not mentioned).\n",
		repoList.String(), truncDiff(task, 1000),
	)

	child, err := Fork(prompt, ".")
	if err != nil {
		log.Printf("detect_dev_repos: fork failed: %v", err)
		return
	}
	Wait(child)
	result := strings.TrimSpace(GetResult(child))

	var devOwner, devName, devBranch string
	for _, line := range strings.Split(result, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "dev=") {
			key := strings.TrimPrefix(line, "dev=")
			parts := strings.SplitN(key, "/", 2)
			if len(parts) == 2 {
				devOwner = parts[0]
				devName = parts[1]
			}
		} else if strings.HasPrefix(line, "branch=") {
			devBranch = strings.TrimPrefix(line, "branch=")
		}
	}
	if devOwner != "" && devName != "" {
		RegisterDevRepo(devOwner, devName, devBranch)
		return
	}
	log.Printf("detect_dev_repos: child did not identify dev repo, result=%q", result)
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

	// if user already mentioned ref repos, use those — don't add more
	if len(good) > 0 {
		log.Printf("ref_discover: user mentioned %d ref repos, using those only", len(good))
		return good
	}

	need := MAX_REFS

	prompt := fmt.Sprintf("[USER TASK]\n%s\n\n"+
		"based on the task above, list %d relevant open-source "+
		"reference repositories on github.\n"+
		"reply with ONLY owner/name lines, one per line, nothing else.\n",
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
					"one per line, nothing else.\n", truncDiff(task, 2000))
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
					name = strings.TrimSuffix(name, ".git")
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

	// step 1: kernel does static survey (wc -l + extensions)
	heavy, light := fileSurvey(owner, name, sha, files)

	// step 2a: if too many files, fork a dir-level survey child first.
	// child sees directory tree + file counts per dir, decides which dirs
	// are core (keep) vs peripheral (skip: tests, benchmarks, examples, tools, docs).
	// child decides — no hardcoded dir names in kernel.
	totalFiles := len(heavy) + len(light)
	if totalFiles > 200 {
		kept := dirSurveyChild(owner, name, sha, heavy, light, task)
		if kept != nil {
			heavy = kept.heavy
			light = kept.light
			log.Printf("ref_theorize: dir survey pruned → %d heavy, %d light (from %d total)",
				len(heavy), len(kept.light), totalFiles)
		}
	}

	// step 2b: fork a lightweight survey child to refine file-level classification
	// child sees file listing + first 10 lines of each, outputs heavy=/light= lines
	surveyResult := fileSurveyChild(owner, name, sha, heavy, light, task)
	if surveyResult != nil {
		heavy = surveyResult.heavy
		light = surveyResult.light
		log.Printf("ref_theorize: file survey refined → %d heavy, %d light",
			len(heavy), len(surveyResult.light))
	}

	// step 3: heavy files get full LAYER 1a+1b+2
	var result string
	if len(heavy) > SPLIT_CHUNK {
		log.Printf("ref_theorize: %s/%s %s: %d heavy files -> split (skipped %d light)",
			owner, name, sha[:12], len(heavy), len(light))
		result = ref_split(parent, owner, name, sha, heavy, task, 0)
	} else if len(heavy) > 0 {
		result = ref_theorize_leaf(parent, owner, name, sha, heavy, task)
	}

	// step 4: light files → kernel appends listing, no child needed
	if len(light) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\n[LIGHT FILES — structure only, no LAYER 1a/1b]\n")
		for _, lf := range light {
			sb.WriteString(fmt.Sprintf("  %s (%d lines)\n", lf.path, lf.lines))
		}
		result = result + sb.String()
	}

	if len(strings.TrimSpace(result)) > 10 {
		theorem_store(owner+"/"+name, sha, result, 0)
	}
	return result
}

type surveyOutput struct {
	heavy []string
	light []lightFile
}

// dirSurveyChild: when a commit has >200 files, fork a child to prune at directory level.
// child sees: directory tree with file counts and total lines per dir.
// child outputs: keep=dir1,dir2,dir3 (dirs whose files need theorizing)
// everything NOT in a keep dir becomes light.
func dirSurveyChild(owner, name, sha string, heavy []string, light []lightFile, task string) *surveyOutput {
	// build dir → file count + total lines
	type dirStat struct {
		files int
		lines int
	}
	dirs := make(map[string]*dirStat)

	allFiles := make(map[string]int) // path → lines
	for _, f := range heavy {
		content, err := RefFileAt(owner, name, sha, f)
		lines := 0
		if err == nil {
			lines = strings.Count(content, "\n") + 1
		}
		allFiles[f] = lines

		dir := f
		if idx := strings.LastIndex(f, "/"); idx >= 0 {
			dir = f[:idx]
		} else {
			dir = "."
		}
		if dirs[dir] == nil {
			dirs[dir] = &dirStat{}
		}
		dirs[dir].files++
		dirs[dir].lines += lines
	}
	for _, lf := range light {
		allFiles[lf.path] = lf.lines
		dir := lf.path
		if idx := strings.LastIndex(lf.path, "/"); idx >= 0 {
			dir = lf.path[:idx]
		} else {
			dir = "."
		}
		if dirs[dir] == nil {
			dirs[dir] = &dirStat{}
		}
		dirs[dir].files++
		dirs[dir].lines += lf.lines
	}

	// build a VERY short prompt — child must output exactly "keep=..." and nothing else
	var prompt strings.Builder
	prompt.WriteString(fmt.Sprintf("%d directories in %s/%s. which have core algorithms?\n\n",
		len(dirs), owner, name))

	var sortedDirs []string
	for d := range dirs {
		sortedDirs = append(sortedDirs, d)
	}
	sort.Strings(sortedDirs)

	for _, d := range sortedDirs {
		s := dirs[d]
		prompt.WriteString(fmt.Sprintf("%s  %d files %d lines\n", d, s.files, s.lines))
	}

	prompt.WriteString("\nreply ONLY: keep=dir1,dir2,dir3\nskip tests, benchmarks, examples, docs, CI, vendored, build scripts.\n")

	child, err := Fork(prompt.String(), ".")
	if err != nil {
		log.Printf("dirSurveyChild: fork failed: %v", err)
		return nil
	}
	Wait(child)

	stdout := GetResult(child)
	log.Printf("dirSurveyChild: child returned %d bytes", len(stdout))

	// parse keep= line
	var keepDirs []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "keep=") {
			for _, d := range strings.Split(line[5:], ",") {
				d = strings.TrimSpace(d)
				if d != "" {
					keepDirs = append(keepDirs, d)
				}
			}
		}
	}

	if len(keepDirs) == 0 {
		log.Printf("dirSurveyChild: no keep dirs parsed, skipping prune")
		return nil
	}

	log.Printf("dirSurveyChild: keeping %d dirs: %v", len(keepDirs), keepDirs)

	// filter: files in keep dirs stay heavy, rest become light
	isKept := func(path string) bool {
		for _, d := range keepDirs {
			if strings.HasPrefix(path, d+"/") || path == d || d == "." {
				return true
			}
		}
		return false
	}

	var newHeavy []string
	var newLight []lightFile
	for _, f := range heavy {
		if isKept(f) {
			newHeavy = append(newHeavy, f)
		} else {
			newLight = append(newLight, lightFile{f, allFiles[f]})
		}
	}
	for _, lf := range light {
		newLight = append(newLight, lf) // already light, stays light
	}

	return &surveyOutput{heavy: newHeavy, light: newLight}
}

// fileSurveyChild: fork a child that sees the file listing + first 10 lines
// of each file, and outputs which files need full mathematical analysis.
// child outputs kv format: heavy=path1,path2,... and light=path3,path4,...
// kernel parses the output. child conversation is tiny — one round, no tools.
func fileSurveyChild(owner, name, sha string, initHeavy []string, initLight []lightFile, task string) *surveyOutput {
	// build a compact listing: path | lines | first 10 lines
	var prompt strings.Builder
	prompt.WriteString(fmt.Sprintf("[TASK] %s\n\n", task))
	prompt.WriteString(fmt.Sprintf("[FILE SURVEY] %s/%s commit %s\n", owner, name, sha[:12]))
	prompt.WriteString("classify each file: does it need mathematical specification (LAYER 1a/1b)?\n")
	prompt.WriteString("files with algorithms, state machines, concurrency, retry logic, invariants → heavy\n")
	prompt.WriteString("simple wrappers, RAII, getters/setters, configs, constants, tests → light\n\n")

	allFiles := make(map[string]int) // path → lines
	for _, f := range initHeavy {
		content, err := RefFileAt(owner, name, sha, f)
		if err != nil {
			continue
		}
		lines := strings.Count(content, "\n") + 1
		allFiles[f] = lines

		prompt.WriteString(fmt.Sprintf("--- %s (%d lines) ---\n%s\n\n", f, lines, codePreview(content, 10)))
	}
	for _, lf := range initLight {
		allFiles[lf.path] = lf.lines
		content, err := RefFileAt(owner, name, sha, lf.path)
		if err != nil {
			continue
		}
		prompt.WriteString(fmt.Sprintf("--- %s (%d lines) ---\n%s\n\n", lf.path, lf.lines, codePreview(content, 10)))
	}

	prompt.WriteString("output ONLY two lines, nothing else:\n")
	prompt.WriteString("heavy=comma,separated,paths\n")
	prompt.WriteString("light=comma,separated,paths\n")

	child, err := Fork(prompt.String(), ".")
	if err != nil {
		log.Printf("fileSurveyChild: fork failed: %v", err)
		return nil
	}
	Wait(child)

	stdout := GetResult(child)
	log.Printf("fileSurveyChild: %s/%s child returned %d bytes", owner, name, len(stdout))

	// parse heavy= and light= lines
	var heavyPaths, lightPaths []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "heavy=") {
			for _, p := range strings.Split(line[6:], ",") {
				p = strings.TrimSpace(p)
				if p != "" && allFiles[p] > 0 {
					heavyPaths = append(heavyPaths, p)
				}
			}
		} else if strings.HasPrefix(line, "light=") {
			for _, p := range strings.Split(line[6:], ",") {
				p = strings.TrimSpace(p)
				if p != "" && allFiles[p] > 0 {
					lightPaths = append(lightPaths, p)
				}
			}
		}
	}

	if len(heavyPaths) == 0 && len(lightPaths) == 0 {
		log.Printf("fileSurveyChild: no parseable output, keeping kernel classification")
		return nil
	}

	// build output, defaulting unmentioned files to kernel's classification
	mentioned := make(map[string]bool)
	for _, p := range heavyPaths {
		mentioned[p] = true
	}
	for _, p := range lightPaths {
		mentioned[p] = true
	}

	// unmentioned heavy files stay heavy
	for _, f := range initHeavy {
		if !mentioned[f] {
			heavyPaths = append(heavyPaths, f)
		}
	}
	// unmentioned light files stay light
	var lightOut []lightFile
	for _, p := range lightPaths {
		lightOut = append(lightOut, lightFile{p, allFiles[p]})
	}
	for _, lf := range initLight {
		if !mentioned[lf.path] {
			lightOut = append(lightOut, lf)
		}
	}

	return &surveyOutput{heavy: heavyPaths, light: lightOut}
}

type lightFile struct {
	path  string
	lines int
}

// fileSurvey: kernel reads file sizes and extensions to decide what needs
// full mathematical analysis vs what's just structural boilerplate.
// no hardcoded rules per extension — uses line count + content signals.
func fileSurvey(owner, name, sha string, files []string) (heavy []string, light []lightFile) {
	// pass 1: collect stats
	type fstat struct {
		path  string
		lines int
		ext   string
	}
	var stats []fstat
	extCount := make(map[string]int)
	extLines := make(map[string]int)

	for _, f := range files {
		if isBinaryFile(f) || isDocOnlyFile(f) {
			continue
		}
		content, err := RefFileAt(owner, name, sha, f)
		if err != nil {
			continue
		}
		lines := strings.Count(content, "\n") + 1
		ext := ""
		if idx := strings.LastIndex(f, "."); idx >= 0 {
			ext = f[idx:]
		}
		stats = append(stats, fstat{f, lines, ext})
		extCount[ext]++
		extLines[ext] += lines
	}

	// pass 2: classify
	for _, s := range stats {
		if s.lines < 30 {
			light = append(light, lightFile{s.path, s.lines})
			continue
		}
		// short files of types that have many instances → likely boilerplate
		if s.lines < 80 && extCount[s.ext] > 5 {
			light = append(light, lightFile{s.path, s.lines})
			continue
		}
		heavy = append(heavy, s.path)
	}

	log.Printf("fileSurvey: %s/%s %d total → %d heavy, %d light",
		owner, name, len(stats), len(heavy), len(light))
	for ext, cnt := range extCount {
		log.Printf("fileSurvey:   %s: %d files, %d lines", ext, cnt, extLines[ext])
	}
	return
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

	var extra string
	if read < 50 {
		extra = fmt.Sprintf(
			"\nNOTE: only %d lines of source were included above (binary/doc files skipped). "+
				"use git at %s to read the actual source files for this commit: "+
				"git ls-tree -r --name-only %s | head -50, then git show %s:<path>.\n",
			read, dir, sha[:12], sha[:12])
	}

	prompt := src.String() + extra + theorize_instruction +
		"you may use git to read other files for CONTEXT (understanding types, interfaces, dependencies).\n" +
		"the bare repo is at " + dir + " — use `git --git-dir=" + dir + " show " + sha[:12] + ":<path>`.\n" +
		"do NOT clone the repo again.\n" +
		"produce LAYER 1/2 ONLY for the files listed above.\n\n" +
		"write your LAYER 1 and LAYER 2 however you prefer — text, Artifact, or create_file all work. " +
		"the kernel will download the full conversation after you finish."

	child, err := Fork(prompt, ".")
	if err != nil {
		return fmt.Sprintf("ref_theorize fork: %v", err)
	}
	log.Printf("ref_theorize: parent=%d child=%d %s/%s %s (%d files)",
		parent, child, owner, name, sha[:12], len(files))
	Wait(child)

	theory := GetResult(child)
	// DownloadConv fallback if GetResult is short
	if len(theory) < 500 {
		if m := getMeta(child); m != nil && m.ConvID != "" {
			downloaded, err := DownloadConv(m.ConvID)
			if err == nil && len(downloaded) > len(theory) {
				log.Printf("ref_theorize: GetResult=%d, downloaded=%d, using download",
					len(theory), len(downloaded))
				theory = downloaded
			}
		}
	}

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

// codePreview: extract first N lines of actual code, skipping license headers,
// comment blocks, and blank lines. shows what the file actually does.
func codePreview(content string, n int) string {
	lines := strings.Split(content, "\n")
	var out []string
	inBlockComment := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// track block comments
		if !inBlockComment && (strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "/**")) {
			inBlockComment = true
		}
		if inBlockComment {
			if strings.Contains(trimmed, "*/") {
				inBlockComment = false
			}
			continue
		}

		// skip line comments and blanks
		if trimmed == "" || strings.HasPrefix(trimmed, "//") ||
			strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "--") {
			continue
		}

		out = append(out, line)
		if len(out) >= n {
			break
		}
	}
	if len(out) == 0 {
		// all comments/blanks — show raw first few lines
		if len(lines) > n {
			return strings.Join(lines[:n], "\n") + "\n..."
		}
		return content
	}
	result := strings.Join(out, "\n")
	if len(out) >= n {
		result += "\n..."
	}
	return result
}

func isDocOnlyFile(path string) bool {
	lower := strings.ToLower(path)
	base := lower
	if idx := strings.LastIndex(lower, "/"); idx >= 0 {
		base = lower[idx+1:]
	}
	for _, name := range []string{
		"readme.md", "readme.rst", "readme.txt", "readme",
		"license", "license.md", "license.txt",
		"changelog.md", "changelog.txt", "changes.md",
		"contributing.md", "code_of_conduct.md",
		".gitignore", ".gitattributes", ".editorconfig",
	} {
		if base == name {
			return true
		}
	}
	return false
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
