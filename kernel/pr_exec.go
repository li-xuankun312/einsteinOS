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
	STEP_REF_THEORIZE_ALL
	STEP_DEV_FROM_THEOREMS
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

	case STEP_REF_THEORIZE_ALL:
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
		devPrompt := fmt.Sprintf(
			"[REFERENCE THEORY from %s/%s commit %s]\n%s\n\n"+
				"[DEVELOPMENT TASK]\n%s\n\n"+
				"based on the mathematical theory above, write code that satisfies "+
				"the stated invariants and bounds. output ONLY code and brief comments.",
			step.Owner, step.Repo, short_sha(step.SHA),
			truncDiff(theory, 6000),
			task)
		childPid, e := Fork(devPrompt, ".")
		err = e
		if err == nil {
			Wait(childPid)
			step.ChildPid = childPid
			result = GetResult(childPid)
		}

	case STEP_DEV_FROM_THEOREMS:
		result, err = dev_from_theorems(pid, step)

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
	commands := []string{
		"cat /proc/cpuinfo | head -30",
		"free -h",
		"lspci | grep -i -E 'vga|3d|display|gpu' 2>/dev/null || echo 'no lspci'",
		"nvidia-smi 2>/dev/null || ixsmi 2>/dev/null || echo 'no gpu tool'",
		"uname -a",
		"cat /etc/os-release 2>/dev/null | head -5",
	}

	var results []string
	for _, cmd := range commands {
		childPid, err := ForkExec(cmd, ".")
		if err != nil {
			results = append(results, fmt.Sprintf("%s: %v", cmd, err))
			continue
		}
		Wait(childPid)
		out := GetResult(childPid)
		stderr := GetStderr(childPid)
		if out != "" {
			results = append(results, out)
		}
		if stderr != "" {
			results = append(results, stderr)
		}
	}

	hw_info := strings.Join(results, "\n---\n")
	log.Printf("hw_init: pid=%d %d bytes", pid, len(hw_info))

	prMu.Lock()
	pr := prTable[pid]
	prMu.Unlock()
	if pr != nil {
		pr.HWInfo = hw_info
	}

	return hw_info, nil
}

func ref_bind(pid int64, owner, repo string) (string, error) {
	commits, err := RefListCommits(owner, repo)
	if err != nil {
		return "", err
	}
	log.Printf("ref_bind: %s/%s %d commits", owner, repo, len(commits))

	done := 0
	for i, c := range commits {
		if theorem_get(owner+"/"+repo, c.SHA) != "" {
			done++
			continue
		}

		diff, files, e := RefReadCommit(owner, repo, c.SHA)
		if e != nil {
			log.Printf("ref_bind: skip %s: %v", c.SHA[:12], e)
			continue
		}

		ref_theorize(pid, owner, repo, c.SHA, diff, len(files), c.Message)
		done++

		if (i+1)%10 == 0 {
			log.Printf("ref_bind: %s/%s %d/%d", owner, repo, done, len(commits))
		}
	}

	count := theorem_count(owner + "/" + repo)
	result := fmt.Sprintf("%s/%s: %d commits %d theorems", owner, repo, len(commits), count)
	log.Printf("ref_bind: %s", result)
	return result, nil
}

func dev_from_theorems(pid int64, step *Step) (string, error) {
	repo := step.Owner + "/" + step.Repo
	all_theorems := theorem_get_all(repo)

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
		prompt.WriteString(truncDiff(hw_info, 2000))
		prompt.WriteString("\n\n")
	}
	prompt.WriteString(fmt.Sprintf("[THEOREMS from %s — %d entries]\n", repo, theorem_count(repo)))
	prompt.WriteString(truncDiff(all_theorems, 80000))
	prompt.WriteString("\n\n[TASK]\n")
	prompt.WriteString(task)
	prompt.WriteString("\n\nbased on the accumulated mathematical theory, write code. output ONLY code.")

	childPid, err := Fork(prompt.String(), ".")
	if err != nil {
		return "", err
	}
	Wait(childPid)
	step.ChildPid = childPid
	return GetResult(childPid), nil
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
	case STEP_REF_THEORIZE_ALL:
		return "ref_bind"
	case STEP_DEV_WITH_THEORY:
		return "dev_theory"
	case STEP_DEV_FROM_THEOREMS:
		return "dev_thms"
	case STEP_DEV_EXEC:
		return "dev_exec"
	case STEP_HW_PROBE:
		return "hw_probe"
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
