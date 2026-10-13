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
	commit, _, err := RefFirstCommit(owner, repo)
	if err != nil {
		return "", err
	}

	files, _ := RefTreeAt(owner, repo, commit.SHA)
	dir := fmt.Sprintf("/tmp/ref_%s_%s.git", owner, repo)

	var listing strings.Builder
	for _, f := range files {
		listing.WriteString(f)
		listing.WriteString("\n")
	}

	prompt := fmt.Sprintf(
		"repo %s/%s first commit %s: %d files.\n"+
			"bare repo at %s, read with: git --git-dir %s show %s:<path>\n\n"+
			"file list:\n%s\n"+
			"read ALL %d files. you decide the order and grouping.\n"+
			"if there are too many to read in one pass, use bash to read batches, "+
			"or focus on the most important directories first.\n"+
			"for each file, output key=value:\n"+
			"  file=<path>\n  lines=<number>\n  role=<one phrase>\n\n"+
			"after reading all files, output a THEORY section with pure LaTeX "+
			"mathematical formalizations for each semantic unit.\n"+
			"verify LaTeX compiles with pdflatex.",
		owner, repo, commit.SHA[:12], len(files),
		dir, dir, commit.SHA,
		listing.String(), len(files))

	log.Printf("ref_bind: %s/%s %s %d files, child decides reading strategy",
		owner, repo, commit.SHA[:12], len(files))

	child, err := Fork(prompt, ".")
	if err != nil {
		return "", err
	}
	Wait(child)

	stdout := GetResult(child)
	stderr := GetStderr(child)

	theorem_store(owner+"/"+repo, commit.SHA, stdout, child)

	log.Printf("ref_bind: %s/%s done, stdout=%d stderr=%d",
		owner, repo, len(stdout), len(stderr))
	return stdout, nil
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

	feedback := pr_feedback(pid)
	if feedback != "" {
		prompt.WriteString(fmt.Sprintf("\n\n[DEVELOPMENT DISCIPLINE from prior review]\n%s", feedback))
	}

	log.Printf("dev_from_theorems: %s %d files %d lines fed", repo, len(files), read)

	child, err := Fork(prompt.String(), ".")
	if err != nil {
		return "", err
	}
	Wait(child)
	step.ChildPid = child
	return GetResult(child), nil
}

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
