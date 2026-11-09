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

// thi_output takes TheoryOutputsDir.
const thi_output = `output one .lean file per module via create_file.
file: %[1]s/<CamelCaseName>.lean — name by what the code DOES, not the source path.
`

// thi_structure: the SOURCES line is what the kernel's coverage check reads. Matching by
// name alone missed short ones: 11/04 03:13:42 chunk 5 was continued for src/kernels/cuda/
// ple.cu and qsa.cu, already covered by PleBlockGpu.lean and QsaCacheIndexerAttention.lean.
const thi_structure = `each .lean file:

/-!
# MODULE: <snake_case_name>
SOURCES: <every reference file this module covers, by its full repo path, comma-separated>
<what this module does — 1-3 sentences>

## Source semantics
<core contracts and invariants, in natural language>

## Form template identifiers
<role mapping for downstream code generation — see source names → abstract names>
-/

then sections (-- § name) with theorems. every theorem gets a /-- docstring -/.
`

// thi_proofs takes proofRefURL, proofRefDir and ProofReferencePath().
const thi_proofs = `PROOF STYLE — BEFORE writing any .lean file, run this:

  git clone %[1]s %[2]s

then read 3-5 files under %[3]s to see how real
Lean 4 proofs are structured. notice: they decompose goals with 'have'
and 'obtain', main theorems cite helper lemmas, tactics only close
small leaf goals. model your proofs after what you see there.

do NOT use native_decide (Axle bans it). do NOT change a theorem statement to make it easier to prove.
a theorem that uses sorry is not proved yet: the proofs under %[3]s show how. every Axle answer
comes back with the sorry count measured on the machine that runs you.
`

// thi_axle takes the Axle session cookie and CSRF token.
//
// Axle cannot be reached from the sandbox: every call is answered by the machine
// that runs the conversation, and each batch of calls is one round trip (one
// completion in the 9-per-10-minutes window). "verify after each .lean file" made
// a round trip per file; the batch protocol makes it a round trip per attempt.
const thi_axle = `VERIFICATION — through Axle. every Axle call is answered by the machine that runs you,
so each round of calls costs one round trip. work in batches:
  1. write ALL your .lean files first — do not verify between files
  2. verify them ALL with ONE command (a loop over the files)
  3. fix every failure in ONE script, then verify them all again

curl -s https://axle.axiommath.ai/api/v1/verify_proof \
  -H 'content-type: application/json' \
  -b 'provertools_session=%[1]s' \
  -H 'x-browser-api-csrf: %[2]s' \
  -H 'x-request-source: web' \
  -H 'origin: https://axle.axiommath.ai' \
  --data-raw '{"formal_statement":"<with sorry>","content":"<with proof>","ignore_imports":true,"environment":"lean-4.35.0","timeout_seconds":120}'

if okay:false, fix the proof and re-verify. if timeout/network error, move on.
`

// thi_scope takes TheoryOutputsDir and DoneInstruction().
const thi_scope = `produce theory ONLY for your assigned files. do not write .md, .cpp, .py.
do not push to git. before you finish, run ls %[1]s once so the list of your files is in the conversation, then write a brief summary.
%[2]s`

// formatTheorizeInstruction assembles the theory prompt from pieces.
func formatTheorizeInstruction() string {
	session, csrf := axleCredentials()
	return fmt.Sprintf(thi_output, TheoryOutputsDir) + thi_structure +
		fmt.Sprintf(thi_proofs, proofRefURL, proofRefDir, ProofReferencePath()) +
		fmt.Sprintf(thi_axle, session, csrf) +
		fmt.Sprintf(thi_scope, TheoryOutputsDir, DoneInstruction())
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

// refDiscoverPromptFmt: the search for repositories beyond the ones a task names (%[1]s the
// task, %[2]d how many). ref_discover only runs it when the task names none; the integration
// unit's evaluation (eval_discover.go) runs it every time.
const refDiscoverPromptFmt = "[USER TASK]\n%[1]s\n\n" +
	"based on the task above, list %[2]d relevant open-source " +
	"reference repositories on github.\n" +
	"reply with ONLY owner/name lines, one per line, nothing else.\n"

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

	prompt := fmt.Sprintf(refDiscoverPromptFmt, truncDiff(task, 3000), need)

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

	prompt.WriteString("output ONLY two lines, nothing else:\n")
	prompt.WriteString("heavy=comma,separated,paths\n")
	prompt.WriteString("light=comma,separated,paths\n")
	prompt.WriteString(DoneInstruction())

	var heavyPaths, lightPaths []string
	for attempt := 1; attempt <= 5; attempt++ {
		child, err := Fork(prompt.String(), ".")
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
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, name)

	var src strings.Builder
	if task != "" {
		src.WriteString(fmt.Sprintf("[TASK] %s\n\n", task))
	}
	src.WriteString(fmt.Sprintf("[REFERENCE] %s/%s commit %s\n%d files\n\n",
		owner, name, sha[:12], len(files)))

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

	var extra string
	if read < 50 {
		extra = fmt.Sprintf(
			"\nNOTE: only %d lines of source were included above (binary/doc files skipped). "+
				"use git at %s to read the actual source files for this commit: "+
				"git ls-tree -r --name-only %s | head -50, then git show %s:<path>.\n",
			read, dir, sha[:12], sha[:12])
	}

	prompt := src.String() + extra + formatTheorizeInstruction() +
		fmt.Sprintf("you may use git to read other files for CONTEXT.\n"+
			"the bare repo is at %s — use `git --git-dir=%s show %s:<path>`.\n"+
			"do NOT clone the repo again.\n"+
			"produce .lean theory ONLY for the files listed above.\n",
			dir, dir, sha[:12])
	prompt += "\n" + BudgetBlock(tally.Theory(), RoundBudget("theorize_full"))

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
