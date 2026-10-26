package kernel

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type StepKind int

const (
	STEP_REF_CLONE StepKind = iota + 1
	STEP_REF_LIST
	STEP_REF_READ
	STEP_REF_FILE
	STEP_REF_TREE
	STEP_REF_FIRST
	STEP_DEV_EXEC
	STEP_REF_THEORIZE
	STEP_DEV_WITH_THEORY
	STEP_HW_PROBE
	STEP_REF_BIND
	STEP_DEV_FROM_THEOREMS
	STEP_DEV_FIX    // kernel-driven fix: structured args, not "fix the issues above"
	STEP_REF_DIFF
	// STEP_VLLM_UP and STEP_VLLM_BENCH removed: deployment and benchmarking
	// are the main conversation's job, not a pipeline step. the main conv runs
	// on the real machine with shadow+writeback — it probes hardware, builds,
	// and benchmarks itself by reading the project code.
	STEP_OPEN_PR    // kernel: git add+commit+push to temp branch, open PR via GitHub API
)

type Step struct {
	Kind     StepKind
	Owner    string
	Repo     string
	SHA      string
	Path     string
	Command  string
	Dir      string
	Result   string
	Files    []string
	Err      error
	ChildPid int64
	Started  time.Time
	Finished time.Time
}

var (
	prSteps   = make(map[int64][]*Step)
	prCursor  = make(map[int64]int)
	prStepsMu sync.Mutex
)

func PRPushStep(pid int64, s *Step) {
	prStepsMu.Lock()
	defer prStepsMu.Unlock()
	prSteps[pid] = append(prSteps[pid], s)
}

func PRCurrentStep(pid int64) *Step {
	prStepsMu.Lock()
	defer prStepsMu.Unlock()
	steps := prSteps[pid]
	cursor := prCursor[pid]
	if cursor >= len(steps) {
		return nil
	}
	return steps[cursor]
}

func PRAdvance(pid int64) {
	prStepsMu.Lock()
	defer prStepsMu.Unlock()
	prCursor[pid]++
}

func PRStepCount(pid int64) (total, done int) {
	prStepsMu.Lock()
	defer prStepsMu.Unlock()
	return len(prSteps[pid]), prCursor[pid]
}

func PRRunNext(pid int64) (string, error) {
	step := PRCurrentStep(pid)
	if step == nil {
		return "", fmt.Errorf("pid %d: no more steps", pid)
	}

	PRBlock(pid)
	step.Started = time.Now()

	var result string
	var err error

	switch step.Kind {
	case STEP_HW_PROBE:
		result, err = hw_init(pid)

	case STEP_REF_CLONE:
		dir := fmt.Sprintf("/tmp/ref_%s_%s.git", step.Owner, step.Repo)
		_, err = RefClone(step.Owner, step.Repo, dir)
		if err == nil {
			result = fmt.Sprintf("cloned %s/%s", step.Owner, step.Repo)
		}

	case STEP_REF_LIST:
		commits, e := RefListCommits(step.Owner, step.Repo)
		err = e
		if err == nil {
			result = fmt.Sprintf("%d commits", len(commits))
		}

	case STEP_REF_FIRST:
		commit, diff, e := RefFirstCommit(step.Owner, step.Repo)
		err = e
		if err == nil {
			step.Files = commit.Files
			result = ref_theorize(pid,
				step.Owner, step.Repo,
				commit.SHA, diff,
				len(commit.Files), commit.Message)

			pr := GetPR(pid)
			if pr != nil && result != "" {
				PRPushStep(pid, &Step{
					Kind:    STEP_DEV_WITH_THEORY,
					Owner:   step.Owner,
					Repo:    step.Repo,
					SHA:     commit.SHA,
					Command: pr.Title,
					Result:  result,
				})
			}
		}

	case STEP_REF_BIND:
		startIdx := 0
		if strings.HasPrefix(step.Command, "start=") {
			fmt.Sscanf(step.Command, "start=%d", &startIdx)
		}
		result, err = ref_bind_at(pid, step.Owner, step.Repo, startIdx)

	case STEP_REF_READ:
		diff, files, e := RefReadCommit(step.Owner, step.Repo, step.SHA)
		err = e
		if err == nil {
			step.Files = files
			result = truncDiff(diff, 4000)
		}

	case STEP_REF_FILE:
		content, e := RefFileAt(step.Owner, step.Repo, step.SHA, step.Path)
		err = e
		if err == nil {
			result = content
		}

	case STEP_REF_TREE:
		files, e := RefTreeAt(step.Owner, step.Repo, step.SHA)
		err = e
		if err == nil {
			step.Files = files
			result = fmt.Sprintf("%d files", len(files))
		}

	case STEP_REF_THEORIZE:
		diff, files, e := RefReadCommit(step.Owner, step.Repo, step.SHA)
		err = e
		if err == nil {
			step.Files = files
			result = ref_theorize(pid,
				step.Owner, step.Repo,
				step.SHA, diff,
				len(files), "")
		}

	case STEP_DEV_WITH_THEORY:
		theory := step.Result
		if theory == "" {
			theory = theorem_get(step.Owner+"/"+step.Repo, step.SHA)
		}
		task := strip_credentials(step.Command)
		repo := step.Owner + "/" + step.Repo
		feedback := pr_feedback(pid)
		devPrompt := fmt.Sprintf(
			"[REFERENCE THEORY from %s commit %s]\n%s\n\n"+
				"[DEVELOPMENT TASK]\n%s",
			repo, short_sha(step.SHA),
			truncDiff(theory, 6000),
			task)
		if feedback != "" {
			devPrompt += fmt.Sprintf("\n\n[DEVELOPMENT DISCIPLINE from prior review]\n%s", feedback)
		}
		childPid, e := ForkWithRole(devPrompt, ".", "dev_legacy")
		err = e
		if err == nil {
			Wait(childPid)
			step.ChildPid = childPid
			result = GetResult(childPid)
		}

	case STEP_DEV_FROM_THEOREMS:
		result, err = dev_from_theorems(pid, step)

	case STEP_DEV_FIX:
		result, err = dev_fix(pid, step)

	case STEP_REF_DIFF:
		result, err = ref_diff(pid, step)

	case STEP_DEV_EXEC:
		childPid, e := ForkExec(step.Command, step.Dir)
		err = e
		if err == nil {
			_, code, _ := Wait(childPid)
			step.ChildPid = childPid
			result = fmt.Sprintf("exit %d: %s", code, GetResult(childPid))
		}

	// STEP_VLLM_UP and STEP_VLLM_BENCH removed — main conv handles deployment

	case STEP_OPEN_PR:
		result, err = open_pr(pid, step)

	}

	step.Finished = time.Now()
	step.Result = result
	step.Err = err

	PRUnblock(pid)
	PRAdvance(pid)
	PRTick(pid, int64(step.Finished.Sub(step.Started).Milliseconds()))

	total, done := PRStepCount(pid)
	nextStep := PRCurrentStep(pid)
	nextKind := "nil"
	if nextStep != nil {
		nextKind = step_name(nextStep.Kind)
	}
	log.Printf("PRRunNext: pid=%d finished step %s, advanced to %d/%d next=%s",
		pid, step_name(step.Kind), done, total, nextKind)

	if err != nil {
		return "", err
	}
	return result, nil
}

func hw_init(pid int64) (string, error) {
	pr := GetPR(pid)
	task := ""
	if pr != nil {
		task = strip_credentials(pr.Title)
	}

	prompt := "probe this machine's hardware. " +
		"run whatever commands you need (lspci, nvidia-smi, ixsmi, " +
		"cpuinfo, free, topology, etc). " +
		"report: GPU vendor/model/count, interconnect topology, " +
		"CPU cores, memory, kernel version.\n\n" +
		"context: " + task

	child, err := ForkWithRole(prompt, ".", "hw_probe")
	if err != nil {
		return "", err
	}
	log.Printf("hw_init: pid=%d child=%d role=hw_probe", pid, child)
	Wait(child)

	hw_info := GetResult(child)
	hw_tools := GetStderr(child)

	gpuAll := parseGPUList(hw_info + "\n" + hw_tools)

	prMu.Lock()
	if pr := prTable[pid]; pr != nil {
		pr.HWInfo = hw_info + "\n" + hw_tools
		pr.GPUAll = gpuAll
		pr.GPUUsable = gpuAll
	}
	prMu.Unlock()

	log.Printf("hw_init: pid=%d stdout=%d stderr=%d gpu_all=%s", pid, len(hw_info), len(hw_tools), gpuAll)
	return hw_info, nil
}

func parseGPUList(hwText string) string {
	count := 0
	for _, line := range strings.Split(hwText, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "bi-v100") || strings.Contains(lower, "iluvatar") ||
			strings.Contains(lower, "gpu 0") || strings.Contains(lower, "gpu 1") ||
			strings.Contains(lower, "/dev/iluvatar") {
			count++
		}
	}
	if count == 0 {
		count = 4
	}
	if count > 8 {
		count = 8
	}
	var ids []string
	for i := 0; i < count; i++ {
		ids = append(ids, fmt.Sprintf("%d", i))
	}
	return strings.Join(ids, ",")
}

func ref_bind(pid int64, owner, repo string) (string, error) {
	return ref_bind_at(pid, owner, repo, 0)
}

// ref_bind_at: theorize starting from commit index startIdx.
// after theorizing one batch, fork an eval child to decide "continue or sufficient".
// if continue, push a new STEP_REF_BIND with SHA=cursor so kernel picks up next batch.
func ref_bind_at(pid int64, owner, repo string, startIdx int) (string, error) {
	commits, err := RefListCommits(owner, repo)
	if err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return "", fmt.Errorf("ref_bind: %s/%s no commits", owner, repo)
	}

	// find first source-heavy commit at or after startIdx
	var theoryCommit *RefCommit
	var theoryIdx int
	for i := startIdx; i < len(commits) && i < startIdx+20; i++ {
		c := commits[i]
		files, _ := RefTreeAt(owner, repo, c.SHA)
		src_count := 0
		for _, f := range files {
			if !isBinaryFile(f) && !isDocOnlyFile(f) {
				src_count++
			}
		}
		log.Printf("ref_bind: %s/%s %s: %d files (%d source)",
			owner, repo, short_sha(c.SHA), len(files), src_count)
		if src_count >= 2 {
			theoryCommit = &commits[i]
			theoryIdx = i
			break
		}
	}

	if theoryCommit == nil {
		if startIdx == 0 {
			theoryCommit = &commits[0]
			theoryIdx = 0
			log.Printf("ref_bind: %s/%s no source-heavy commit in first 20, using first",
				owner, repo)
		} else {
			// no more source commits after startIdx — done
			log.Printf("ref_bind: %s/%s no more source commits after index %d, done",
				owner, repo, startIdx)
			count := theorem_count(owner + "/" + repo)
			return fmt.Sprintf("%s/%s: %d commits %d theorems (exhausted)",
				owner, repo, len(commits), count), nil
		}
	}

	theoryFiles, _ := RefTreeAt(owner, repo, theoryCommit.SHA)
	log.Printf("ref_bind: %s/%s %d commits, theorize=%s (index %d) %d files",
		owner, repo, len(commits), short_sha(theoryCommit.SHA), theoryIdx, len(theoryFiles))

	ref_theorize(pid, owner, repo, theoryCommit.SHA, "", len(theoryFiles), theoryCommit.Message)

	count := theorem_count(owner + "/" + repo)
	remaining := len(commits) - theoryIdx - 1

	if remaining <= 0 {
		log.Printf("ref_bind: %s/%s all commits processed", owner, repo)
		result := fmt.Sprintf("%s/%s: %d commits %d theorems", owner, repo, len(commits), count)
		log.Printf("ref_bind: %s", result)
		return result, nil
	}

	// don't eval theory quality here — that's subjective.
	// dev_from_theorems will build code; when all units pass, the pipeline
	// ends and the main conversation (on the real machine) handles testing.
	log.Printf("ref_bind: %s/%s theorized commit %d, %d remaining (dev_from_theorems will eval)",
		owner, repo, theoryIdx, remaining)

	result := fmt.Sprintf("%s/%s: %d commits %d theorems (%d remaining)",
		owner, repo, len(commits), count, remaining)
	log.Printf("ref_bind: %s", result)
	return result, nil
}

// ref_bind_cursor: find the highest commit index that ref_bind processed for this repo.
// scans the step queue for the last STEP_REF_BIND with matching owner/repo.
func ref_bind_cursor(pid int64, owner, repo string) int {
	prStepsMu.Lock()
	defer prStepsMu.Unlock()
	best := 0
	for _, s := range prSteps[pid] {
		if s.Kind == STEP_REF_BIND && s.Owner == owner && s.Repo == repo {
			idx := 0
			if strings.HasPrefix(s.Command, "start=") {
				fmt.Sscanf(s.Command, "start=%d", &idx)
			}
			if idx > best {
				best = idx
			}
		}
	}
	return best
}

// dev_split_theory splits the merged theory blob into discrete units.
// splits on "## FILE" / "=== " / "### " section headers.
// each unit is one conceptual piece (one file's LAYER 1+2, or one function group).
// dev_split_theory splits theory into units grouped by directory.
// individual ## FILE sections are too small (a few dozen lines each).
// group all files under the same directory into one unit:
//   src/core/*.cpp → one unit
//   src/kernels/*.cu → one unit
//   include/strata/core/*.hpp → one unit
// if no ## FILE headers found, return as single unit.
// ---------------------------------------------------------------------------
// dev_plan: fork a planner conv to split theory into fine-grained units.
//
// The planner does NOT write code. It reads the theory, sees the ## FILE
// headers, and outputs a structured plan: which files go into which unit,
// max ~10 files per unit, respecting coupling (files in same dir together).
//
// If the planner fails or returns unparseable output, fallback to static split.
// ---------------------------------------------------------------------------

type devUnit struct {
	Files   []string // file paths assigned to this unit
	Theory  string   // the theory text for just these files
}

// dev_store_theory: write theory to .einsteinOS/theory.md on the branch.
// planner and dev convs read it from there with view/grep.
func dev_store_theory(theory, repo, branch string) {
	devRepos := GetDevRepos()
	if len(devRepos) == 0 {
		log.Printf("dev_store_theory: no dev repos, skipping")
		return
	}
	parts := strings.Split(devRepos[0], "/")
	repoName := parts[len(parts)-1]
	repoDir := fmt.Sprintf("/home/claude/%s", repoName)

	tmpFile := "/tmp/theory_full.md"
	if err := os.WriteFile(tmpFile, []byte(theory), 0644); err != nil {
		log.Printf("dev_store_theory: write tmp failed: %v", err)
		return
	}

	script := fmt.Sprintf(`cd %s 2>/dev/null || exit 1
git fetch origin 2>/dev/null
git checkout %s 2>/dev/null || git checkout -b %s 2>/dev/null
mkdir -p .einsteinOS
cp %s .einsteinOS/theory.md
git add .einsteinOS/theory.md
git commit -m "theory: %d bytes" --allow-empty 2>/dev/null
TOKEN=$(cat /tmp/.gh_token 2>/dev/null)
git push "https://${TOKEN}@github.com/%s.git" %s 2>/dev/null
echo "stored theory %d bytes"`,
		repoDir,
		branch, branch,
		tmpFile,
		len(theory),
		devRepos[0], branch,
		len(theory))

	child, err := ForkExec(script, repoDir)
	if err != nil {
		log.Printf("dev_store_theory: ForkExec failed: %v", err)
		return
	}
	Wait(child)
	log.Printf("dev_store_theory: %s stored %d bytes to %s:.einsteinOS/theory.md",
		repo, len(theory), branch)
}

func dev_plan(pid int64, repo string, theory string, branch string) []devUnit {
	fileSections := dev_extract_file_sections(theory)
	if len(fileSections) <= 10 {
		log.Printf("dev_plan: %s %d sections ≤ 10, single unit", repo, len(fileSections))
		return []devUnit{{
			Files:  dev_section_paths(fileSections),
			Theory: theory,
		}}
	}

	// build module list for planner — names + sizes, not content
	var moduleList strings.Builder
	for i, sec := range fileSections {
		moduleList.WriteString(fmt.Sprintf("  %d. %s (%d bytes)\n", i+1, sec.path, len(sec.content)))
	}

	devRepos := GetDevRepos()

	cloneCmd := ""
	if len(devRepos) > 0 {
		cloneCmd = fmt.Sprintf(
			"git clone https://github.com/%s.git /tmp/devrepo 2>/dev/null\n"+
				"cd /tmp/devrepo && git checkout %s 2>/dev/null\n"+
				"view .einsteinOS/theory.md  # full theory is here\n",
			devRepos[0], branch)
	}

	plannerPrompt := fmt.Sprintf(
		"[TASK] group the %d modules below into implementation units.\n\n"+
			"[IMPORTANT] the complete list of modules is below. this IS the input.\n"+
			"you do NOT need to search for files, clone repos, or find anything.\n"+
			"just read the list and group them.\n\n"+
			"[MODULES] (%d entries — each is a ## MODULE section from the theory)\n%s\n"+
			"[OPTIONAL] if you need to understand module dependencies to group better:\n%s\n"+
			"[RULES]\n"+
			"- group related modules together (same subsystem)\n"+
			"- foundational modules first (types, config, utils), then core, then high-level\n"+
			"- each unit should be a meaningful chunk of work for one developer session\n"+
			"- don't split too fine: a unit with 1-2 modules is too small unless they're very large\n"+
			"- don't lump too much: a unit with 15+ modules is too big for one session\n"+
			"- sweet spot: 5-10 modules per unit for most cases\n"+
			"- every module from the list above must appear in exactly one unit\n\n"+
			"[OUTPUT FORMAT]\n"+
			"output ONLY lines like:\n"+
			"unit: module_name_1, module_name_2, module_name_3\n"+
			"unit: module_name_4, module_name_5\n"+
			"use the EXACT module names from the list above (copy-paste). one line per unit. nothing else.\n",
		len(fileSections),
		len(fileSections), moduleList.String(),
		cloneCmd)

	child, err := ForkWithRole(plannerPrompt, ".", "dev_planner")
	if err != nil {
		log.Printf("dev_plan: %s planner fork failed: %v, using static split", repo, err)
		return dev_static_split(fileSections)
	}
	if m := getMeta(child); m != nil && m.ConvID != "" {
		RegisterConv(pid, m.ConvID, repo, "dev_planner", 0)
	}
	Wait(child)
	planOutput := GetResult(child)
	log.Printf("dev_plan: %s planner returned %d bytes", repo, len(planOutput))

	units := dev_parse_plan(planOutput, fileSections)
	if len(units) == 0 {
		log.Printf("dev_plan: %s planner output unparseable, using static split", repo)
		return dev_static_split(fileSections)
	}

	log.Printf("dev_plan: %s planner split %d modules into %d units", repo, len(fileSections), len(units))
	for i, u := range units {
		log.Printf("dev_plan:   unit %d: %d modules, %d bytes theory", i+1, len(u.Files), len(u.Theory))
	}
	return units
}

// dev_parse_plan: parse "unit: file1, file2, ..." lines from planner output.
//
// the planner sees module names like "expert_cache (expert routing weight cache)"
// but may output just "expert_cache" in its unit lines. we need fuzzy matching:
//   1. exact match on sectionMap key
//   2. prefix match: planner output is a prefix of the section key
//   3. normalized match: strip parenthetical, lowercase, compare
func dev_parse_plan(output string, sections []devFileSection) []devUnit {
	// build exact map and prefix index
	sectionMap := make(map[string]*devFileSection)
	for i := range sections {
		sectionMap[sections[i].path] = &sections[i]
	}

	// dev_match_section: find the section that best matches a planner name.
	// returns the section and its map key, or nil if no match.
	matchSection := func(name string) (*devFileSection, string) {
		// 1. exact match
		if sec, ok := sectionMap[name]; ok {
			return sec, name
		}
		// 2. prefix match: "expert_cache" matches "expert_cache (expert routing...)"
		for key, sec := range sectionMap {
			if strings.HasPrefix(key, name+" ") || strings.HasPrefix(key, name+"(") {
				return sec, key
			}
		}
		// 3. normalized: strip parens from both, lowercase compare
		normName := strings.ToLower(strings.TrimSpace(name))
		for key, sec := range sectionMap {
			normKey := key
			if parenIdx := strings.Index(normKey, "("); parenIdx > 0 {
				normKey = strings.TrimSpace(normKey[:parenIdx])
			}
			normKey = strings.ToLower(normKey)
			if normKey == normName {
				return sec, key
			}
		}
		// 4. substring: planner name appears anywhere in section key
		for key, sec := range sectionMap {
			if strings.Contains(strings.ToLower(key), normName) {
				return sec, key
			}
		}
		return nil, ""
	}

	var units []devUnit
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "unit:") {
			continue
		}
		rest := strings.TrimSpace(line[5:])
		if rest == "" {
			continue
		}

		var files []string
		var theoryBuf strings.Builder
		matched := 0
		for _, f := range strings.Split(rest, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			files = append(files, f)
			if sec, key := matchSection(f); sec != nil {
				theoryBuf.WriteString(sec.content)
				theoryBuf.WriteString("\n\n")
				delete(sectionMap, key) // claimed
				matched++
			}
		}
		if len(files) > 0 {
			units = append(units, devUnit{
				Files:  files,
				Theory: strings.TrimSpace(theoryBuf.String()),
			})
			if matched < len(files) {
				log.Printf("dev_parse_plan: unit has %d files but only %d matched theory sections",
					len(files), matched)
			}
		}
	}

	// any unclaimed sections? distribute into chunks, not one giant unit
	if len(sectionMap) > 0 && len(units) > 0 {
		var unclaimed []*devFileSection
		for _, sec := range sectionMap {
			unclaimed = append(unclaimed, sec)
		}
		log.Printf("dev_plan: %d unclaimed modules after matching", len(unclaimed))

		// split unclaimed into groups of 8, each becomes its own unit
		for start := 0; start < len(unclaimed); start += 8 {
			end := start + 8
			if end > len(unclaimed) {
				end = len(unclaimed)
			}
			var files []string
			var theory strings.Builder
			for _, sec := range unclaimed[start:end] {
				files = append(files, sec.path)
				theory.WriteString(sec.content)
				theory.WriteString("\n\n")
			}
			units = append(units, devUnit{
				Files:  files,
				Theory: strings.TrimSpace(theory.String()),
			})
		}
		log.Printf("dev_plan: %d unclaimed modules → %d extra units",
			len(unclaimed), (len(unclaimed)+7)/8)
	}

	return units
}

// dev_static_split: fallback when planner fails. group by directory, split
// large directories into chunks of ≤10 files.
func dev_static_split(sections []devFileSection) []devUnit {
	dirOrder := []string{}
	dirSections := map[string][]devFileSection{}
	for _, sec := range sections {
		d := sec.dir
		if d == "" {
			d = "/"
		}
		if _, ok := dirSections[d]; !ok {
			dirOrder = append(dirOrder, d)
		}
		dirSections[d] = append(dirSections[d], sec)
	}

	var units []devUnit
	for _, d := range dirOrder {
		secs := dirSections[d]
		// chunk large directories into groups of 10
		for start := 0; start < len(secs); start += 10 {
			end := start + 10
			if end > len(secs) {
				end = len(secs)
			}
			chunk := secs[start:end]
			var files []string
			var theory strings.Builder
			theory.WriteString(fmt.Sprintf("# Directory: %s (files %d-%d of %d)\n\n",
				d, start+1, end, len(secs)))
			for _, sec := range chunk {
				files = append(files, sec.path)
				theory.WriteString(sec.content)
				theory.WriteString("\n\n")
			}
			s := strings.TrimSpace(theory.String())
			if len(s) > 100 {
				units = append(units, devUnit{Files: files, Theory: s})
			}
		}
	}

	if len(units) == 0 {
		// nothing parsed
		var allContent strings.Builder
		var allFiles []string
		for _, sec := range sections {
			allFiles = append(allFiles, sec.path)
			allContent.WriteString(sec.content)
			allContent.WriteString("\n\n")
		}
		units = []devUnit{{Files: allFiles, Theory: allContent.String()}}
	}

	log.Printf("dev_static_split: %d files -> %d units", len(sections), len(units))
	for i, u := range units {
		log.Printf("dev_static_split:   unit %d: %d files, %d bytes", i+1, len(u.Files), len(u.Theory))
	}
	return units
}

// ---------------------------------------------------------------------------
// dev_extract_file_sections: parse ## FILE headers from theory text.
// shared by planner and static split.
// ---------------------------------------------------------------------------

type devFileSection struct {
	path    string
	dir     string
	content string
}

func dev_extract_file_sections(theory string) []devFileSection {
	lines := strings.Split(theory, "\n")
	var sections []devFileSection
	var cur strings.Builder
	curPath := ""

	flush := func() {
		s := strings.TrimSpace(cur.String())
		if len(s) > 50 {
			dir := ""
			if idx := strings.LastIndex(curPath, "/"); idx > 0 {
				dir = curPath[:idx]
			}
			sec := devFileSection{path: curPath, dir: dir, content: s}
			// skip shadow/local paths
			cleanPath := strings.Trim(sec.path, "\"'` \t")
			cleanDir := strings.Trim(sec.dir, "\"'` \t")
			if strings.HasPrefix(cleanPath, "/home/claude") || strings.HasPrefix(cleanDir, "/home/claude") ||
				strings.HasPrefix(cleanPath, "/mnt/") || strings.HasPrefix(cleanDir, "/mnt/") ||
				strings.HasPrefix(cleanPath, "/tmp/") || strings.HasPrefix(cleanDir, "/tmp/") {
				// skip
			} else if strings.Contains(cleanPath, "## FILE:") || strings.Contains(cleanDir, "## FILE:") {
				// skip malformed
			} else {
				sec.path = cleanPath
				sec.dir = cleanDir
				sections = append(sections, sec)
			}
		}
		cur.Reset()
		curPath = ""
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		isHeader := false
		newPath := ""
		// support both ## MODULE: (new) and ## FILE: (legacy)
		if strings.Contains(trimmed, "## MODULE:") {
			isHeader = true
			idx := strings.Index(trimmed, "## MODULE:")
			newPath = strings.TrimSpace(trimmed[idx+len("## MODULE:"):])
		} else if strings.HasPrefix(trimmed, "## MODULE") {
			isHeader = true
			newPath = strings.TrimSpace(trimmed[len("## MODULE"):])
			newPath = strings.TrimLeft(newPath, ": ")
		} else if strings.Contains(trimmed, "## FILE:") {
			isHeader = true
			idx := strings.Index(trimmed, "## FILE:")
			newPath = strings.TrimSpace(trimmed[idx+len("## FILE:"):])
		} else if strings.HasPrefix(trimmed, "## FILE") {
			isHeader = true
			newPath = strings.TrimSpace(trimmed[len("## FILE"):])
			newPath = strings.TrimLeft(newPath, ": ")
		}
		if isHeader {
			newPath = strings.Trim(newPath, "\"'` \t\r\n")
		}
		if isHeader && cur.Len() > 0 {
			flush()
		}
		if isHeader {
			curPath = newPath
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	flush()
	return sections
}

func dev_section_paths(sections []devFileSection) []string {
	var paths []string
	for _, s := range sections {
		if s.path != "" {
			paths = append(paths, s.path)
		}
	}
	return paths
}

// dev_split_theory: compat wrapper — returns []string for callers that
// don't use the new devUnit structure yet. will be removed once
// dev_from_theorems fully migrates.
func dev_split_theory(theory string) []string {
	sections := dev_extract_file_sections(theory)
	units := dev_static_split(sections)
	var result []string
	for _, u := range units {
		result = append(result, u.Theory)
	}
	return result
}



func dev_from_theorems(pid int64, step *Step) (string, error) {
	repo := step.Owner + "/" + step.Repo

	theory := theorem_get(repo, "")
	if theory == "" {
		return "", fmt.Errorf("dev_from_theorems: no theory for %s", repo)
	}

	pr := GetPR(pid)
	task := ""
	hw_info := ""
	gpuAll, gpuUsable := pr_gpu_args(pid)
	if pr != nil {
		task = strip_credentials(pr.Title)
		hw_info = pr.HWInfo
	}

	if pr != nil {
		writeGHToken(pr.Title)
	}

	// strip ref repo references from task — dev conv must NOT know the source repo.
	// it should only see the dev repo and the abstract theory.
	if pr != nil {
		for _, ref := range pr.RefRepos {
			refFull := ref.Owner + "/" + ref.Name
			task = strings.ReplaceAll(task, refFull, "(reference project)")
			task = strings.ReplaceAll(task, "github.com/"+refFull, "(reference)")
			task = strings.ReplaceAll(task, ref.Name, "(ref)")
		}
	}

	// shared branch for all incremental children
	branch := fmt.Sprintf("einsteinOS/dev-%d", pid)

	// store theory to branch — planner and dev convs read it from there
	dev_store_theory(theory, repo, branch)

	// planner decides the split — NOT the dev conv, NOT static grouping
	units := dev_plan(pid, repo, theory, branch)
	log.Printf("dev_from_theorems: %s %d planned units, total %d bytes theory, gpu_all=%s gpu_usable=%s",
		repo, len(units), len(theory), gpuAll, gpuUsable)

	devRepos := GetDevRepos()
	var devRepoBlock string
	if len(devRepos) > 0 {
		var drb strings.Builder
		drb.WriteString("[DEV REPO]\n")
		for _, dr := range devRepos {
			parts := strings.Split(dr, "/")
			drb.WriteString(fmt.Sprintf("  %s → /home/claude/%s/\n", dr, parts[len(parts)-1]))
		}
		devRepoBlock = drb.String()
	}

	var lastStdout string
	var lastStatus map[string]string

	for i, unit := range units {
		var p strings.Builder

		if hw_info != "" {
			p.WriteString("[HARDWARE]\n")
			p.WriteString(hw_info)
			p.WriteString("\n\n")
		}

		// --- conv address list: prior units as addresses, NOT full text ---
		// dev conv gets: summary + convID for each prior unit.
		// it reads code from the branch (git checkout). if it needs to understand
		// WHY a prior unit made a decision, it can DownloadConv(convID).
		// this keeps the prompt small: ~200 bytes per prior unit, not ~200KB.
		if i > 0 {
			p.WriteString(fmt.Sprintf("[PRIOR UNITS 1..%d — code + work logs on branch %s]\n", i, branch))
			for j := 0; j < i; j++ {
				rec := FindConv(repo, fmt.Sprintf("dev_unit_%d", j+1), j+1)
				convStatus := "done"
				if rec != nil {
					convStatus = rec.Status
				}
				fileList := ""
				if j < len(units) {
					fileList = strings.Join(units[j].Files, ", ")
					if len(fileList) > 120 {
						fileList = fileList[:120] + "..."
					}
				}
				p.WriteString(fmt.Sprintf("  unit %d: status=%s files=[%s]\n",
					j+1, convStatus, fileList))
				p.WriteString(fmt.Sprintf("    work log: .einsteinOS/convs/dev_unit_%d.md\n", j+1))
			}
			p.WriteString("\nall code is on the branch. git checkout and READ the source files.\n")
			p.WriteString("work logs in .einsteinOS/convs/ contain each unit's full process:\n")
			p.WriteString("  tool calls, compile output, decisions. use view/grep to read selectively.\n")
			p.WriteString("  do NOT cat the whole file — use view with line ranges or grep for specifics.\n")
			p.WriteString("do NOT rewrite prior code unless your theory REQUIRES it.\n\n")
		}

		// --- theory reference: on the branch, not in the prompt ---
		p.WriteString(fmt.Sprintf("[THEORY — unit %d/%d, %d modules]\n", i+1, len(units), len(unit.Files)))
		p.WriteString(fmt.Sprintf("your modules: %s\n", strings.Join(unit.Files, ", ")))
		p.WriteString(fmt.Sprintf("full theory is at: .einsteinOS/theory.md on branch %s\n", branch))
		p.WriteString("after git checkout, use view/grep to read the ## MODULE sections for YOUR modules.\n")
		p.WriteString("example: grep -n 'MODULE:' .einsteinOS/theory.md  — find your sections\n")
		p.WriteString("         view .einsteinOS/theory.md <start> <end>  — read a section\n")
		p.WriteString("you decide what to read and how much.\n\n")

		// --- strict scope: execute only, no self-judging ---
		p.WriteString("[YOUR ROLE]\n")
		p.WriteString("you are an EXECUTOR — write code, compile, push. that's it.\n")
		p.WriteString("a separate REVIEW conversation will judge your work after you finish.\n")
		p.WriteString("do NOT decide whether your work is 'good enough' — just do your best.\n")
		p.WriteString("do NOT fork new conversations or delegate to sub-tasks.\n")
		p.WriteString("do NOT spend tools exploring beyond what YOUR modules need.\n")
		p.WriteString("if you can't finish a module, write what you can and note it in your output.\n\n")

		if i > 0 {
			p.WriteString(fmt.Sprintf("[EXISTING CODE]\n"+
				"branch %s has code from units 1..%d.\n"+
				"  cd /home/claude/<repo> && git fetch origin && git checkout %s\n"+
				"read what's there. ADD your new code, then compile the WHOLE project.\n\n",
				branch, i, branch))
		}

		p.WriteString(fmt.Sprintf("[TASK]\n%s\n\n", task))

		if devRepoBlock != "" {
			p.WriteString(devRepoBlock)
			p.WriteString("\n")
		}

		p.WriteString(fmt.Sprintf(dev_incremental_instruction, branch, gpuAll, gpuUsable, gpuUsable, i+1, len(units)))

		feedback := pr_feedback(pid)
		if feedback != "" {
			p.WriteString(fmt.Sprintf("\n\n[PRIOR FEEDBACK]\n%s", feedback))
		}

		log.Printf("dev_from_theorems: %s unit %d/%d forking (prompt %d bytes, %d files: %v)",
			repo, i+1, len(units), p.Len(), len(unit.Files), unit.Files)

		prompt := p.String()
		// termination: behavioral signals only, no hard timeout.
		// ProbeConvBusy handles liveness detection at the transport layer.
		devTermination := OrConditions(
			&BashExitZero{Pattern: "git push"},
			AndConditions(
				&CompileResult{BuildCmd: "make"},
				&ConsecutiveTextRounds{Threshold: 2},
			),
			&MaxRounds{Limit: 20},
		)
		var child int64
		for attempt := 1; attempt <= 5; attempt++ {
			var err error
			child, err = ForkWithTermination(prompt, ".", fmt.Sprintf("dev_unit_%d", i+1), devTermination)
			if err != nil {
				log.Printf("dev_from_theorems: %s unit %d/%d fork failed (attempt %d/5): %v",
					repo, i+1, len(units), attempt, err)
				continue
			}
			Wait(child)
			step.ChildPid = child

			// register AFTER Wait — ConvID is set by model during Fork execution,
			// but may not be available immediately after Fork returns.
			if m := getMeta(child); m != nil && m.ConvID != "" {
				RegisterConv(pid, m.ConvID, repo, fmt.Sprintf("dev_unit_%d", i+1), i+1)
				log.Printf("dev_from_theorems: %s unit %d/%d registered conv %s",
					repo, i+1, len(units), m.ConvID[:8])
			} else {
				log.Printf("dev_from_theorems: %s unit %d/%d WARNING: no ConvID after Wait",
					repo, i+1, len(units))
			}

			lastStdout, lastStatus = dev_collect_child(child, repo)

			// retry logic: only no_response means child did nothing.
			// any other non-empty compile status (pass, fail, unknown, pending, etc.)
			// means dev_collect found real work — don't retry, even if the downloaded
			// text contains "no context" phrases from Continue's preamble.
			// empty compile means dev_collect set nothing — fall through to text heuristic.
			needsRetry := false
			compile := lastStatus["compile"]
			if compile == "no_response" {
				needsRetry = true
			} else if compile != "" {
				// any real status from dev_collect = child worked
				needsRetry = false
			} else if strings.Contains(lastStdout, "[API Error]") ||
				strings.Contains(lastStdout, "TLS handshake timeout") ||
				strings.Contains(lastStdout, "net/http:") {
				log.Printf("dev_from_theorems: %s unit %d/%d child hit API/network error (attempt %d/5), retrying",
					repo, i+1, len(units), attempt)
				needsRetry = true
			}
			if !needsRetry {
				break
			}
			if attempt < 5 {
				log.Printf("dev_from_theorems: %s unit %d/%d no_response (attempt %d/5), retrying fresh",
					repo, i+1, len(units), attempt)
			}
		}

		dev_update_gpu(pid, lastStatus, gpuAll, gpuUsable)
		PRFeedbackAdd(pid, fmt.Sprintf("- dev[%s] unit %d/%d: compile=%s smoke=%s",
			repo, i+1, len(units), lastStatus["compile"], lastStatus["smoke"]))

		// interrupted (SSE broke before compile finished): compile is "pending",
		// empty, or "no_response". this is NOT a failure — the child was cut off.
		devCompile := lastStatus["compile"]
		interrupted := devCompile == "pending" || devCompile == "" || devCompile == "no_response"
		if interrupted {
			log.Printf("dev_from_theorems: %s unit %d/%d interrupted (compile=%q), re-queuing dev_from_theorems",
				repo, i+1, len(units), devCompile)
			pr_push_step(pid, &Step{
				Kind:  STEP_DEV_FROM_THEOREMS,
				Owner: step.Owner,
				Repo:  step.Repo,
			})
			return lastStdout, nil
		}

		// store conv content to branch BEFORE review — review_conv reads it
		if m := getMeta(child); m != nil && m.ConvID != "" {
			dev_store_conv(m.ConvID, repo, branch, i+1, lastStatus)
		}

		// --- REVIEW CONV: separate decision from execution ---
		// dev_unit wrote code and stopped. now fork a review_conv that:
		//   - reads the branch code + .einsteinOS/convs/dev_unit_N.md
		//   - checks compile status from dev_collect
		//   - compares against the theory spec on the branch
		//   - outputs PASS, FAIL: reason
		// review_conv does NOT write code. it only judges.
		reviewVerdict := dev_review_unit(pid, repo, branch, i+1, len(units), unit, lastStatus)
		log.Printf("dev_from_theorems: %s unit %d/%d review → %s",
			repo, i+1, len(units), reviewVerdict)

		if strings.HasPrefix(reviewVerdict, "PASS") {
			log.Printf("dev_from_theorems: %s unit %d/%d pass (review confirmed), branch %s updated",
				repo, i+1, len(units), branch)
		} else {
			// FAIL — push fix, then re-queue remaining units
			log.Printf("dev_from_theorems: %s unit %d/%d review FAIL: %s, pushing fix",
				repo, i+1, len(units), reviewVerdict)
			if lastStatus == nil {
				lastStatus = map[string]string{}
			}
			lastStatus["review"] = reviewVerdict
			dev_push_fix(pid, step.Owner, step.Repo, lastStatus, i+1)
			pr_push_step(pid, &Step{
				Kind:  STEP_DEV_FROM_THEOREMS,
				Owner: step.Owner,
				Repo:  step.Repo,
			})
			if i+1 < len(units) {
				log.Printf("dev_from_theorems: %s %d units remaining, re-queued after fix",
					repo, len(units)-i-1)
			}
			return lastStdout, nil
		}
	}

	// all units passed — pipeline complete.
	// do NOT push vllm or any benchmark step. the main conversation runs on the
	// real machine with shadow+writeback: it will checkout the branch, read the
	// project, probe hardware (ixsmi / nvidia-smi), build, and benchmark itself.
	// it doesn't need hardcoded commands — it reads the code and figures it out.
	log.Printf("dev_from_theorems: %s all %d units pass. pipeline complete, branch %s ready for main conv.",
		repo, len(units), branch)
	PRFeedbackAdd(pid, fmt.Sprintf("- dev_from_theorems: all %d units pass, branch %s ready", len(units), branch))

	// log the conv registry so main conv knows where to find everything
	log.Printf("dev_from_theorems: conv_registry:\n%s", ConvRegistryStatus())

	return lastStdout, nil
}



// vllm_up and vllm_bench removed.
//
// deployment and benchmarking are the main conversation's job.
// the main conv runs on the real machine with shadow+writeback:
//   - shadow intercepts every tool_use and runs it locally
//   - writeback sends local results back to the remote Claude
//   - the remote Claude sees real GPU output, real compile results
//
// so the main conv will:
//   1. checkout the dev branch (from conv registry)
//   2. read the project code to understand the architecture
//   3. probe hardware (ixsmi, device query) — shadow runs it on real GPUs
//   4. build the project — shadow compiles on real machine
//   5. write a generate/benchmark entry point if missing
//   6. run it — shadow executes on real GPUs, writeback returns tok/s
//   7. decide next steps based on actual results
//
// no hardcoded GPU IDs, no hardcoded commands, no vllm assumption.

// open_pr: three steps, all kernel-driven.
//   1. fork verify child: re-run numeric checks, output pass/fail
//   2. if pass: fresh clone, copy new code in, single squashed commit, push
//   3. open PR via GitHub API
//
// follows ef92bb70: child does the judgment, kernel routes.
// Step.Command = "tok_s=42.3"
func open_pr(pid int64, step *Step) (string, error) {
	repo := step.Owner + "/" + step.Repo
	tokS := step.Command
	devRepo := fmt.Sprintf("/home/claude/%s", step.Repo)

	// step 1: fork a verify child — re-check the numbers are real
	verifyPrompt := fmt.Sprintf(
		"[TASK] verify the build in %s is working.\n"+
			"run: cd %s && make so 2>&1 | tail -5\n"+
			"run: ls -la %s/build/*.so 2>/dev/null\n"+
			"check the .so exists and was built recently.\n\n"+
			"[OUTPUT FORMAT]\n"+
			"verify=pass|fail\n"+
			"verify_error=<one line if fail>\n",
		devRepo, devRepo, devRepo,
	)

	verifyChild, err := ForkWithRole(verifyPrompt, ".", "verify_conv")
	if err != nil {
		return "", fmt.Errorf("open_pr: verify fork failed: %w", err)
	}
	Wait(verifyChild)
	verifyResult := GetResult(verifyChild)
	verifyStatus := parse_last_status(verifyResult)

	if verifyStatus != nil && verifyStatus["verify"] != "pass" {
		errMsg := verifyStatus["verify_error"]
		log.Printf("open_pr: %s verify failed: %s", repo, errMsg)
		return verifyResult, fmt.Errorf("open_pr: verify failed: %s", errMsg)
	}

	log.Printf("open_pr: %s verify pass, preparing clean PR", repo)

	// get target branch from dev repo registration (user said "分支是 infccl-0.1")
	baseBranch := DevRepoBranch(step.Owner, step.Repo)
	if baseBranch == "" {
		baseBranch = "main"
	}

	// step 2: fresh clone → copy new code → single commit → push
	branch := fmt.Sprintf("einsteinOS/%s-%d", baseBranch, pid)
	prDir := fmt.Sprintf("/tmp/pr_%s_%d", step.Repo, pid)

	script := fmt.Sprintf(`set -e
rm -rf %s
git clone https://$(cat /tmp/.gh_token 2>/dev/null)@github.com/%s/%s.git %s
cd %s
git checkout %s 2>/dev/null || git checkout -b %s
git checkout -b %s

# copy new code from dev repo, overwrite
rsync -a --exclude='.git' %s/ %s/

git add -A
git diff --cached --stat
git commit -m "feat: %s"
git push origin %s 2>&1`,
		prDir,
		step.Owner, step.Repo, prDir,
		prDir,
		baseBranch, baseBranch,
		branch,
		devRepo, prDir,
		tokS,
		branch)

	pushPid, err := ForkExec(script, "/tmp")
	if err != nil {
		return "", fmt.Errorf("open_pr: push failed: %w", err)
	}
	_, _, _ = Wait(pushPid)
	pushResult := GetResult(pushPid)

	// step 3: open PR via GitHub API
	prScript := fmt.Sprintf(`curl -s -X POST \
		-H "Authorization: token $(cat /tmp/.gh_token 2>/dev/null)" \
		-H "Accept: application/vnd.github.v3+json" \
		"https://api.github.com/repos/%s/%s/pulls" \
		-d '{
			"title": "feat: %s",
			"body": "einsteinOS pipeline pid=%d\n%s",
			"head": "%s",
			"base": "%s"
		}' 2>&1`,
		step.Owner, step.Repo,
		tokS, pid, tokS,
		branch, baseBranch)

	prPid, err := ForkExec(prScript, ".")
	if err != nil {
		log.Printf("open_pr: PR API failed but branch %s pushed: %v", branch, err)
		return pushResult, nil
	}
	_, _, _ = Wait(prPid)
	prResult := GetResult(prPid)

	ForkExec(fmt.Sprintf("rm -rf %s", prDir), "/tmp")

	log.Printf("open_pr: %s PR opened %s → %s", repo, branch, baseBranch)
	return fmt.Sprintf("PR: %s → %s\n%s\n%s", branch, baseBranch, pushResult, prResult), nil
}

// dev_fix: kernel-driven fix step. Step fields carry structured args:
//   Command = failure type (compile_fail | gpu_fail | smoke_fail | needs_source)
//   Path    = error message from child's status block
//   SHA     = comma-separated ref file hints (kernel-selected from compile error)
//   Result  = files_written by previous attempt
//   Dir     = attempt number as string
func dev_fix(pid int64, step *Step) (string, error) {
	repo := step.Owner + "/" + step.Repo
	attempt := dev_fix_attempt(step)

	if attempt > 3 {
		log.Printf("dev_fix: %s exceeded max attempts", repo)
		return step.Result, nil
	}

	pr := GetPR(pid)
	task := ""
	hw_info := ""
	if pr != nil {
		task = strip_credentials(pr.Title)
		hw_info = pr.HWInfo
	}

	theory := theorem_get(repo, "")
	gpuAll, gpuUsable := pr_gpu_args(pid)

	var prompt strings.Builder

	if hw_info != "" {
		prompt.WriteString("[HARDWARE]\n")
		prompt.WriteString(hw_info)
		prompt.WriteString("\n\n")
	}

	if theory != "" {
		prompt.WriteString(fmt.Sprintf("[THEORY from %s]\n%s\n\n", repo, theory))
	}

	prompt.WriteString(fmt.Sprintf("[TASK]\n%s\n\n", task))

	// kernel-computed failure diagnosis — kv format, not prose
	prompt.WriteString("[PRIOR RESULT]\n")
	prompt.WriteString(fmt.Sprintf("status=%s\n", step.Command))
	if step.Path != "" {
		prompt.WriteString(fmt.Sprintf("error=%s\n", step.Path))
	}
	if step.Result != "" {
		prompt.WriteString(fmt.Sprintf("files_written=%s\n", step.Result))
	}
	prompt.WriteString("\n")

	// NO ref source files. the dev child must work from theory + existing dev
	// repo code + compile errors. reading the ref source is "looking at the answer"
	// — it produces code with K(I₂|I₁) ≈ 0 (identical structure, renamed identifiers).
	// the theory (LAYER 1 math + LAYER 2 form template) is the specification;
	// the implementation must be independently derived from it.

	// dev repo listing
	devRepos := GetDevRepos()
	if len(devRepos) > 0 {
		prompt.WriteString("[DEV REPO]\n")
		for _, dr := range devRepos {
			parts := strings.Split(dr, "/")
			prompt.WriteString(fmt.Sprintf("  %s → /home/claude/%s/\n", dr, parts[len(parts)-1]))
		}
		prompt.WriteString("\n")
	}

	prompt.WriteString(fmt.Sprintf(dev_verify_instruction, gpuAll, gpuUsable, gpuUsable, gpuAll))

	feedback := pr_feedback(pid)
	if feedback != "" {
		prompt.WriteString(fmt.Sprintf("\n\n[PRIOR FEEDBACK]\n%s", feedback))
	}

	log.Printf("dev_fix: %s attempt=%d failure=%s gpu_usable=%s",
		repo, attempt, step.Command, gpuUsable)

	child, err := ForkWithRole(prompt.String(), ".", fmt.Sprintf("dev_fix_%d", attempt))
	if err != nil {
		return "", err
	}
	if m := getMeta(child); m != nil && m.ConvID != "" {
		RegisterConv(pid, m.ConvID, repo, fmt.Sprintf("dev_fix_%d", attempt), attempt)
	}
	Wait(child)
	step.ChildPid = child

	stdout, status := dev_collect_child(child, repo)

	dev_update_gpu(pid, status, gpuAll, gpuUsable)
	for k, v := range status {
		PRFeedbackAdd(pid, fmt.Sprintf("- dev[%s]: %s=%s", repo, k, v))
	}

	compilePassed := strings.HasPrefix(status["compile"], "pass")
	smokeOK := status["smoke"] == "" || strings.HasPrefix(status["smoke"], "pass")
	if compilePassed && smokeOK {
		log.Printf("dev_fix: %s verified on attempt %d", repo, attempt)
		return stdout, nil
	}

	// still failing — kernel pushes another fix step
	dev_push_fix(pid, step.Owner, step.Repo, status, attempt+1)
	return stdout, nil
}

// dev_push_fix: kernel classifies failure and pushes a STEP_DEV_FIX.
// no text instructions — only structured args in Step fields.
// dev_store_conv: download a unit's conversation, extract a SUMMARY, and
// push it to .einsteinOS/convs/dev_unit_N.md on the branch.
//
// what we store (useful for next units):
//   - assistant reasoning text (decisions, why they did X)
//   - bash commands run (what they did)
//   - compile/test output (key lines only, truncated)
//
// what we DON'T store (already on branch or too large):
//   - create_file file_text (source code — it's in the git tree)
//   - full shell output (only first/last lines of long output)
//
// the child has view, grep, head — it reads what it needs.
// ---------------------------------------------------------------------------
// dev_review_unit: fork a review_conv to judge whether a dev unit's output
// satisfies the theory spec. the review_conv:
//   - reads branch code + .einsteinOS/convs/dev_unit_N.md (work log)
//   - reads theory from .einsteinOS/theory.md (grep for the unit's modules)
//   - checks compile status from the dev_collect results
//   - outputs PASS or FAIL: <reason>
//
// the review_conv does NOT write code, fix bugs, or modify anything.
// this separates the decision (is this unit done?) from execution (writing code).
// dev_unit convs tend to self-assess optimistically ("compile=pass" even when
// not all modules are implemented). the review_conv catches that.
// ---------------------------------------------------------------------------
func dev_review_unit(pid int64, repo, branch string, unitNum, totalUnits int, unit devUnit, status map[string]string) string {
	devRepos := GetDevRepos()
	if len(devRepos) == 0 {
		// no dev repo — can't review, trust the compile status
		if strings.HasPrefix(status["compile"], "pass") {
			return "PASS (no dev repo to review)"
		}
		return "FAIL: compile=" + status["compile"]
	}

	// fast path: if dev_collect already says compile failed, skip review conv
	if status["compile"] == "fail" || status["compile"] == "error" {
		return "FAIL: compile=" + status["compile"] + " " + status["compile_error"]
	}

	parts := strings.Split(devRepos[0], "/")
	repoName := parts[len(parts)-1]

	var prompt strings.Builder
	prompt.WriteString(fmt.Sprintf("[TASK] review whether dev unit %d/%d is complete and correct.\n\n", unitNum, totalUnits))
	prompt.WriteString("[YOU ARE A REVIEWER — NOT A DEVELOPER]\n")
	prompt.WriteString("- do NOT write code, fix bugs, or modify any files\n")
	prompt.WriteString("- do NOT clone repositories other than the dev repo\n")
	prompt.WriteString("- ONLY read, check, and judge\n\n")

	prompt.WriteString(fmt.Sprintf("[BRANCH] %s\n", branch))
	prompt.WriteString(fmt.Sprintf("[DEV REPO] %s → /home/claude/%s/\n\n", devRepos[0], repoName))

	prompt.WriteString(fmt.Sprintf("[UNIT %d MODULES]\n%s\n\n", unitNum, strings.Join(unit.Files, ", ")))

	prompt.WriteString(fmt.Sprintf("[DEV UNIT STATUS from kernel]\n"))
	for k, v := range status {
		prompt.WriteString(fmt.Sprintf("  %s = %s\n", k, v))
	}
	prompt.WriteString("\n")

	prompt.WriteString("[WHAT TO CHECK]\n")
	prompt.WriteString("1. git clone the dev repo, checkout the branch\n")
	prompt.WriteString("2. ls .einsteinOS/convs/ — find dev_unit_" + fmt.Sprintf("%d", unitNum) + ".md\n")
	prompt.WriteString("3. grep -n 'compile=' .einsteinOS/convs/dev_unit_" + fmt.Sprintf("%d", unitNum) + ".md\n")
	prompt.WriteString("4. check: does source code exist for EACH module listed above?\n")
	prompt.WriteString("   grep for the module names in the source files.\n")
	prompt.WriteString("   a module with no corresponding source file = incomplete.\n")
	prompt.WriteString("5. check: does the code actually compile? run make or the project's build command.\n")
	prompt.WriteString("6. compare code structure against theory:\n")
	prompt.WriteString("   grep -n 'MODULE:' .einsteinOS/theory.md — find sections for THIS unit's modules\n")
	prompt.WriteString("   read the LAYER 1a spec — does the code match the math?\n\n")

	prompt.WriteString("[OUTPUT FORMAT]\n")
	prompt.WriteString("your LAST line must be exactly one of:\n")
	prompt.WriteString("  PASS\n")
	prompt.WriteString("  FAIL: <one line reason>\n")
	prompt.WriteString("nothing else on that line. you may write analysis above it.\n")

	log.Printf("dev_review_unit: %s unit %d/%d forking review_conv (%d bytes prompt)",
		repo, unitNum, totalUnits, prompt.Len())

	// reviewer termination: let the reviewer FINISH its analysis (theory comparison
	// + compile check + verdict), then stop. Do NOT terminate on CompileResult —
	// compile is one piece of evidence, not the verdict. The reviewer's job is to
	// compare implementation against theory spec; cutting it short on compile
	// skips the most valuable check.
	reviewTerm := OrConditions(
		&ConsecutiveTextRounds{Threshold: 2}, // reviewer done reading → writes verdict
		&MaxRounds{Limit: 10},
	)
	child, err := ForkWithTermination(prompt.String(), ".", "review_conv", reviewTerm)
	if err != nil {
		log.Printf("dev_review_unit: %s unit %d fork failed: %v, using compile status",
			repo, unitNum, err)
		if status["compile"] == "pass" {
			return "PASS (review fork failed, using compile status)"
		}
		return "FAIL: review fork failed, compile=" + status["compile"]
	}
	if m := getMeta(child); m != nil && m.ConvID != "" {
		RegisterConv(pid, m.ConvID, repo, fmt.Sprintf("review_unit_%d", unitNum), unitNum)
	}
	Wait(child)

	result := GetResult(child)
	log.Printf("dev_review_unit: %s unit %d review_conv returned %d bytes", repo, unitNum, len(result))

	// collect compile evidence from tool signals (but NOT as the verdict)
	reviewCollector := GetCollector(child)
	compileEvidence := ""
	if reviewCollector != nil {
		compilePassed, compileFound := reviewCollector.CompileStatus()
		if compileFound {
			if compilePassed {
				compileEvidence = "pass"
			} else {
				compileEvidence = "fail"
			}
			log.Printf("dev_review_unit: %s unit %d compile evidence=%s (from tool signal)",
				repo, unitNum, compileEvidence)
		}
	}

	// PRIMARY: the reviewer's own verdict — it compared code against theory spec.
	// scan text for PASS/FAIL.
	lines := strings.Split(strings.TrimSpace(result), "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "PASS" {
			// safety override: reviewer says PASS but compile actually failed
			if compileEvidence == "fail" {
				log.Printf("dev_review_unit: %s unit %d reviewer said PASS but compile failed — overriding to FAIL",
					repo, unitNum)
				return "FAIL: reviewer said PASS but compile failed: " + truncForSignal(reviewCollector.LastCompileOutput(), 200)
			}
			return "PASS"
		}
		if strings.HasPrefix(line, "FAIL:") || strings.HasPrefix(line, "FAIL ") {
			return line
		}
	}

	// reviewer gave no text verdict — try downloading full conversation
	if m := getMeta(child); m != nil && m.ConvID != "" {
		downloaded, err := DownloadConv(m.ConvID)
		if err == nil && len(downloaded) > len(result) {
			dLines := strings.Split(strings.TrimSpace(downloaded), "\n")
			for i := len(dLines) - 1; i >= 0 && i >= len(dLines)-5; i-- {
				line := strings.TrimSpace(dLines[i])
				if line == "PASS" {
					if compileEvidence == "fail" {
						return "FAIL: reviewer said PASS but compile failed"
					}
					return "PASS"
				}
				if strings.HasPrefix(line, "FAIL:") || strings.HasPrefix(line, "FAIL ") {
					return line
				}
			}
		}
	}

	// no verdict from reviewer at all — compile evidence is last resort,
	// but we note that theory comparison was not performed
	if compileEvidence == "pass" {
		return "PASS (review gave no verdict, compile=pass, theory comparison unknown)"
	}
	if compileEvidence == "fail" {
		return "FAIL: review gave no verdict, compile failed"
	}
	// no compile evidence either — fall back to parent's compile status
	if status["compile"] == "pass" {
		return "PASS (review gave no verdict, compile=pass, theory comparison unknown)"
	}
	return "FAIL: review gave no verdict, compile=" + status["compile"]
}

func dev_store_conv(convID, repo, branch string, unitNum int, status map[string]string) {
	rawJSON, err := DownloadConvRaw(convID)
	if err != nil {
		log.Printf("dev_store_conv: %s unit %d download failed: %v", repo, unitNum, err)
		return
	}

	summary := dev_conv_summary(rawJSON, convID, unitNum, status)
	if len(summary) < 50 {
		log.Printf("dev_store_conv: %s unit %d summary too short, skipping", repo, unitNum)
		return
	}

	// write summary to branch
	devRepos := GetDevRepos()
	if len(devRepos) == 0 {
		log.Printf("dev_store_conv: no dev repos registered, skipping")
		return
	}
	parts := strings.Split(devRepos[0], "/")
	repoName := parts[len(parts)-1]
	repoDir := fmt.Sprintf("/home/claude/%s", repoName)
	filePath := fmt.Sprintf(".einsteinOS/convs/dev_unit_%d.md", unitNum)

	// write to temp file first to avoid heredoc escaping issues
	tmpFile := fmt.Sprintf("/tmp/conv_unit_%d.md", unitNum)
	if err := os.WriteFile(tmpFile, []byte(summary), 0644); err != nil {
		log.Printf("dev_store_conv: %s unit %d write temp failed: %v", repo, unitNum, err)
		return
	}

	script := fmt.Sprintf(`cd %s 2>/dev/null || exit 1
git fetch origin 2>/dev/null
git checkout %s 2>/dev/null || git checkout -b %s 2>/dev/null
mkdir -p .einsteinOS/convs
cp %s %s
git add %s
git commit -m "conv: unit %d (%s)" --allow-empty 2>/dev/null
TOKEN=$(cat /tmp/.gh_token 2>/dev/null)
git push "https://${TOKEN}@github.com/%s.git" %s 2>/dev/null
echo "stored %s"`,
		repoDir,
		branch, branch,
		tmpFile, filePath,
		filePath,
		unitNum, convID[:8],
		devRepos[0], branch,
		filePath)

	child, forkErr := ForkExec(script, repoDir)
	if forkErr != nil {
		log.Printf("dev_store_conv: %s unit %d ForkExec failed: %v", repo, unitNum, forkErr)
		return
	}
	Wait(child)
	result := GetResult(child)
	log.Printf("dev_store_conv: %s unit %d stored (%d bytes summary from %d bytes raw): %s",
		repo, unitNum, len(summary), len(rawJSON), strings.TrimSpace(result))
}

// dev_conv_summary: parse raw conversation JSON and extract a compact summary.
// keeps: assistant text, bash commands, compile/test results (truncated).
// drops: create_file file_text, large tool_result output.
func dev_conv_summary(rawJSON []byte, convID string, unitNum int, status map[string]string) string {
	var conv struct {
		ChatMessages []struct {
			Sender  string            `json:"sender"`
			Content []json.RawMessage `json:"content"`
		} `json:"chat_messages"`
	}
	if err := json.Unmarshal(rawJSON, &conv); err != nil {
		return fmt.Sprintf("# unit %d — parse error\n", unitNum)
	}

	var out strings.Builder

	// header
	out.WriteString(fmt.Sprintf("# dev_unit_%d — conv %s\n", unitNum, convID[:8]))
	if status != nil {
		out.WriteString(fmt.Sprintf("# compile: %s  smoke: %s\n", status["compile"], status["smoke"]))
		if files, ok := status["files_written"]; ok {
			out.WriteString(fmt.Sprintf("# files_written: %s\n", files))
		}
	}
	out.WriteString("\n")

	for _, msg := range conv.ChatMessages {
		for _, raw := range msg.Content {
			var block struct {
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Name    string          `json:"name"`
				Input   json.RawMessage `json:"input"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(raw, &block) != nil {
				continue
			}

			switch block.Type {
			case "tool_use":
				if block.Name == "create_file" {
					var inp struct {
						Path string `json:"path"`
					}
					if json.Unmarshal(block.Input, &inp) == nil && inp.Path != "" {
						out.WriteString(fmt.Sprintf("[create_file] %s\n", relPath(inp.Path)))
					}
				} else if block.Name == "str_replace" {
					var inp struct {
						Path   string `json:"path"`
						OldStr string `json:"old_str"`
					}
					if json.Unmarshal(block.Input, &inp) == nil {
						desc := inp.OldStr
						if len(desc) > 80 {
							desc = desc[:80] + "..."
						}
						out.WriteString(fmt.Sprintf("[str_replace] %s: %s\n", relPath(inp.Path), desc))
					}
				} else if block.Name == "bash_tool" || block.Name == "bash" {
					var inp struct {
						Command string `json:"command"`
					}
					if json.Unmarshal(block.Input, &inp) == nil && inp.Command != "" {
						cmd := inp.Command
						if len(cmd) > 200 {
							cmd = cmd[:200] + "..."
						}
						out.WriteString(fmt.Sprintf("$ %s\n", cmd))
					}
				}

			case "tool_result":
				// keep only first+last few lines of output
				var content string
				if json.Unmarshal(block.Content, &content) != nil {
					var parts []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}
					if json.Unmarshal(block.Content, &parts) == nil {
						for _, p := range parts {
							if p.Type == "text" {
								content += p.Text
							}
						}
					}
				}
				if len(content) > 0 {
					lines := strings.Split(content, "\n")
					if len(lines) <= 10 {
						out.WriteString(content)
						out.WriteString("\n")
					} else {
						// first 5 + last 5
						for _, l := range lines[:5] {
							out.WriteString(l + "\n")
						}
						out.WriteString(fmt.Sprintf("... (%d lines omitted)\n", len(lines)-10))
						for _, l := range lines[len(lines)-5:] {
							out.WriteString(l + "\n")
						}
					}
				}
			}
		}
	}

	return out.String()
}

// relPath strips absolute prefixes to get a repo-relative path.
// /home/claude/baloon/include/core/x.hpp → include/core/x.hpp
// /tmp/work/baloon/src/y.cpp → src/y.cpp
// already relative → unchanged
func relPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		return p
	}
	// split on "/" and find the first segment that looks like a repo dir,
	// then return everything after it.
	// common patterns: /home/claude/<repo>/, /tmp/<repo>/, /mnt/.../<repo>/
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		// skip empty, home, claude, tmp, mnt, user-data, uploads, etc.
		if seg == "" || seg == "home" || seg == "claude" || seg == "tmp" ||
			seg == "mnt" || seg == "user-data" || seg == "uploads" || seg == "outputs" {
			continue
		}
		// first non-system segment is likely the repo name — return rest
		if i+1 < len(parts) {
			return strings.Join(parts[i+1:], "/")
		}
	}
	return p
}

func dev_push_fix(pid int64, owner, repo string, status map[string]string, attempt int) {
	if attempt > 3 {
		log.Printf("dev_push_fix: %s/%s max fix attempts reached, re-queuing dev_from_theorems for fresh plan",
			owner, repo)
		// don't just stop — re-queue so the pipeline replans and retries.
		// the planner may split differently, or the network may be better.
		pr_push_step(pid, &Step{
			Kind:  STEP_DEV_FROM_THEOREMS,
			Owner: owner,
			Repo:  repo,
		})
		return
	}

	failureType := "unknown"
	errorMsg := ""
	filesWritten := status["files_written"]

	switch {
	case status["compile"] == "no_response":
		log.Printf("dev_push_fix: %s/%s no_response, retrying same step (not counting attempt)",
			owner, repo)
		pr_push_step(pid, &Step{
			Kind:  STEP_DEV_FROM_THEOREMS,
			Owner: owner,
			Repo:  repo,
		})
		return

	case status["compile"] == "unknown":
		failureType = "compile_unknown"
		errorMsg = status["compile_error"]
		filesWritten = status["files_written"]

	case status["compile"] == "fail":
		failureType = "compile_fail"
		errorMsg = status["compile_error"]
		// NO source hints. fix child works from theory + compile error + existing dev code.

	case status["gpu_test"] == "fail":
		failureType = "gpu_fail"
		errorMsg = status["gpu_test_detail"]

	case status["smoke"] == "fail":
		failureType = "smoke_fail"
		errorMsg = status["smoke_error"]
	}

	log.Printf("dev_push_fix: %s/%s type=%s attempt=%d",
		owner, repo, failureType, attempt)

	pr_push_step(pid, &Step{
		Kind:    STEP_DEV_FIX,
		Owner:   owner,
		Repo:    repo,
		Command: failureType,
		Path:    errorMsg,
		Result:  filesWritten,
		Dir:     fmt.Sprintf("%d", attempt),
	})
}

// dev_select_source_files: kernel reads ref repo and returns formatted source
// for files matching hints or compile error references.
func dev_select_source_files(owner, repo, hints, compileError string) string {
	commit, _, err := RefFirstCommit(owner, repo)
	if err != nil {
		return ""
	}

	files, _ := RefTreeAt(owner, repo, commit.SHA)

	// collect wanted files from hints (child's needs_source_files or kernel's extract)
	wanted := make(map[string]bool)
	for _, f := range strings.Split(hints, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			wanted[f] = true
		}
	}

	// if no hints, kernel picks key files (headers, engine, transport, main)
	if len(wanted) == 0 {
		for _, f := range files {
			if len(wanted) >= 5 {
				break
			}
			if isBinaryFile(f) {
				continue
			}
			if strings.HasSuffix(f, ".h") || strings.Contains(f, "engine") ||
				strings.Contains(f, "transport") || strings.Contains(f, "main") {
				wanted[f] = true
			}
		}
	}

	var src strings.Builder
	read := 0
	for _, f := range files {
		if !wanted[f] {
			base := f
			if idx := strings.LastIndex(f, "/"); idx >= 0 {
				base = f[idx+1:]
			}
			if !wanted[base] {
				continue
			}
		}
		content, e := RefFileAt(owner, repo, commit.SHA, f)
		if e != nil {
			continue
		}
		lines := strings.Count(content, "\n") + 1
		src.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", f, lines))
		src.WriteString(content)
		src.WriteString("\n\n")
		read += lines
	}

	log.Printf("dev_select_source: %s/%s provided %d files (%d lines)",
		owner, repo, len(wanted), read)
	return src.String()
}

// dev_extract_source_hints: kernel parses compile error for ref file names.
func dev_extract_source_hints(owner, repo, compileError string) string {
	if compileError == "" {
		return ""
	}
	commit, _, err := RefFirstCommit(owner, repo)
	if err != nil {
		return ""
	}
	files, _ := RefTreeAt(owner, repo, commit.SHA)

	var hints []string
	for _, f := range files {
		base := f
		if idx := strings.LastIndex(f, "/"); idx >= 0 {
			base = f[idx+1:]
		}
		name := strings.TrimSuffix(base, ".go")
		name = strings.TrimSuffix(name, ".h")
		name = strings.TrimSuffix(name, ".c")
		name = strings.TrimSuffix(name, ".py")
		if name != "" && len(name) > 3 && strings.Contains(strings.ToLower(compileError), strings.ToLower(name)) {
			hints = append(hints, f)
		}
	}
	if len(hints) > 5 {
		hints = hints[:5]
	}
	return strings.Join(hints, ",")
}

// dev_fix_attempt: read attempt number from Step.Dir
func dev_fix_attempt(step *Step) int {
	n := 1
	if step.Dir != "" {
		fmt.Sscanf(step.Dir, "%d", &n)
	}
	return n
}

// pr_gpu_args: read GPU config from PRProcess, with defaults.
// dev_collect_child: kernel collects child output through system channels.
// 1. GetResult (stdout text) — try parse_last_status
// 2. GetConvFiles (shadow DB) — what files the child actually wrote
// 3. DownloadConv fallback — extract tool_result (shell output) from conversation
// child doesn't need to output a status block. kernel inspects what happened.
func dev_collect_child(childPid int64, repo string) (string, map[string]string) {
	stdout := GetResult(childPid)

	// PRIMARY: extract status from tool signal collector (structured, no text parsing)
	toolStatus := CollectChild(childPid)
	if toolStatus != nil && toolStatus["compile"] != "" {
		log.Printf("dev_collect: %s status from tool signals: compile=%s files=%s",
			repo, toolStatus["compile"], toolStatus["files_written"])
		if files := dev_child_files(childPid); len(files) > 0 {
			toolStatus["files_written"] = strings.Join(files, ",")
		}
		return stdout, toolStatus
	}

	// FALLBACK: text-based status (for backward compat during migration)
	status := parse_last_status(stdout)
	if status != nil {
		log.Printf("dev_collect: %s status from text (fallback): compile=%s", repo, status["compile"])
		// also record files from shadow DB
		if files := dev_child_files(childPid); len(files) > 0 {
			status["files_written"] = strings.Join(files, ",")
		}
		return stdout, status
	}

	// no status in text — check shadow DB for what child actually did
	files := dev_child_files(childPid)
	log.Printf("dev_collect: %s no status in text, shadow files=%v", repo, files)

	// DownloadConv: get full conversation including tool_result (shell output)
	convID := ""
	if m := getMeta(childPid); m != nil {
		convID = m.ConvID
	}
	// fallback: FindConv from registry (survives zombie reap)
	if convID == "" {
		for _, role := range []string{"dev_draft", "dev_fix_1", "dev_fix_2", "dev_fix_3"} {
			if rec := FindConv(repo, role, 0); rec != nil {
				convID = rec.ConvID
				log.Printf("dev_collect: %s convID from registry role=%s", repo, role)
				break
			}
		}
	}
	if convID != "" {
		downloaded, err := DownloadConv(convID)
		if err == nil && len(downloaded) > len(stdout) {
			log.Printf("dev_collect: %s downloaded %d chars (stdout was %d)",
				repo, len(downloaded), len(stdout))
			status = parse_last_status(downloaded)
			if status != nil {
				log.Printf("dev_collect: %s status from download: compile=%s", repo, status["compile"])
				if len(files) > 0 {
					status["files_written"] = strings.Join(files, ",")
				}
				return downloaded, status
			}
			stdout = downloaded
		}

		// SSE broke but remote may still be running. Poll like ref_split.
		// dev children write 30+ files then compile — if SSE breaks mid-write,
		// remote continues and finishes compile+push on the server side.
		if len(stdout) < 500 {
			log.Printf("dev_collect: %s stdout thin (%d bytes), polling conv %s (exit after 5 stale)",
				repo, len(stdout), convID[:8])
			maxSeen := len(stdout)
			prevSize := len(stdout)
			staleCount := 0
			for poll := 0; staleCount < 5; poll++ {
				time.Sleep(3 * time.Minute)
				redownloaded, dlErr := DownloadConv(convID)
				curSize := 0
				if dlErr == nil {
					curSize = len(redownloaded)
				}
				if dlErr != nil {
					if busy, probeErr := ProbeConvBusy(convID); probeErr == nil && busy {
						log.Printf("dev_collect: %s poll %d: download error but remote still busy, waiting", repo, poll+1)
						continue
					}
					staleCount++
					log.Printf("dev_collect: %s poll %d: download error (stale %d/5)", repo, poll+1, staleCount)
					continue
				}
				log.Printf("dev_collect: %s poll %d: %d bytes (prev %d, max %d, stale %d/5)",
					repo, poll+1, curSize, prevSize, maxSeen, staleCount)

				if curSize == 0 {
					if maxSeen <= 500 {
						if busy, probeErr := ProbeConvBusy(convID); probeErr == nil && busy {
							log.Printf("dev_collect: %s poll %d: 0 bytes but remote still busy, waiting", repo, poll+1)
							continue
						}
						log.Printf("dev_collect: %s poll %d: 0 bytes, remote idle — conversation dead", repo, poll+1)
						break
					}
					staleCount++
					continue
				}
				if curSize > maxSeen {
					maxSeen = curSize
					staleCount = 0
				} else {
					if busy, probeErr := ProbeConvBusy(convID); probeErr == nil && busy {
						log.Printf("dev_collect: %s poll %d: %d bytes unchanged but remote still busy, waiting",
							repo, poll+1, curSize)
					} else {
						// remote idle + content stale → ContinueConv to resume
						if staleCount == 0 && curSize > 500 {
							log.Printf("dev_collect: %s poll %d: remote idle, sending ContinueConv to resume",
								repo, poll+1)
							ContinueConv(convID, "continue — finish compiling, testing, and push to the branch")
						} else {
							staleCount++
						}
					}
				}
				if curSize > 500 {
					if curSize == prevSize {
						log.Printf("dev_collect: %s remote finished: %d bytes stable", repo, curSize)
						stdout = redownloaded
						status = parse_last_status(redownloaded)
						if status != nil {
							log.Printf("dev_collect: %s status from poll: compile=%s", repo, status["compile"])
							if len(files) > 0 {
								status["files_written"] = strings.Join(files, ",")
							}
							return stdout, status
						}
						break
					}
					if curSize > len(stdout) {
						stdout = redownloaded
					}
				}
				prevSize = curSize
			}
			if len(stdout) > 500 {
				log.Printf("dev_collect: %s polling got %d bytes, no status found", repo, len(stdout))
			}
		}
	}

	// still no status block — kernel builds status from system observations
	status = map[string]string{}

	if len(files) > 0 {
		status["files_written"] = strings.Join(files, ",")
		// child wrote files but no status → it did work, compile state unknown
		hasCode := false
		for _, f := range files {
			if strings.HasSuffix(f, ".go") || strings.HasSuffix(f, ".c") ||
				strings.HasSuffix(f, ".cu") || strings.HasSuffix(f, ".py") {
				hasCode = true
				break
			}
		}
		if hasCode {
			status["compile"] = "unknown"
			status["compile_error"] = "child wrote code files but did not report compile status"
		} else {
			status["compile"] = "fail"
			status["compile_error"] = "child wrote files but no code: " + strings.Join(files, ",")
		}
	} else if len(stdout) < 100 {
		// child produced almost nothing — likely API failure, not a real attempt
		status["compile"] = "no_response"
		status["compile_error"] = fmt.Sprintf("child stdout=%d bytes, no files written", len(stdout))
	} else if strings.Contains(stdout, "[API Error]") ||
		strings.Contains(stdout, "http request:") ||
		strings.Contains(stdout, "TLS handshake timeout") ||
		strings.Contains(stdout, "net/http:") {
		// child stdout is an API/network error message, not a real attempt
		status["compile"] = "no_response"
		status["compile_error"] = fmt.Sprintf("child returned API/network error (%d bytes)", len(stdout))
	} else {
		status["compile"] = "fail"
		status["compile_error"] = "no status block, no code files"
	}

	log.Printf("dev_collect: %s built status from system: compile=%s files=%d",
		repo, status["compile"], len(files))
	return stdout, status
}

// dev_child_files: get files written by child from shadow DB
func dev_child_files(childPid int64) []string {
	convID := ""
	if m := getMeta(childPid); m != nil {
		convID = m.ConvID
	}
	if convID == "" {
		return nil
	}
	return GetConvFiles(convID)
}

func pr_gpu_args(pid int64) (string, string) {
	gpuAll := "0,1,2,3"
	gpuUsable := "0,1,2,3"
	prMu.Lock()
	if pr := prTable[pid]; pr != nil {
		if pr.GPUAll != "" {
			gpuAll = pr.GPUAll
		}
		if pr.GPUUsable != "" {
			gpuUsable = pr.GPUUsable
		}
	}
	prMu.Unlock()
	return gpuAll, gpuUsable
}

func dev_update_gpu(pid int64, status map[string]string, gpuAll, gpuUsable string) string {
	if pairsOK, ok := status["gpu_pairs_ok"]; ok && pairsOK != "" {
		usable := computeUsableGPUs(gpuAll, pairsOK, status["gpu_pairs_fail"])
		prMu.Lock()
		if pr := prTable[pid]; pr != nil {
			pr.GPUPairsOK = pairsOK
			pr.GPUUsable = usable
		}
		prMu.Unlock()
		log.Printf("dev_update_gpu: kernel computed gpu_usable=%s from pairs_ok=%s", usable, pairsOK)
		return usable
	}
	return gpuUsable
}

func parse_last_status(text string) map[string]string {
	blocks := parse_kv_blocks(text)
	for i := len(blocks) - 1; i >= 0; i-- {
		if _, ok := blocks[i]["compile"]; ok {
			return blocks[i]
		}
	}
	return nil
}

func computeUsableGPUs(gpuAll, pairsOK, pairsFail string) string {
	allIDs := strings.Split(gpuAll, ",")

	adj := make(map[string]map[string]bool)
	for _, id := range allIDs {
		adj[strings.TrimSpace(id)] = make(map[string]bool)
	}
	for _, pair := range strings.Split(pairsOK, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), "-", 2)
		if len(parts) == 2 {
			a, b := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			adj[a][b] = true
			adj[b][a] = true
		}
	}

	var best []string
	n := len(allIDs)
	for mask := 1; mask < (1 << n); mask++ {
		var subset []string
		for i := 0; i < n; i++ {
			if mask&(1<<i) != 0 {
				subset = append(subset, strings.TrimSpace(allIDs[i]))
			}
		}
		ok := true
		for i := 0; i < len(subset) && ok; i++ {
			for j := i + 1; j < len(subset) && ok; j++ {
				if !adj[subset[i]][subset[j]] {
					ok = false
				}
			}
		}
		if ok && len(subset) > len(best) {
			best = subset
		}
	}

	if len(best) == 0 {
		if len(allIDs) > 0 {
			return strings.TrimSpace(allIDs[0])
		}
		return "0"
	}
	return strings.Join(best, ",")
}

const dev_draft_instruction = `

[GPU CONFIGURATION]
all GPUs on this machine: %s
usable GPUs (from prior P2P test): %s
use CUDA_VISIBLE_DEVICES=%s for all GPU operations.

[WHAT YOU HAVE]
you have the THEORY above: LAYER 1 (math specs) and LAYER 2 (form templates).
you do NOT have the original source code. this is intentional.

LAYER 2 form templates use placeholder identifiers:
  prjname = project namespace (fill with YOUR project's namespace)
  adj = adjective prefix (choose one that fits YOUR component)
  Obj = object class (name it for YOUR domain)
  typenameType = type alias (define YOUR own)
  objtype = member handle (YOUR internal handle type)
  argname = argument names (YOUR naming convention)

you are creating NEW code, not copying. the form template is a quotient —
all implementations that share the same structural depth are equivalent.
you choose the representative: SetPosition can become WriteDefinition,
TransferEngine can become CollectiveRunner, as long as the math in
LAYER 1 is satisfied and the structural depth of LAYER 2 is preserved.

[OUTPUT FORMAT]
write code, compile it, test it on the real machine. then output a status block:

compile=pass|fail
compile_error=<one line if fail>
needs_source=true|false
needs_source_files=<comma-separated filenames if needs_source=true>
needs_source_reason=<one line: what you need to see to proceed>
template_fill=<your choices: adjObj→YourName, prjname→yourns, typenameType→YourType>
gpu_pairs_ok=<pairs that pass P2P, e.g. 0-1,1-2>
gpu_pairs_fail=<pairs that fail>
gpu_test=pass|fail
smoke=pass|fail
smoke_error=<one line if fail>
files_written=<comma-separated paths>

if compile fails, fix it using the theory + compile error message + your existing code.
you will NEVER receive the reference source code. the theory is your only spec.

GIT: you may git add, commit, and push to a TEMP branch only.
use branch name: einsteinOS/auto-{your conv id or any unique suffix}
do NOT push to main or the target branch directly.
the kernel will open a PR from your temp branch after verification.

for git clone/push with auth, use the token file:
  TOKEN=$(cat /tmp/.gh_token 2>/dev/null)
  git clone https://${TOKEN}@github.com/OWNER/REPO.git
  git push https://${TOKEN}@github.com/OWNER/REPO.git BRANCH

VERIFICATION STEPS:
1. fill the form template with your project's identifiers
2. write the code to the dev repo
3. compile — if fail after 2 tries, set needs_source=true with the files you need
4. if compile passes, test GPU P2P on all pairs
5. smoke test end to end
`

const dev_incremental_instruction = `

[INCREMENTAL BUILD — unit %[5]d of %[6]d]
you are building ONE unit of a multi-unit project. each unit is a NEW conversation.
the shared branch is: %[1]s

[GPU CONFIGURATION]
all GPUs on this machine: %[2]s
usable GPUs: %[3]s
CUDA_VISIBLE_DEVICES=%[4]s

[WHAT YOU HAVE]
- the THEORY for your unit above (LAYER 1 math + LAYER 2 form templates)
- if unit > 1: prior code already on the branch — git checkout and READ it first
- you do NOT have the original source code. this is intentional.

[CRITICAL — DO NOT]
- do NOT clone, fetch, or read ANY repository other than the dev repo
- do NOT search for or access the reference source code in any way
- do NOT use the reference project's namespace, directory structure, or file names
- the theory is ABSTRACT — it describes WHAT to build, not HOW the reference did it
- if the theory mentions paths from the reference, IGNORE those paths
- create YOUR OWN project layout appropriate for YOUR dev repo

[YOUR PROJECT LAYOUT]
use the dev repo's existing structure. look at what prior units created:
  include/<project>/  — headers
  src/<project>/      — implementations
  tests/              — test files
if unit 1 and no structure exists yet, create one that fits YOUR project name.

[RULES]
1. if this is unit 1: clone the dev repo, create branch %[1]s, start coding
2. if this is unit > 1: git fetch && git checkout %[1]s, read existing code, then ADD your unit
3. do NOT rewrite code from prior units unless your theory REQUIRES changing it
4. compile the WHOLE project after adding your code
5. push to branch %[1]s (NOT main)

for git auth: TOKEN=$(cat /tmp/.gh_token 2>/dev/null)
  git push https://${TOKEN}@github.com/OWNER/REPO.git %[1]s

[THEORY → CODE]
LAYER 2 form templates use placeholder identifiers:
  prjname, adj, Obj, typenameType, objtype, argname
fill them with YOUR project's names. you are creating NEW code, not copying.
the math in LAYER 1 defines WHAT the code must do. HOW you implement it
(data structures, control flow, error handling) is YOUR choice for YOUR hardware.

[OUTPUT FORMAT]
write code, compile, test. then output:

compile=pass|fail
compile_error=<one line if fail>
smoke=pass|fail
smoke_error=<one line if fail>
files_written=<comma-separated paths you added or changed>
unit=%[5]d
`

const dev_verify_instruction = `

[GPU CONFIGURATION]
all GPUs on this machine: %s
usable GPUs (from prior P2P test): %s
use CUDA_VISIBLE_DEVICES=%s for all GPU operations.

[OUTPUT FORMAT]
write code, compile it, test it on the real machine. then output a status block:

compile=pass|fail
compile_error=<one line if fail>
gpu_pairs_ok=<pairs that pass P2P, e.g. 0-1,1-2,2-3>
gpu_pairs_fail=<pairs that fail P2P, e.g. 0-3,1-3>
gpu_test=pass|fail
gpu_test_detail=<what you tested: p2p, allreduce, memory>
smoke=pass|fail
smoke_error=<one line if fail>
files_written=<comma-separated paths of code you wrote>

VERIFICATION STEPS (do all of these before outputting the status block):
1. write the code to the dev repo
2. compile — if fail, fix and recompile until pass or 3 tries
3. test GPU-to-GPU P2P: for EVERY pair of GPUs in {%s},
   try cudaMemcpyPeer or equivalent transfer. report each pair
   as pass or fail in gpu_pairs_ok / gpu_pairs_fail.
   THIS IS NOT OPTIONAL. you must test every pair.
4. if a model exists at /tmp/model, check its size vs available memory.
   if it won't fit with the usable GPU count, report smoke=fail.
5. smoke test: run the code with a minimal workload end to end
   using only the GPUs that passed P2P.
   if it hangs or crashes, read the error, fix, retry.

do NOT output the status block until you have actually run steps 1-5.
the tool results are from the REAL machine.
`

func PRRunAll(pid int64) []string {
	var results []string
	for {
		next := PRSchedule()
		if next != pid {
			break
		}
		result, err := PRRunNext(pid)
		if err != nil {
			results = append(results, fmt.Sprintf("error: %v", err))
			break
		}
		results = append(results, result)
		if PRCurrentStep(pid) == nil {
			break
		}
	}
	return results
}

func PRStepLog(pid int64) string {
	prStepsMu.Lock()
	defer prStepsMu.Unlock()

	steps := prSteps[pid]
	cursor := prCursor[pid]

	out := fmt.Sprintf("  %-4s %-14s %-30s %-8s %s\n",
		"#", "KIND", "TARGET", "TIME", "STATUS")
	for i, s := range steps {
		kind := step_name(s.Kind)
		target := s.Owner + "/" + s.Repo
		if s.SHA != "" {
			target += " " + short_sha(s.SHA)
		}
		if len(target) > 30 {
			target = target[:30]
		}

		dur := ""
		status := "pending"
		if i < cursor {
			dur = s.Finished.Sub(s.Started).Truncate(time.Millisecond).String()
			if s.Err != nil {
				status = "FAIL"
			} else {
				status = "done"
			}
		} else if i == cursor {
			status = "-> next"
		}

		out += fmt.Sprintf("  %-4d %-14s %-30s %-8s %s\n",
			i, kind, target, dur, status)
	}
	return out
}

func step_name(k StepKind) string {
	switch k {
	case STEP_REF_CLONE:
		return "ref_clone"
	case STEP_REF_LIST:
		return "ref_list"
	case STEP_REF_READ:
		return "ref_read"
	case STEP_REF_FILE:
		return "ref_file"
	case STEP_REF_TREE:
		return "ref_tree"
	case STEP_REF_FIRST:
		return "ref_first"
	case STEP_REF_THEORIZE:
		return "ref_theorize"
	case STEP_REF_BIND:
		return "ref_bind"
	case STEP_DEV_WITH_THEORY:
		return "dev_theory"
	case STEP_DEV_FROM_THEOREMS:
		return "dev_thms"
	case STEP_DEV_FIX:
		return "dev_fix"
	case STEP_DEV_EXEC:
		return "dev_exec"
	case STEP_HW_PROBE:
		return "hw_probe"
	case STEP_REF_DIFF:
		return "ref_diff"
	case STEP_OPEN_PR:
		return "open_pr"
	default:
		return "?"
	}
}

func short_sha(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// WriteGHTokenFromTask: extract ghp_ token from text and write to /tmp/.gh_token
// so child conversations can read it for git push without token in prompt.
func WriteGHTokenFromTask(text string) { writeGHToken(text) }
func writeGHToken(text string) {
	for _, word := range strings.Fields(text) {
		if strings.HasPrefix(word, "ghp_") {
			token := strings.TrimRight(word, ".,;:!?")
			os.WriteFile("/tmp/.gh_token", []byte(token), 0600)
			log.Printf("writeGHToken: wrote %d-char token to /tmp/.gh_token", len(token))
			return
		}
	}
}

func strip_credentials(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "ghp_") ||
			strings.Contains(line, "@stu.") ||
			strings.Contains(line, "token") ||
			strings.Contains(line, "password") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func findPrevResult(pid int64, kind StepKind, owner, repo string) string {
	prStepsMu.Lock()
	defer prStepsMu.Unlock()
	for _, s := range prSteps[pid] {
		if s.Kind == kind && s.Owner == owner && s.Repo == repo && s.Result != "" {
			return s.Result
		}
	}
	return ""
}

func StepName(k StepKind) string {
	return step_name(k)
}

func pr_feedback(pid int64) string {
	prMu.Lock()
	defer prMu.Unlock()
	pr := prTable[pid]
	if pr == nil || len(pr.Feedback) == 0 {
		return ""
	}
	return strings.Join(pr.Feedback, "\n")
}

func PRFeedbackAdd(pid int64, line string) {
	prMu.Lock()
	defer prMu.Unlock()
	if pr := prTable[pid]; pr != nil {
		pr.Feedback = append(pr.Feedback, line)
	}
}

func ref_diff(pid int64, step *Step) (string, error) {
	ref_content, err := RefFileAt(step.Owner, step.Repo, step.SHA, step.Path)
	if err != nil {
		return "", fmt.Errorf("ref %s/%s@%s %s: %w",
			step.Owner, step.Repo, short_sha(step.SHA), step.Path, err)
	}

	ref_lines := strings.Count(ref_content, "\n") + 1

	var prompt strings.Builder
	prompt.WriteString(fmt.Sprintf("[REFERENCE: %s/%s commit %s — %s (%d lines)]\n",
		step.Owner, step.Repo, short_sha(step.SHA), step.Path, ref_lines))
	prompt.WriteString(ref_content)
	prompt.WriteString("\n\n")
	prompt.WriteString("read every line above. then read the corresponding file in our working directory.\n")
	prompt.WriteString("output ONLY key=value pairs, one per line. no prose, no explanation.\n")
	prompt.WriteString("required keys:\n")
	prompt.WriteString("  ref_file=<path>\n")
	prompt.WriteString("  ref_lines=<number>\n")
	prompt.WriteString("  ref_funcs=<comma-separated function/struct names>\n")
	prompt.WriteString("  ref_pattern=<one phrase: what the ref does differently>\n")
	prompt.WriteString("  our_file=<path>\n")
	prompt.WriteString("  our_lines=<number>\n")
	prompt.WriteString("  our_funcs=<comma-separated function/struct names>\n")
	prompt.WriteString("  gap=<one phrase: what ours is missing or doing wrong>\n")
	prompt.WriteString("repeat the block for each function/struct pair that differs.\n")

	child, err := Fork(prompt.String(), step.Dir)
	if err != nil {
		return "", err
	}
	Wait(child)
	step.ChildPid = child

	stdout := GetResult(child)
	blocks := parse_kv_blocks(stdout)

	for _, b := range blocks {
		line := fmt.Sprintf("- %s (%s lines, funcs: %s) pattern: %s | our %s (%s lines, funcs: %s) gap: %s",
			b["ref_file"], b["ref_lines"], b["ref_funcs"], b["ref_pattern"],
			b["our_file"], b["our_lines"], b["our_funcs"], b["gap"])
		PRFeedbackAdd(pid, line)
	}

	log.Printf("ref_diff: %s/%s %s -> %d kv blocks, %d feedback",
		step.Owner, step.Repo, step.Path,
		len(blocks), len(GetPR(pid).Feedback))

	return stdout, nil
}

func parse_kv_blocks(text string) []map[string]string {
	var blocks []map[string]string
	current := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(current) > 0 {
				blocks = append(blocks, current)
				current = map[string]string{}
			}
			continue
		}
		if idx := strings.Index(line, "="); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			current[key] = val
		}
	}
	if len(current) > 0 {
		blocks = append(blocks, current)
	}
	return blocks
}
