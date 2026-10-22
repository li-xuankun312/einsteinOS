package kernel

import (
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
	STEP_VLLM_UP    // child A: start vllm server, output port + pid
	STEP_VLLM_BENCH // child B: benchmark against running vllm, output tok_s
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
		childPid, e := Fork(devPrompt, ".")
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

	case STEP_VLLM_UP:
		result, err = vllm_up(pid, step)

	case STEP_VLLM_BENCH:
		result, err = vllm_bench(pid, step)

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
	// dev_from_theorems will build code, run vllm, measure tok/s.
	// if tok/s < threshold, dev_from_theorems pushes STEP_REF_BIND(start=next)
	// to come back for more theory. quantitative eval drives the loop.
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
func dev_split_theory(theory string) []string {
	lines := strings.Split(theory, "\n")

	// first pass: split into per-file sections
	type fileSection struct {
		path    string
		dir     string
		content string
	}
	var sections []fileSection
	var cur strings.Builder
	curPath := ""

	flush := func() {
		s := strings.TrimSpace(cur.String())
		if len(s) > 50 {
			dir := ""
			if idx := strings.LastIndex(curPath, "/"); idx > 0 {
				dir = curPath[:idx]
			}
			sections = append(sections, fileSection{path: curPath, dir: dir, content: s})
		}
		cur.Reset()
		curPath = ""
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// detect ## FILE: <path> header
		isHeader := false
		newPath := ""
		if strings.Contains(trimmed, "## FILE:") {
			isHeader = true
			idx := strings.Index(trimmed, "## FILE:")
			newPath = strings.TrimSpace(trimmed[idx+len("## FILE:"):])
		} else if strings.HasPrefix(trimmed, "## FILE") {
			isHeader = true
			newPath = strings.TrimSpace(trimmed[len("## FILE"):])
			newPath = strings.TrimLeft(newPath, ": ")
		}

		if isHeader {
			// clean the path: strip quotes, backticks, leading/trailing junk
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

	if len(sections) <= 1 {
		// no splitting possible
		log.Printf("dev_split_theory: %d bytes -> 1 unit (found %d ## FILE sections)", len(theory), len(sections))
		return []string{theory}
	}

	// second pass: group sections by directory.
	// filter out /home/claude paths — these are shadow exec local file paths
	// (e.g. /home/claude/strata_chunk1_theory.md), not repo source files.
	// if a shadow file contains valid ## FILE: headers inside its content,
	// those were already split into separate sections in the first pass.
	dirOrder := []string{}
	dirSections := map[string][]fileSection{}
	for _, sec := range sections {
		// clean path/dir: strip quotes, whitespace that leaked from parsing
		cleanPath := strings.Trim(sec.path, "\"'` \t")
		cleanDir := strings.Trim(sec.dir, "\"'` \t")

		// skip shadow/local paths
		if strings.HasPrefix(cleanPath, "/home/claude") || strings.HasPrefix(cleanDir, "/home/claude") ||
			strings.HasPrefix(cleanPath, "/mnt/") || strings.HasPrefix(cleanDir, "/mnt/") ||
			strings.HasPrefix(cleanPath, "/tmp/") || strings.HasPrefix(cleanDir, "/tmp/") {
			continue
		}
		// skip malformed: path contains ## FILE: (concatenated headers from ContinueConv)
		if strings.Contains(cleanPath, "## FILE:") || strings.Contains(cleanDir, "## FILE:") {
			continue
		}
		sec.path = cleanPath
		sec.dir = cleanDir
		d := sec.dir
		if d == "" {
			d = "/"
		}
		if _, ok := dirSections[d]; !ok {
			dirOrder = append(dirOrder, d)
		}
		dirSections[d] = append(dirSections[d], sec)
	}

	// build units from directory groups
	var units []string
	for _, d := range dirOrder {
		secs := dirSections[d]
		var unit strings.Builder
		unit.WriteString(fmt.Sprintf("# Directory: %s (%d files)\n\n", d, len(secs)))
		for _, sec := range secs {
			unit.WriteString(sec.content)
			unit.WriteString("\n\n")
		}
		s := strings.TrimSpace(unit.String())
		if len(s) > 100 {
			units = append(units, s)
		}
	}

	if len(units) == 0 {
		units = []string{theory}
	}

	// log the split
	log.Printf("dev_split_theory: %d bytes -> %d units (%d files, %d dirs)",
		len(theory), len(units), len(sections), len(dirOrder))
	for i, u := range units {
		firstLine := u
		if idx := strings.Index(firstLine, "\n"); idx > 0 {
			firstLine = firstLine[:idx]
		}
		log.Printf("dev_split_theory:   unit %d: %d bytes — %s", i+1, len(u), firstLine)
	}

	return units
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

	units := dev_split_theory(theory)
	log.Printf("dev_from_theorems: %s %d theory units, total %d bytes, gpu_all=%s gpu_usable=%s",
		repo, len(units), len(theory), gpuAll, gpuUsable)

	// shared branch for all incremental children
	branch := fmt.Sprintf("einsteinOS/dev-%d", pid)

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

		// accumulated context: prior theories in FULL + prior unit conversations
		// downloaded via DownloadConv. child sees everything, no truncation.
		// pattern: [code1 + T1 + T2 + ... + Ti] → codei
		if i > 0 {
			// inject prior unit work: download each prior child conversation
			// which contains create_file code, bash commands, compile results.
			for j := 0; j < i; j++ {
				rec := FindConv(repo, fmt.Sprintf("dev_unit_%d", j+1), j+1)
				if rec == nil {
					continue
				}
				downloaded, err := DownloadConv(rec.ConvID)
				if err != nil {
					log.Printf("dev_from_theorems: %s unit %d prior conv download failed: %v",
						repo, j+1, err)
					continue
				}
				p.WriteString(fmt.Sprintf("[PRIOR UNIT %d/%d — conv %s, %d bytes]\n",
					j+1, len(units), rec.ConvID[:8], len(downloaded)))
				p.WriteString(downloaded)
				p.WriteString("\n\n")
				log.Printf("dev_from_theorems: %s injected prior unit %d conv (%d bytes) into unit %d prompt",
					repo, j+1, len(downloaded), i+1)
			}

			// prior theories in full — not summaries
			p.WriteString(fmt.Sprintf("[PRIOR THEORIES 1..%d — full text]\n", i))
			for j := 0; j < i; j++ {
				p.WriteString(fmt.Sprintf("--- THEORY %d/%d ---\n", j+1, len(units)))
				p.WriteString(units[j])
				p.WriteString("\n\n")
			}
		}

		p.WriteString(fmt.Sprintf("[THEORY %d/%d — your task: implement THIS unit]\n", i+1, len(units)))
		p.WriteString(unit)
		p.WriteString("\n\n")

		if i > 0 {
			p.WriteString(fmt.Sprintf("[EXISTING CODE]\n"+
				"branch %s has code from prior units (you have their full conversations above).\n"+
				"  cd /home/claude/<repo> && git fetch origin && git checkout %s\n"+
				"do NOT rewrite existing files unless theory %d requires changing them.\n"+
				"ADD your new code, then compile the WHOLE project.\n\n",
				branch, branch, i+1))
		}

		p.WriteString(fmt.Sprintf("[TASK]\n%s\n\n", task))

		if devRepoBlock != "" {
			p.WriteString(devRepoBlock)
			p.WriteString("\n")
		}

		// use the incremental instruction (shared branch, not own branch)
		p.WriteString(fmt.Sprintf(dev_incremental_instruction, branch, gpuAll, gpuUsable, gpuUsable, i+1, len(units)))

		feedback := pr_feedback(pid)
		if feedback != "" {
			p.WriteString(fmt.Sprintf("\n\n[PRIOR FEEDBACK]\n%s", feedback))
		}

		log.Printf("dev_from_theorems: %s unit %d/%d forking (prompt %d bytes)",
			repo, i+1, len(units), p.Len())

		// retry loop: connection failures and "no context" responses get retried.
		// same pattern as ref_split: network issues are transient.
		prompt := p.String()
		for attempt := 1; attempt <= 5; attempt++ {
			child, err := Fork(prompt, ".")
			if err != nil {
				log.Printf("dev_from_theorems: %s unit %d/%d fork failed (attempt %d/5): %v",
					repo, i+1, len(units), attempt, err)
				continue
			}
			if m := getMeta(child); m != nil && m.ConvID != "" {
				RegisterConv(pid, m.ConvID, repo, fmt.Sprintf("dev_unit_%d", i+1), i+1)
			}
			Wait(child)
			step.ChildPid = child

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

		// unit success: compile status starts with "pass" (may have description suffix
		// like "pass (CPU tests + headers); pending (GPU)"). smoke may be empty if
		// no GPU available — treat empty smoke as acceptable when compile passes.
		compilePassed := strings.HasPrefix(lastStatus["compile"], "pass")
		smokeOK := lastStatus["smoke"] == "" || strings.HasPrefix(lastStatus["smoke"], "pass")
		if !compilePassed || !smokeOK {
			log.Printf("dev_from_theorems: %s unit %d/%d failed (compile=%q smoke=%q), pushing fix",
				repo, i+1, len(units), lastStatus["compile"], lastStatus["smoke"])
			dev_push_fix(pid, step.Owner, step.Repo, lastStatus, i+1)
			// remaining units become new dev_thms steps to resume later
			if i+1 < len(units) {
				log.Printf("dev_from_theorems: %s %d units remaining after fix",
					repo, len(units)-i-1)
			}
			return lastStdout, nil
		}

		log.Printf("dev_from_theorems: %s unit %d/%d pass, branch %s updated",
			repo, i+1, len(units), branch)
	}

	// all units passed → push vllm benchmark
	log.Printf("dev_from_theorems: %s all %d units pass, pushing vllm benchmark", repo, len(units))

	_, gpuUsable = pr_gpu_args(pid)
	pr_push_step(pid, &Step{
		Kind:    STEP_VLLM_UP,
		Owner:   step.Owner,
		Repo:    step.Repo,
		Command: gpuUsable,
	})
	pr_push_step(pid, &Step{
		Kind:  STEP_VLLM_BENCH,
		Owner: step.Owner,
		Repo:  step.Repo,
	})
	return lastStdout, nil
}



// vllm_up: child A starts vllm server, outputs port and pid.
// kernel polls health endpoint until ready, then advances to STEP_VLLM_BENCH.
// Step.Command = GPU list (e.g. "0,1,2,3")
func vllm_up(pid int64, step *Step) (string, error) {
	repo := step.Owner + "/" + step.Repo
	gpus := step.Command
	if gpus == "" {
		gpus = "0"
	}

	prompt := fmt.Sprintf(
		"[TASK] start vllm inference server for benchmarking.\n\n"+
			"CUDA_VISIBLE_DEVICES=%s\n"+
			"find the model in the dev repo and start vllm:\n"+
			"  python3 -m vllm.entrypoints.openai.api_server --model <path> --port 8919 --trust-remote-code &\n"+
			"then wait for it to be ready:\n"+
			"  for i in $(seq 1 60); do curl -s http://localhost:8919/health && break; sleep 5; done\n\n"+
			"[OUTPUT FORMAT]\n"+
			"vllm_status=ready|fail\n"+
			"vllm_port=8919\n"+
			"vllm_pid=<pid of the server process>\n"+
			"vllm_error=<one line if fail>\n"+
			"model_path=<path to the model used>\n\n"+
			"do NOT exit the conversation after starting — the server must stay alive.\n"+
			"after confirming health check passes, output the status block and stop.\n",
		gpus,
	)

	child, err := Fork(prompt, ".")
	if err != nil {
		return "", fmt.Errorf("vllm_up: fork failed: %w", err)
	}
	RegisterConv(pid, "", repo, "vllm_server", 0)
	if m := getMeta(child); m != nil && m.ConvID != "" {
		RegisterConv(pid, m.ConvID, repo, "vllm_server", 0)
	}
	Wait(child)
	step.ChildPid = child

	stdout := GetResult(child)
	status := parse_last_status(stdout)
	if status == nil || status["vllm_status"] != "ready" {
		errMsg := "unknown"
		if status != nil {
			errMsg = status["vllm_error"]
		}
		log.Printf("vllm_up: %s failed: %s", repo, errMsg)
		return stdout, fmt.Errorf("vllm_up: server not ready: %s", errMsg)
	}

	// store port for bench step
	step.Result = status["vllm_port"]
	log.Printf("vllm_up: %s ready on port %s", repo, status["vllm_port"])
	return stdout, nil
}

// vllm_bench: child B benchmarks against running vllm server.
// measures tok/s. if < 40, pushes more ref_bind + dev_from_theorems.
func vllm_bench(pid int64, step *Step) (string, error) {
	repo := step.Owner + "/" + step.Repo

	// find vllm port from previous step result
	port := "8919"
	prStepsMu.Lock()
	for _, s := range prSteps[pid] {
		if s.Kind == STEP_VLLM_UP && s.Owner == step.Owner && s.Repo == step.Repo && s.Result != "" {
			port = s.Result
		}
	}
	prStepsMu.Unlock()

	prompt := fmt.Sprintf(
		"[TASK] benchmark the running vllm server and measure tokens per second.\n\n"+
			"the server is at http://localhost:%s\n\n"+
			"run a benchmark:\n"+
			"  curl -s http://localhost:%s/v1/completions -H 'Content-Type: application/json' \\\n"+
			"    -d '{\"model\":\"default\",\"prompt\":\"Hello world\",\"max_tokens\":128}'\n\n"+
			"or use vllm's benchmark script if available:\n"+
			"  python3 -m vllm.entrypoints.openai.run_benchmark ... \n\n"+
			"measure output tokens / elapsed seconds. run at least 3 requests, take the average.\n\n"+
			"[OUTPUT FORMAT]\n"+
			"tok_s=<average tokens per second, number only>\n"+
			"requests=<number of benchmark requests run>\n"+
			"tok_s_error=<one line if measurement failed>\n",
		port, port,
	)

	child, err := Fork(prompt, ".")
	if err != nil {
		return "", fmt.Errorf("vllm_bench: fork failed: %w", err)
	}
	if m := getMeta(child); m != nil && m.ConvID != "" {
		RegisterConv(pid, m.ConvID, repo, "vllm_bench", 0)
	}
	Wait(child)
	step.ChildPid = child

	stdout := GetResult(child)
	status := parse_last_status(stdout)

	tokS := 0.0
	if status != nil {
		fmt.Sscanf(status["tok_s"], "%f", &tokS)
	}

	if tokS >= 40.0 {
		log.Printf("vllm_bench: %s PASS tok/s=%.1f ≥ 40, pushing open_pr", repo, tokS)
		PRFeedbackAdd(pid, fmt.Sprintf("- vllm_bench: tok/s=%.1f PASS", tokS))
		pr_push_step(pid, &Step{
			Kind:    STEP_OPEN_PR,
			Owner:   step.Owner,
			Repo:    step.Repo,
			Command: fmt.Sprintf("tok_s=%.1f", tokS),
		})
		return stdout, nil
	}

	// tok/s below threshold — need more theory
	commits, _ := RefListCommits(step.Owner, step.Repo)
	nextIdx := ref_bind_cursor(pid, step.Owner, step.Repo) + 1
	remaining := len(commits) - nextIdx

	if remaining <= 0 {
		log.Printf("vllm_bench: %s tok/s=%.1f < 40 but no more commits, accepting", repo, tokS)
		PRFeedbackAdd(pid, fmt.Sprintf("- vllm_bench: tok/s=%.1f < 40, no more commits", tokS))
		return stdout, nil
	}

	if tokS == 0 {
		log.Printf("vllm_bench: %s tok/s not measured, accepting", repo)
		PRFeedbackAdd(pid, fmt.Sprintf("- vllm_bench: tok/s not measured"))
		return stdout, nil
	}

	log.Printf("vllm_bench: %s tok/s=%.1f < 40, pushing ref_bind(start=%d) for more theory (%d remaining)",
		repo, tokS, nextIdx, remaining)
	PRFeedbackAdd(pid, fmt.Sprintf("- vllm_bench: tok/s=%.1f < 40, looping back for more theory", tokS))

	pr_push_step(pid, &Step{
		Kind:    STEP_REF_BIND,
		Owner:   step.Owner,
		Repo:    step.Repo,
		Command: fmt.Sprintf("start=%d", nextIdx),
	})
	pr_push_step(pid, &Step{
		Kind:  STEP_DEV_FROM_THEOREMS,
		Owner: step.Owner,
		Repo:  step.Repo,
	})
	return stdout, nil
}

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

	verifyChild, err := Fork(verifyPrompt, ".")
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

	child, err := Fork(prompt.String(), ".")
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
func dev_push_fix(pid int64, owner, repo string, status map[string]string, attempt int) {
	if attempt > 3 {
		log.Printf("dev_push_fix: %s/%s max attempts reached", owner, repo)
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

	// try text-based status first (cheapest)
	status := parse_last_status(stdout)
	if status != nil {
		log.Printf("dev_collect: %s status from text: compile=%s", repo, status["compile"])
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
					staleCount++
					log.Printf("dev_collect: %s poll %d: download error (stale %d/5)", repo, poll+1, staleCount)
					continue
				}
				log.Printf("dev_collect: %s poll %d: %d bytes (prev %d, max %d, stale %d/5)",
					repo, poll+1, curSize, prevSize, maxSeen, staleCount)

				if curSize == 0 {
					if maxSeen <= 500 {
						log.Printf("dev_collect: %s poll %d: 0 bytes, conversation dead", repo, poll+1)
						break
					}
					staleCount++
					continue
				}
				if curSize > maxSeen {
					maxSeen = curSize
					staleCount = 0
				} else {
					staleCount++
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
tok_s=<number: tokens/second from vllm benchmark, 0 if not measured>
tok_s_error=<one line if measurement failed>
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
	case STEP_VLLM_UP:
		return "vllm_up"
	case STEP_VLLM_BENCH:
		return "vllm_bench"
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
