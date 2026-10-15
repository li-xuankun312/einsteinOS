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
	commits, err := RefListCommits(owner, repo)
	if err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return "", fmt.Errorf("ref_bind: %s/%s no commits", owner, repo)
	}

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

	ref_theorize(pid, owner, repo, theoryCommit.SHA, "", len(theoryFiles), theoryCommit.Message)

	log.Printf("ref_bind: %s/%s done, %d subsequent commits in git (not scanned)",
		owner, repo, len(commits)-1)

	count := theorem_count(owner + "/" + repo)
	result := fmt.Sprintf("%s/%s: %d commits %d theorems", owner, repo, len(commits), count)
	log.Printf("ref_bind: %s", result)
	return result, nil
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
	gpuAll := "0,1,2,3"
	gpuUsable := "0,1,2,3"
	if pr != nil {
		task = strip_credentials(pr.Title)
		hw_info = pr.HWInfo
		if pr.GPUAll != "" {
			gpuAll = pr.GPUAll
		}
		if pr.GPUUsable != "" {
			gpuUsable = pr.GPUUsable
		}
	}

	log.Printf("dev_from_theorems: %s phase 1 (theory only, no source), gpu_all=%s gpu_usable=%s",
		repo, gpuAll, gpuUsable)

	var p1 strings.Builder
	if hw_info != "" {
		p1.WriteString("[HARDWARE]\n")
		p1.WriteString(hw_info)
		p1.WriteString("\n\n")
	}
	p1.WriteString(fmt.Sprintf("[THEORY from %s — LAYER 1 (math) + LAYER 2 (form template)]\n", repo))
	p1.WriteString(theory)
	p1.WriteString("\n\n[TASK]\n")
	p1.WriteString(task)

	devRepos := GetDevRepos()
	if len(devRepos) > 0 {
		p1.WriteString("\n\n[DEV REPO]\n")
		p1.WriteString("the dev repo is already cloned on the local machine. read it with bash_tool:\n")
		for _, dr := range devRepos {
			p1.WriteString(fmt.Sprintf("  repo: %s → ls /home/claude/%s/\n", dr, strings.Split(dr, "/")[1]))
		}
		p1.WriteString("key files to read first:\n")
		p1.WriteString("  cat patch_ops.sh        — env vars for GPU/vllm runtime\n")
		p1.WriteString("  cat Makefile             — build system\n")
		p1.WriteString("  ls src/ include/         — existing code structure\n")
		p1.WriteString("  cat *.sh                 — deployment scripts\n")
		p1.WriteString("your code must work with the existing env vars and build system.\n")
		p1.WriteString("write code INTO the dev repo, not /home/claude/.\n")
	}

	p1.WriteString(fmt.Sprintf(dev_draft_instruction, gpuAll, gpuUsable, gpuUsable))

	feedback := pr_feedback(pid)
	if feedback != "" {
		p1.WriteString(fmt.Sprintf("\n\n[PRIOR FEEDBACK]\n%s", feedback))
	}

	child1, err := Fork(p1.String(), ".")
	if err != nil {
		return "", err
	}
	if m := getMeta(child1); m != nil && m.ConvID != "" {
		RegisterConv(pid, m.ConvID, repo, "dev_draft", 1)
	}
	Wait(child1)
	step.ChildPid = child1

	stdout1 := GetResult(child1)
	status1 := parse_last_status(stdout1)

	if status1 != nil {
		log.Printf("dev_from_theorems: phase 1 — compile=%s needs_source=%s template_fill=%s",
			status1["compile"], status1["needs_source"], status1["template_fill"])

		if status1["template_fill"] != "" {
			PRFeedbackAdd(pid, fmt.Sprintf("- dev[%s]: template_fill=%s", repo, status1["template_fill"]))
		}

		needsSource := status1["needs_source"] == "true"
		compileFail := status1["compile"] == "fail"
		if needsSource || compileFail {
			return dev_phase2_selective_source(pid, step, repo, status1, gpuAll, gpuUsable, stdout1), nil
		}

		gpuUsable = dev_update_gpu(pid, status1, gpuAll, gpuUsable)
		for k, v := range status1 {
			PRFeedbackAdd(pid, fmt.Sprintf("- dev[%s]: %s=%s", repo, k, v))
		}
		if status1["compile"] == "pass" && status1["smoke"] == "pass" {
			log.Printf("dev_from_theorems: %s verified in phase 1", repo)
			return stdout1, nil
		}
		return dev_phase3_verify(pid, step, repo, gpuAll, gpuUsable, stdout1), nil
	}

	log.Printf("dev_from_theorems: phase 1 — no status block, falling through to phase 2")
	return dev_phase2_selective_source(pid, step, repo, nil, gpuAll, gpuUsable, stdout1), nil
}

func dev_phase2_selective_source(pid int64, step *Step, repo string,
	phase1Status map[string]string, gpuAll, gpuUsable, phase1Output string) string {

	commit, _, err := RefFirstCommit(step.Owner, step.Repo)
	if err != nil {
		log.Printf("dev_phase2: %s RefFirstCommit: %v", repo, err)
		return phase1Output
	}

	requestedFiles := ""
	if phase1Status != nil {
		requestedFiles = phase1Status["needs_source_files"]
	}

	var src strings.Builder
	files, _ := RefTreeAt(step.Owner, step.Repo, commit.SHA)
	read := 0

	if requestedFiles != "" {
		wanted := make(map[string]bool)
		for _, f := range strings.Split(requestedFiles, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				wanted[f] = true
			}
		}
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
		log.Printf("dev_phase2: %s kernel provided %d requested files (%d lines)",
			repo, len(wanted), read)
	} else {
		src.WriteString(fmt.Sprintf("[FILE LISTING — %d files in %s first commit]\n", len(files), repo))
		for _, f := range files {
			src.WriteString(f + "\n")
		}
		src.WriteString("\n")
		given := 0
		for _, f := range files {
			if given >= 5 {
				break
			}
			if isBinaryFile(f) {
				continue
			}
			if strings.HasSuffix(f, ".h") || strings.Contains(f, "engine") ||
				strings.Contains(f, "transport") || strings.Contains(f, "main") {
				content, e := RefFileAt(step.Owner, step.Repo, commit.SHA, f)
				if e != nil {
					continue
				}
				lines := strings.Count(content, "\n") + 1
				src.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", f, lines))
				src.WriteString(content)
				src.WriteString("\n\n")
				read += lines
				given++
			}
		}
		log.Printf("dev_phase2: %s kernel provided file list + %d key files (%d lines)",
			repo, given, read)
	}

	pr := GetPR(pid)
	task := ""
	hw_info := ""
	if pr != nil {
		task = strip_credentials(pr.Title)
		hw_info = pr.HWInfo
	}

	var p2 strings.Builder
	if hw_info != "" {
		p2.WriteString("[HARDWARE]\n")
		p2.WriteString(hw_info)
		p2.WriteString("\n\n")
	}
	theory := theorem_get(repo, "")
	if theory != "" {
		p2.WriteString(fmt.Sprintf("[THEORY from %s]\n%s\n\n", repo, theory))
	}
	p2.WriteString(fmt.Sprintf("[SELECTIVE SOURCE from %s — kernel-provided]\n", repo))
	p2.WriteString(src.String())
	p2.WriteString("\n[TASK]\n")
	p2.WriteString(task)
	if phase1Output != "" {
		p2.WriteString(fmt.Sprintf("\n\n[PRIOR ATTEMPT — fix these issues]\n%s", truncDiff(phase1Output, 3000)))
	}
	p2.WriteString(fmt.Sprintf(dev_verify_instruction, gpuAll, gpuUsable, gpuUsable, gpuAll))

	child2, err := Fork(p2.String(), ".")
	if err != nil {
		return phase1Output
	}
	if m := getMeta(child2); m != nil && m.ConvID != "" {
		RegisterConv(pid, m.ConvID, repo, "dev_fix", 2)
	}
	Wait(child2)
	step.ChildPid = child2

	stdout2 := GetResult(child2)
	status2 := parse_last_status(stdout2)

	if status2 != nil {
		gpuUsable = dev_update_gpu(pid, status2, gpuAll, gpuUsable)
		for k, v := range status2 {
			PRFeedbackAdd(pid, fmt.Sprintf("- dev[%s]: %s=%s", repo, k, v))
		}
		if status2["compile"] == "pass" && status2["smoke"] == "pass" {
			log.Printf("dev_from_theorems: %s verified in phase 2", repo)
			return stdout2
		}
	}

	return dev_phase3_verify(pid, step, repo, gpuAll, gpuUsable, stdout2)
}

func dev_phase3_verify(pid int64, step *Step, repo, gpuAll, gpuUsable, prevOutput string) string {
	prMu.Lock()
	if pr := prTable[pid]; pr != nil && pr.GPUUsable != "" {
		gpuUsable = pr.GPUUsable
	}
	prMu.Unlock()

	pr := GetPR(pid)
	hw_info := ""
	task := ""
	if pr != nil {
		hw_info = pr.HWInfo
		task = strip_credentials(pr.Title)
	}

	var p3 strings.Builder
	if hw_info != "" {
		p3.WriteString("[HARDWARE]\n")
		p3.WriteString(hw_info)
		p3.WriteString("\n\n")
	}
	theory := theorem_get(repo, "")
	if theory != "" {
		p3.WriteString(fmt.Sprintf("[THEORY from %s]\n%s\n\n", repo, theory))
	}
	p3.WriteString("[TASK]\n")
	p3.WriteString(task)
	p3.WriteString(fmt.Sprintf("\n\n[RETRY — previous attempt had issues]\n%s", truncDiff(prevOutput, 3000)))
	p3.WriteString(fmt.Sprintf("\n\n[UPDATED GPU CONFIG] use CUDA_VISIBLE_DEVICES=%s\n", gpuUsable))
	p3.WriteString(fmt.Sprintf(dev_verify_instruction, gpuAll, gpuUsable, gpuUsable, gpuAll))

	child3, err := Fork(p3.String(), ".")
	if err != nil {
		return prevOutput
	}
	if m := getMeta(child3); m != nil && m.ConvID != "" {
		RegisterConv(pid, m.ConvID, repo, "dev_verify", 3)
	}
	Wait(child3)
	step.ChildPid = child3

	stdout3 := GetResult(child3)
	status3 := parse_last_status(stdout3)

	if status3 != nil {
		dev_update_gpu(pid, status3, gpuAll, gpuUsable)
		for k, v := range status3 {
			PRFeedbackAdd(pid, fmt.Sprintf("- dev[%s]: %s=%s", repo, k, v))
		}
		if status3["compile"] == "pass" && status3["smoke"] == "pass" {
			log.Printf("dev_from_theorems: %s verified in phase 3", repo)
			return stdout3
		}
	}

	log.Printf("dev_from_theorems: %s failed after 3 phases, gpu_usable=%s", repo, gpuUsable)
	return stdout3
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

if compile fails and you believe seeing specific source files would help,
set needs_source=true and list the files. the kernel will decide what to provide.
do NOT guess at source structure you haven't seen — ask the kernel for it.

VERIFICATION STEPS:
1. fill the form template with your project's identifiers
2. write the code to the dev repo
3. compile — if fail after 2 tries, set needs_source=true with the files you need
4. if compile passes, test GPU P2P on all pairs
5. smoke test end to end
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
