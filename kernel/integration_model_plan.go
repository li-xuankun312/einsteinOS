package kernel

// integration_model_plan.go — how the port's kernels run the model on this machine, asked of a
// planner conversation by the kernel.
//
// The reference's engine was written for one model; the machine has another of its family
// (11/22: the reference's GGUF architecture and a safetensors model whose config.json names
// another). Which of the units' kernels compute which of this model's tensors, what the model
// computes that no unit ported, which units' kernels its forward does not need, and how the
// program that runs it divides — none of that was asked: 11/22 05:01-12:52 the integration
// conversation had the spawn commands and started nothing, and spent 7h50m on vllm and
// transformers. A planner is given what the kernel measured — the model's config.json and
// tensors, each unit's ported kernels and the reference files they came from, the theory —
// and answers in lines the kernel reads; it decides correspondences only, never a number.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	integrationModelFile = "integration_model.md"
	integrationTheory    = "theory.md"
	// inserted into the shell: %[1]d tensor groups of the model, %[2]d units with ported kernels
	integrationModelWhatFmt = "内核测量了这台机器上的模型（%[1]d 组张量）和移植版 %[2]d 个 unit 已移植的 kernel"
	// %[1]s the data file, %[2]s the theory
	integrationModelContentFmt = "[DATA] %[1]s: the model on this machine as its own files say it is (config.json, every tensor group's name, dtype and shape), " +
		"and per unit of the port the kernels it ported and the reference files they came from; %[2]s: the theory the units were written from.\n" +
		"[ANSWER] the kernel reads lines:\n" +
		"'op: <tensor group> <unit id> <kernel>' — that unit's kernel computes with that tensor group of the model (the group as the data names it); given to the integration unit.\n" +
		"'missing: <tensor group> <what>' — what the model computes with that group that no unit's kernel does; given to the integration unit, which writes it.\n" +
		"'unused: <unit id>' — a unit none of whose kernels the model's forward needs; its kernels are not asked of the end-to-end run.\n" +
		"'part: <name>: <what it does>' — one part of the program that runs the model; the parts are given to the integration unit with their count.\n" +
		"a line naming a group, unit or kernel the data does not have is not read.\n"
)

// integrationModelData is the planner's data file.
func integrationModelData(ms ModelShape, perUnit map[string][]string, sources map[string][]string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## the model at %s (measured: config.json and the safetensors headers)\n\n### config.json\n```\n%s```\n\n### tensor groups (name, dtype, shape, count, the numeric segments' values)\n```\n%s```\n\n",
		ms.Dir, ms.ConfigLines(), ms.TensorLines()))
	b.WriteString("## the units of the port: the kernels each ported, and the reference files they came from\n")
	var units []string
	for u := range perUnit {
		units = append(units, u)
	}
	sort.Strings(units)
	for _, u := range units {
		b.WriteString(fmt.Sprintf("\n### %s\nkernels: %s\nfrom: %s\n", u, strings.Join(perUnit[u], " "), strings.Join(sources[u], " ")))
	}
	return b.String()
}

// integrationModelKey: what the planner's answer depends on.
func integrationModelKey(data, theory string) string {
	h := sha256.Sum256([]byte(data + "\x00" + theory))
	return hex.EncodeToString(h[:8])
}

var (
	integrationModelMu  sync.Mutex
	integrationModelMem = map[string]string{} // key -> the planner's answer
)

// askIntegrationModel returns the planner's answer for data and theory: from the plan's store
// or this process's memory when the same question was asked, else from a planner conversation,
// stored in both.
func askIntegrationModel(pid int64, repo string, plan *DevPlan, data, theory string, groups, units int, wait func(func())) (string, bool) {
	key := integrationModelKey(data, theory)
	integrationModelMu.Lock()
	out, ok := integrationModelMem[key]
	integrationModelMu.Unlock()
	if !ok && plan != nil && plan.IntegrationModel != nil {
		out, ok = plan.IntegrationModel[key]
	}
	if ok {
		return out, false
	}
	files := []ForkFile{{Name: integrationModelFile, Data: []byte(data)}}
	if theory != "" {
		files = append(files, ForkFile{Name: integrationTheory, Data: []byte(theory)})
	}
	prompt := Plain(localUploadsPath(integrationModelFile), fmt.Sprintf(integrationModelWhatFmt, groups, units),
		fmt.Sprintf(integrationModelContentFmt, localUploadsPath(integrationModelFile), localUploadsPath(integrationTheory))+spawnLeafFact+DoneInstruction())
	wait(func() {
		SetForkFiles(files)
		out = devPlannerRunner(pid, repo, prompt)
	})
	log.Printf("dev_from_theorems: %s the planner answered how the port's kernels run the model (%d bytes)", repo, len(out))
	if strings.TrimSpace(out) == "" {
		return "", true
	}
	integrationModelMu.Lock()
	integrationModelMem[key] = out
	integrationModelMu.Unlock()
	if plan != nil {
		if plan.IntegrationModel == nil {
			plan.IntegrationModel = map[string]string{}
		}
		plan.IntegrationModel[key] = out
	}
	return out, true
}

// integrationAnswers: per integration unit, the planner's last answer (read by the end-to-end
// gates, which run in the same process after the unit's prompt).
var (
	integrationAnswersMu sync.Mutex
	integrationAnswers   = map[string]string{}
)

func integrationAnswerOf(unitID string) string {
	integrationAnswersMu.Lock()
	defer integrationAnswersMu.Unlock()
	return integrationAnswers[unitID]
}

// integrationModelAnswer asks (or reads back) the planner's answer for the integration unit:
// the model as measured, the kernels each unit ported on the dev branch and the reference
// files they came from, and the theory stored there.
func (r *devRun) integrationModelAnswer(unit DevPlanNode) string {
	ms, err := modelShapeOf(evalModel())
	if err != nil {
		return ""
	}
	dir, err := devRepoLocalDir(r.devRepo)
	if err != nil {
		return ""
	}
	tree, err := branchTree(dir, r.branch)
	if err != nil {
		log.Printf("dev_from_theorems: %s: the branch could not be read for the planner's question on the model (%v)", r.repo, err)
		return ""
	}
	defer os.RemoveAll(tree)
	perUnit := e2ePortKernels(tree, unit.ID)
	if len(perUnit) == 0 {
		return ""
	}
	sources := map[string][]string{}
	for _, l := range r.plan.Leaves() {
		if _, ok := perUnit[l.ID]; ok {
			sources[l.ID] = r.requirements(l).Sources
		}
	}
	theory, _ := os.ReadFile(filepath.Join(tree, ".einsteinOS", "theory.md"))
	out, fresh := askIntegrationModel(r.pid, r.repo, r.plan, integrationModelData(ms, perUnit, sources), string(theory), len(ms.TensorGroups()), len(perUnit), r.whileWaiting)
	if fresh && out != "" {
		devPlanSave(r.repo, r.devRepo, r.branch, r.plan, "plan: the planner's answer on how the port runs the model")
	}
	integrationAnswersMu.Lock()
	integrationAnswers[unit.ID] = out
	integrationAnswersMu.Unlock()
	return out
}
