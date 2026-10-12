package kernel

import (
	"fmt"
	"os"
	"sync"
	"time"
)

type StepKind int

const (
	STEP_REF_CLONE       StepKind = 1
	STEP_REF_LIST        StepKind = 2
	STEP_REF_READ_COMMIT StepKind = 3
	STEP_REF_READ_FILE   StepKind = 4
	STEP_REF_TREE        StepKind = 5
	STEP_REF_FIRST       StepKind = 6
	STEP_DEV_EXEC        StepKind = 7
	STEP_REF_COMPARE     StepKind = 8
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
	case STEP_REF_CLONE:
		dir := fmt.Sprintf("/tmp/ref_%s_%s.git", step.Owner, step.Repo)
		_, err = RefClone(step.Owner, step.Repo, dir)
		if err == nil {
			result = fmt.Sprintf("cloned %s/%s -> %s", step.Owner, step.Repo, dir)
		}

	case STEP_REF_LIST:
		commits, e := RefListCommits(step.Owner, step.Repo)
		err = e
		if err == nil {
			step.Files = make([]string, 0, len(commits))
			for _, c := range commits {
				step.Files = append(step.Files, c.SHA+" "+c.Message)
			}
			result = fmt.Sprintf("%d commits", len(commits))
		}

	case STEP_REF_FIRST:
		commit, diff, e := RefFirstCommit(step.Owner, step.Repo)
		err = e
		if err == nil {
			step.Files = commit.Files
			result = fmt.Sprintf("%s %s (%d files)\n%s",
				commit.SHA[:12], commit.Message,
				len(commit.Files), truncDiff(diff, 4000))
		}

	case STEP_REF_READ_COMMIT:
		diff, files, e := RefReadCommit(step.Owner, step.Repo, step.SHA)
		err = e
		if err == nil {
			step.Files = files
			result = truncDiff(diff, 4000)
		}

	case STEP_REF_READ_FILE:
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

	case STEP_REF_COMPARE:
		stdin := findPrevResult(pid, STEP_REF_FIRST, step.Owner, step.Repo)
		if stdin == "" {
			err = fmt.Errorf("no ref_first for %s/%s", step.Owner, step.Repo)
			break
		}
		program, readErr := os.ReadFile(step.Path)
		if readErr != nil {
			err = fmt.Errorf("execve %s: %v", step.Path, readErr)
			break
		}
		childPid, forkErr := Fork(string(program)+"\n"+stdin, step.Dir)
		if forkErr != nil {
			err = forkErr
			break
		}
		Wait(childPid)
		result = GetResult(childPid)

	case STEP_DEV_EXEC:
		childPid, e := ForkExec(step.Command, step.Dir)
		err = e
		if err == nil {
			_, code, _ := Wait(childPid)
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
		kind := stepKindName(s.Kind)
		target := s.Owner + "/" + s.Repo
		if s.SHA != "" {
			target += " " + shortSHA(s.SHA)
		}
		if s.Path != "" {
			target += ":" + s.Path
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

func stepKindName(k StepKind) string {
	switch k {
	case STEP_REF_CLONE:
		return "ref_clone"
	case STEP_REF_LIST:
		return "ref_list"
	case STEP_REF_READ_COMMIT:
		return "ref_read"
	case STEP_REF_READ_FILE:
		return "ref_file"
	case STEP_REF_TREE:
		return "ref_tree"
	case STEP_REF_FIRST:
		return "ref_first"
	case STEP_REF_COMPARE:
		return "ref_compare"
	case STEP_DEV_EXEC:
		return "dev_exec"
	default:
		return "?"
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
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

func truncDiff(diff string, max int) string {
	if len(diff) > max {
		return diff[:max] + "\n... (truncated)"
	}
	return diff
}
