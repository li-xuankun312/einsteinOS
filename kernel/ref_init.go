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

// theorize_instruction pieces — assembled by formatTheorizeInstruction.
// kept as separate consts so each is readable and grep-able.

// TheoryOutputsDir is where theory children write their .lean files (the sandbox
// prompts and the local mirror both use it).
const TheoryOutputsDir = "/mnt/user-data/outputs"

// theorizeFacts: the kernel's contracts with a theory conversation, as facts —
// %[1]s TheoryOutputsDir, %[2]s/%[3]s the proof reference's URL and clone dir,
// %[4]s the proofs under it, %[5]s the Axle facts (axleFacts), %[6]s the done line.
// The SOURCES line is what the kernel's coverage check reads (11/04 03:13:42 chunk 5 was
// continued for files already covered when it matched by name alone).
const theorizeFacts = `[OUTPUT] %[1]s/<CamelCaseName>.lean, one file per module, named by what the code does; the kernel finds them by the paths in this conversation (create_file, and one ls %[1]s).
[MODULE HEADER] the kernel reads, at the top of each file:
/-!
# MODULE: <snake_case_name>
SOURCES: <every reference file this module covers, by its full repo path, comma-separated>
<what this module does — 1-3 sentences>

## Source semantics
<core contracts and invariants, in natural language>

## Form template identifiers
<role mapping for downstream code generation — see source names → abstract names>
-/
then sections (-- § name) with theorems, each with a /-- docstring -/.
[PROOF REFERENCE] git clone %[2]s %[3]s — Lean 4 proofs: the files under %[4]s
[MEASURED BY THE KERNEL] a theorem that uses sorry is not proved yet; the sorry count is measured on the machine that runs you, and every theorem statement changed after it was verified is recorded with how it changed.
%[5]s%[6]s`

// axleFactsFmt: how Axle is reached from a conversation, as facts — %[1]s/%[2]s its session
// and CSRF. A theory conversation and a proof repair are given the same.
const axleFactsFmt = `[AXLE] answered by the machine that runs you: each round of Axle calls costs one round trip of this conversation. Axle refuses native_decide. okay:false is a failed proof.
curl -s https://axle.axiommath.ai/api/v1/verify_proof \
  -H 'content-type: application/json' \
  -b 'provertools_session=%[1]s' \
  -H 'x-browser-api-csrf: %[2]s' \
  -H 'x-request-source: web' \
  -H 'origin: https://axle.axiommath.ai' \
  --data-raw '{"formal_statement":"<with sorry>","content":"<with proof>","ignore_imports":true,"environment":"lean-4.35.0","timeout_seconds":120}'
`

// axleFacts: the Axle facts with the credentials inserted.
func axleFacts() string {
	session, csrf := axleCredentials()
	return fmt.Sprintf(axleFactsFmt, session, csrf)
}

// formatTheorizeInstruction: the theorize facts with their values inserted.
func formatTheorizeInstruction() string {
	return fmt.Sprintf(theorizeFacts, TheoryOutputsDir, proofRefURL, proofRefDir, ProofReferencePath(), axleFacts(), DoneInstruction())
}

// theorizePrompt: a theory conversation's prompt — the shell with the reference commit, what
// this conversation covers (%what), and the content inserted: the task, the kernel's facts,
// where the bare repo is, the source files, the budget.
func theorizePrompt(owner, repo, sha, what, task, sources string, files int, budget string) string {
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, repo)
	var c strings.Builder
	if task != "" {
		c.WriteString(fmt.Sprintf("[TASK] %s\n\n", task))
	}
	c.WriteString(formatTheorizeInstruction())
	c.WriteString(fmt.Sprintf(theorizeBareRepoFmt, dir, short_sha(sha)))
	c.WriteString(sources)
	c.WriteString("\n" + budget)
	to := theoryTo()
	return Bagu(fmt.Sprintf(theorizeWhereFmt, owner, repo, short_sha(sha)), what, c.String(), fmt.Sprint(files), to)
}

// %[1]d files, %[2]d lines of source included
const theorizeFullWhatFmt = "这次是 %[1]d 个文件，下面包括其中 %[2]d 行源码"

// %[1]s/%[2]s the reference, %[3]s its commit
const theorizeWhereFmt = "参考仓库 %[1]s/%[2]s 在 commit %[3]s 的源文件"

// %[1]s the bare clone, %[2]s the commit
const theorizeBareRepoFmt = "[BARE REPO] %[1]s: git --git-dir=%[1]s show %[2]s:<path>\n[SOURCE CODE]\n"

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

// refDiscoverPrompt: the search for repositories beyond the ones a task names — the shell
// with the task, how many the kernel takes (n) and what it keeps of the answer (keep).
// ref_discover only runs it when the task names none; the integration unit's evaluation
// (eval_discover.go) runs it every time.
func refDiscoverPrompt(task string, n int, keep string) string {
	content := fmt.Sprintf(refDiscoverContentFmt, task, n, keep)
	return Plain(refDiscoverWhere, fmt.Sprintf(refDiscoverWhatFmt, n), content)
}

// refNotFoundPrompt: the names the kernel could not clone, asked again through the shell
func refNotFoundPrompt(notfound []string, n int, keep string) string {
	content := fmt.Sprintf(refNotFoundContentFmt, strings.Join(notfound, ", "), n, keep)
	return Plain(refDiscoverWhere, fmt.Sprintf(refNotFoundWhatFmt, len(notfound)), content)
}

// inserted for the repository search: where, %[1]d how many, the task, what the kernel keeps
const refDiscoverWhere = "GitHub 上的开源仓库"
const refDiscoverWhatFmt = "内核要找 %[1]d 个和任务相关的仓库"
const refNotFoundWhatFmt = "上一次给的 %[1]d 个名字在 GitHub 上不存在"
const refDiscoverContentFmt = "[TASK]\n%[1]s\n\n[ANSWER] the kernel reads owner/name lines, up to %[2]d, and clones each: %[3]s\n"
const refNotFoundContentFmt = "[NOT ON GITHUB] %[1]s\n[ANSWER] the kernel reads owner/name lines, up to %[2]d, and clones each: %[3]s\n"

// what each search keeps of the answer
const refDiscoverKeepFact = "the ones that exist become the references, read and theorized, never built."

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

// inserted for telling the dev repository from the references
const detectDevWhere = "任务里提到的仓库"
const detectDevWhatFmt = "任务里提到了 %[1]d 个仓库"

// %[1]s the repositories, %[2]s the task; what the kernel does with the two lines
const detectDevContentFmt = "[REPOS]\n%[1]s\n[TASK]\n%[2]s\n\n[ANSWER] the kernel reads two lines, dev=owner/name and branch=<branch>: the code is built in and pushed to dev at branch (main when the line is missing); every other repository named is a reference, read and theorized, never built.\n"

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

	prompt := Plain(detectDevWhere, fmt.Sprintf(detectDevWhatFmt, len(repos)), fmt.Sprintf(detectDevContentFmt, repoList.String(), truncDiff(task, 1000)))

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

	prompt := refDiscoverPrompt(truncDiff(task, 3000), need, refDiscoverKeepFact)

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
		prompt = refNotFoundPrompt(notfound, MAX_REFS-len(good), refDiscoverKeepFact)
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
					// a GitHub owner and repository name are only [A-Za-z0-9._-]: 11/10 01:24
					// "github.com/zeng-bin3123/baloon我们要开发的是" was taken as the repository
					// "baloon我们要开发的是" and cloned
					owner := githubNamePrefix(parts[0])
					name := strings.TrimRight(githubNamePrefix(parts[1]), ".,;:!?\u3002\uff0c\u3001\uff1b\uff1a\uff01\uff1f")
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

		// match bare Owner/Repo (e.g. "参考Niko1221/Strata")
		for _, word := range strings.Fields(line) {
			word = strings.TrimRight(word, ".,;:!?\u3002\uff0c\u3001\uff1b\uff1a\uff01\uff1f")
			word = strings.TrimSuffix(word, ".git")
			parts := strings.SplitN(word, "/", 3)
			if len(parts) == 2 && len(parts[0]) > 1 && len(parts[1]) > 1 {
				owner, name := parts[0], parts[1]
				if !strings.Contains(owner, ".") && !strings.Contains(owner, ":") &&
					!skip[name] && owner[0] >= 'A' && owner[0] <= 'z' {
					key := owner + "/" + name
					if !seen[key] {
						seen[key] = true
						repos = append(repos, RefRepo{Owner: owner, Name: name})
					}
				}
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

	if stored, _, ok := storedTheoryFn(owner+"/"+name, sha); ok {
		theorem_store(owner+"/"+name, sha, stored, 0)
		return stored
	}

	// step 2: ONE conversation classifies every file (heavy = needs LAYER 1a/1b)
	// and, when there are enough files to need it, groups the heavy ones into
	// chunks (ref_survey_plan.go). The directory-level pre-survey is gone: it
	// only existed to shrink the input of the file survey, and cost a call.
	heavy, light, planned := surveyAndPlan(parent, owner, name, sha, heavy, light, task)

	// step 3: heavy files get full LAYER 1a+1b+2
	var result string
	if len(heavy) > SPLIT_CHUNK {
		log.Printf("ref_theorize: %s/%s %s: %d heavy files -> split (skipped %d light)",
			owner, name, sha[:12], len(heavy), len(light))
		result = ref_split_with(parent, owner, name, sha, heavy, task, 0, planned)
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

// refFileAtFn reads a file of the reference at a commit; a variable so the survey
// can be tested without the network.
var refFileAtFn = RefFileAt

type surveyOutput struct {
	heavy []string
	light []lightFile
}

// the two lines the kernel reads from a file survey
const surveyTwoLinesFact = "[ANSWER] the kernel reads two lines, with exact paths from the list:\nheavy=comma,separated,paths\nlight=comma,separated,paths\n"

// keysOf: the paths of a path → lines map
func keysOf(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
	prompt.WriteString(surveyHeavyLightFact + "\n")

	allFiles := make(map[string]int) // path → lines
	for _, f := range initHeavy {
		content, err := refFileAtFn(owner, name, sha, f)
		if err != nil {
			continue
		}
		lines := strings.Count(content, "\n") + 1
		allFiles[f] = lines

		prompt.WriteString(fmt.Sprintf("--- %s (%d lines) ---\n%s\n\n", f, lines, codePreview(content, 10)))
	}
	for _, lf := range initLight {
		allFiles[lf.path] = lf.lines
		content, err := refFileAtFn(owner, name, sha, lf.path)
		if err != nil {
			continue
		}
		prompt.WriteString(fmt.Sprintf("--- %s (%d lines) ---\n%s\n\n", lf.path, lf.lines, codePreview(content, 10)))
	}

	prompt.WriteString(surveyTwoLinesFact)
	prompt.WriteString(DoneInstruction())
	surveyPrompt := surveyShell(owner, name, sha, len(allFiles), len(dirStatsOf(keysOf(allFiles), allFiles)), prompt.String())

	var heavyPaths, lightPaths []string
	for attempt := 1; attempt <= 5; attempt++ {
		child, err := Fork(surveyPrompt, ".")
		if err != nil {
			log.Printf("fileSurveyChild: fork failed (attempt %d/5): %v", attempt, err)
			continue
		}
		Wait(child)

		stdout := GetResult(child)
		log.Printf("fileSurveyChild: %s/%s child returned %d bytes (attempt %d/5)", owner, name, len(stdout), attempt)

		// parse heavy= and light= lines
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

		if len(heavyPaths) > 0 || len(lightPaths) > 0 {
			break
		}
		log.Printf("fileSurveyChild: no parseable output (attempt %d/5), retrying", attempt)
	}

	if len(heavyPaths) == 0 && len(lightPaths) == 0 {
		log.Printf("fileSurveyChild: all attempts failed, keeping kernel classification")
		return nil
	}

	return applySurvey(heavyPaths, lightPaths, initHeavy, initLight, allFiles)
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
	var unread []string
	extCount := make(map[string]int)
	extLines := make(map[string]int)
	refMu.Lock()
	if r := refRepos[owner+"/"+name]; r != nil {
		refMu.Unlock()
		prefetchCommitBlobs(r.CloneDir, sha)
	} else {
		refMu.Unlock()
	}

	for _, f := range files {
		if isBinaryFile(f) || isDocOnlyFile(f) {
			continue
		}
		content, err := RefFileAt(owner, name, sha, f)
		if err != nil {
			unread = append(unread, f)
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
	// 11/10 14:17 the survey counted 189 of 212 files and 11/11 05:58 207 of 212, and said nothing
	// of the files it skipped
	if len(unread) > 0 {
		log.Printf("fileSurvey: %s/%s %d file(s) could not be read and are not counted: %s", owner, name, len(unread), shortList(unread, 10))
	}
	for ext, cnt := range extCount {
		log.Printf("fileSurvey:   %s: %d files, %d lines", ext, cnt, extLines[ext])
	}
	return
}

// ref_theorize_task is the task the theory conversations are given: the first non-empty of
// the process's context, its prompt, and its PR's title — always without credentials (the
// context was returned as it stood, and an empty prompt never reached the PR title, so the
// 11/03 theory prompts said "[TASK]" and nothing else).
func ref_theorize_task(pid int64) string {
	var cands []string
	if ctx := GetCOWContext(pid); ctx != nil && ctx.Len() > 0 {
		cands = append(cands, ctx.Read(0))
	}
	metaMu.Lock()
	if m := taskMeta[pid]; m != nil {
		cands = append(cands, m.Prompt)
	}
	metaMu.Unlock()
	if pr := GetPR(pid); pr != nil {
		cands = append(cands, pr.Title)
	}
	for _, c := range cands {
		if t := strings.TrimSpace(strip_credentials(c)); t != "" {
			return t
		}
	}
	return ""
}

func ref_theorize_leaf(parent int64, owner, name, sha string, files []string, task string) string {
	var src strings.Builder
	read := 0
	skipped := 0
	var tally sourceTally
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
		tally.Add(f, lines)
	}

	log.Printf("ref_theorize: %s/%s %s: %d files (%d binary skipped), %d lines fed",
		owner, name, sha[:12], len(files), skipped, read)

	prompt := theorizePrompt(owner, name, sha, fmt.Sprintf(theorizeFullWhatFmt, len(files), read), task, src.String(), len(files),
		BudgetBlock(tally.Theory(), RoundBudget("theorize_full")))

	child, err := ForkWithRole(prompt, ".", "theorize_full")
	if err != nil {
		return fmt.Sprintf("ref_theorize fork: %v", err)
	}
	log.Printf("ref_theorize: parent=%d child=%d %s/%s %s (%d files)",
		parent, child, owner, name, sha[:12], len(files))
	Wait(child)

	theory := GetResult(child)
	convID := ""
	if m := getMeta(child); m != nil {
		convID = m.ConvID
	}
	// the theory is the files the conversation left in its sandbox, in their final state
	// (chunk_files.go) — the streamed text is a summary and the transcript is everything it ran
	if text, ok := theoryFromFiles(convID); ok {
		log.Printf("ref_theorize: theory = the conversation's files from the sandbox, %d bytes (streamed text was %d)", len(text), len(theory))
		theory = text
		forgetChunkFiles(convID)
	} else if len(theory) < 500 && convID != "" {
		// DownloadConv fallback if GetResult is short
		downloaded, err := DownloadConv(convID)
		if err == nil && len(downloaded) > len(theory) {
			log.Printf("ref_theorize: GetResult=%d, downloaded=%d, using download",
				len(theory), len(downloaded))
			theory = downloaded
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
	// return the LAST match: leaf chunks store first (GetResult, no ## FILE headers),
	// then ref_bind stores the merged result (with DownloadConv content including headers).
	// last match = merged = most complete theory.
	best := ""
	for i := 0; i < nr_theorems; i++ {
		t := theorem_table[i]
		if t.repo == repo && (sha == "" || strings.HasPrefix(t.sha, sha)) {
			best = t.theory
		}
	}
	return best
}

// theorem_replace_last swaps the text of the entry theorem_get(repo,"") would
// return, in place. Used when the kernel patches a spec proof after Axle has
// verified the replacement; appending a new entry instead would skew
// theorem_count() and the ref_bind bookkeeping that reads it.
func theorem_replace_last(repo, theory string) bool {
	theorem_mu.Lock()
	defer theorem_mu.Unlock()
	last := -1
	for i := 0; i < nr_theorems; i++ {
		if theorem_table[i].repo == repo {
			last = i
		}
	}
	if last < 0 {
		return false
	}
	theorem_table[last].theory = theory
	return true
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

// githubNamePrefix: the leading part of s that a GitHub owner or repository name can hold.
func githubNamePrefix(s string) string {
	for i, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return s[:i]
		}
	}
	return s
}
