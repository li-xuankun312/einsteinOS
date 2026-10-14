package kernel

import (
	"fmt"
	"log"
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
	STEP_REF_DIFF
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
		result, err = ref_bind(pid, step.Owner, step.Repo)

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

	child, err := Fork(prompt, ".")
	if err != nil {
		return "", err
	}
	log.Printf("hw_init: pid=%d child=%d", pid, child)
	Wait(child)

	hw_info := GetResult(child)
	hw_tools := GetStderr(child)

	prMu.Lock()
	if pr := prTable[pid]; pr != nil {
		pr.HWInfo = hw_info + "\n" + hw_tools
	}
	prMu.Unlock()

	log.Printf("hw_init: pid=%d stdout=%d stderr=%d", pid, len(hw_info), len(hw_tools))
	return hw_info, nil
}

func ref_bind(pid int64, owner, repo string) (string, error) {
	commits, err := RefListCommits(owner, repo)
	if err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return "", fmt.Errorf("ref_bind: %s/%s no commits", owner, repo)
	}

	task := ref_theorize_task(pid)

	var theoryCommit *RefCommit
	var theoryFiles []string
	for i := 0; i < len(commits) && i < 20; i++ {
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
			theoryFiles = files
			break
		}
	}

	if theoryCommit == nil {
		theoryCommit = &commits[0]
		theoryFiles, _ = RefTreeAt(owner, repo, theoryCommit.SHA)
		log.Printf("ref_bind: %s/%s no source-heavy commit in first 20, using first",
			owner, repo)
	}

	log.Printf("ref_bind: %s/%s %d commits, theorize=%s %d files",
		owner, repo, len(commits), short_sha(theoryCommit.SHA), len(theoryFiles))

	if len(theoryFiles) > SPLIT_CHUNK {
		ref_split(pid, owner, repo, theoryCommit.SHA, theoryFiles, task, 0)
	} else {
		ref_theorize(pid, owner, repo, theoryCommit.SHA, "", len(theoryFiles), theoryCommit.Message)
	}

	var diffLog strings.Builder
	for i := 1; i < len(commits); i++ {
		c := commits[i]
		_, changed, _ := RefReadCommit(owner, repo, c.SHA)
		if len(changed) == 0 {
			continue
		}
		diffLog.WriteString(fmt.Sprintf("%s %d files: %s\n",
			short_sha(c.SHA), len(changed), c.Message))
		if (i+1)%500 == 0 {
			log.Printf("ref_bind: %s/%s scanned %d/%d commits",
				owner, repo, i+1, len(commits))
		}
	}
	if diffLog.Len() > 0 {
		theorem_store(owner+"/"+repo, "changelog",
			fmt.Sprintf("[%d subsequent commits]\n%s", len(commits)-1, diffLog.String()), 0)
	}

	count := theorem_count(owner + "/" + repo)
	result := fmt.Sprintf("%s/%s: %d commits %d theorems", owner, repo, len(commits), count)
	log.Printf("ref_bind: %s", result)
	return result, nil
}

func dev_from_theorems(pid int64, step *Step) (string, error) {
	repo := step.Owner + "/" + step.Repo

	commit, _, err := RefFirstCommit(step.Owner, step.Repo)
	if err != nil {
		return "", err
	}

	files, _ := RefTreeAt(step.Owner, step.Repo, commit.SHA)
	var src strings.Builder
	read := 0
	for _, f := range files {
		if isBinaryFile(f) {
			continue
		}
		content, e := RefFileAt(step.Owner, step.Repo, commit.SHA, f)
		if e != nil {
			continue
		}
		lines := strings.Count(content, "\n") + 1
		src.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", f, lines))
		src.WriteString(content)
		src.WriteString("\n\n")
		read += lines
	}

	theory := theorem_get(repo, "")

	pr := GetPR(pid)
	task := ""
	hw_info := ""
	if pr != nil {
		task = strip_credentials(pr.Title)
		hw_info = pr.HWInfo
	}

	var prompt strings.Builder
	if hw_info != "" {
		prompt.WriteString("[HARDWARE]\n")
		prompt.WriteString(hw_info)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString(fmt.Sprintf("[REFERENCE SOURCE from %s first commit — %d files, %d lines, read in full]\n", repo, len(files), read))
	prompt.WriteString(src.String())
	if theory != "" {
		prompt.WriteString(fmt.Sprintf("\n[THEORY extracted from above]\n%s\n", theory))
	}
	prompt.WriteString("\n[TASK]\n")
	prompt.WriteString(task)

	prompt.WriteString(dev_verify_instruction)

	feedback := pr_feedback(pid)
	if feedback != "" {
		prompt.WriteString(fmt.Sprintf("\n\n[PRIOR FEEDBACK]\n%s", feedback))
	}

	log.Printf("dev_from_theorems: %s %d files %d lines fed", repo, len(files), read)

	var lastResult string
	for attempt := 0; attempt < 3; attempt++ {
		p := prompt.String()
		if attempt > 0 {
			p += fmt.Sprintf("\n\n[RETRY %d/3 — previous attempt failed]\n%s\nfix the issues above and re-verify.", attempt, lastResult)
		}

		child, err := Fork(p, ".")
		if err != nil {
			return lastResult, err
		}
		Wait(child)
		step.ChildPid = child

		stdout := GetResult(child)
		blocks := parse_kv_blocks(stdout)

		var status map[string]string
		for _, b := range blocks {
			if _, ok := b["compile"]; ok {
				status = b
			}
		}

		if status != nil {
			log.Printf("dev_from_theorems: attempt %d — compile=%s gpu_test=%s smoke=%s",
				attempt+1, status["compile"], status["gpu_test"], status["smoke"])

			for k, v := range status {
				PRFeedbackAdd(pid, fmt.Sprintf("- dev[%s]: %s=%s", repo, k, v))
			}

			if status["compile"] == "pass" && status["smoke"] == "pass" {
				log.Printf("dev_from_theorems: %s verified on attempt %d", repo, attempt+1)
				return stdout, nil
			}
		} else {
			log.Printf("dev_from_theorems: attempt %d — no status block in output", attempt+1)
		}

		lastResult = stdout
	}

	log.Printf("dev_from_theorems: %s failed after 3 attempts", repo)
	return lastResult, nil
}

const dev_verify_instruction = `

[OUTPUT FORMAT]
write code, compile it, test it on the real machine. then output a status block:

compile=pass|fail
compile_error=<one line if fail>
gpu_visible=<which GPUs work, e.g. 0,1,2,3 or 1,2>
gpu_test=pass|fail
gpu_test_detail=<what you tested: p2p, allreduce, memory>
smoke=pass|fail
smoke_error=<one line if fail>
files_written=<comma-separated paths of code you wrote>

VERIFICATION STEPS (do all of these before outputting the status block):
1. write the code to the dev repo
2. compile — if fail, fix and recompile until pass or 3 tries
3. run ixsmi / gpu topology check — determine which GPUs are reachable
   if some GPUs have no P2P path, only use the working subset
4. test GPU-to-GPU: cudaMemcpyPeer or equivalent P2P transfer between
   each pair of usable GPUs. this is not optional.
5. if a model exists at /tmp/model, check size vs available memory.
   if it won't fit, adjust tp or exclude GPUs to avoid OOM.
6. smoke test: run the code with a minimal workload end to end.
   if it hangs or crashes, read the error, fix, retry.

do NOT output the status block until you have actually run steps 1-6.
the tool results are from the REAL machine with 4x BI-V100.
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
	case STEP_DEV_EXEC:
		return "dev_exec"
	case STEP_HW_PROBE:
		return "hw_probe"
	case STEP_REF_DIFF:
		return "ref_diff"
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
