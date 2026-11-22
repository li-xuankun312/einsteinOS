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
	integrationAnswers   = map[string]IntegrationModelPlan{}
)

func integrationAnswerOf(unitID string) IntegrationModelPlan {
	integrationAnswersMu.Lock()
	defer integrationAnswersMu.Unlock()
	return integrationAnswers[unitID]
}

// integrationModelAnswer asks (or reads back) the planner's answer for the integration unit:
// the model as measured, the kernels each unit ported on the dev branch and the reference
// files they came from, and the theory stored there.
func (r *devRun) integrationModelAnswer(unit DevPlanNode) IntegrationModelPlan {
	ms, err := modelShapeOf(evalModel())
	if err != nil {
		return IntegrationModelPlan{}
	}
	dir, err := devRepoLocalDir(r.devRepo)
	if err != nil {
		return IntegrationModelPlan{}
	}
	tree, err := branchTree(dir, r.branch)
	if err != nil {
		log.Printf("dev_from_theorems: %s: the branch could not be read for the planner's question on the model (%v)", r.repo, err)
		return IntegrationModelPlan{}
	}
	defer os.RemoveAll(tree)
	perUnit := e2ePortKernels(tree, unit.ID)
	if len(perUnit) == 0 {
		return IntegrationModelPlan{}
	}
	sources := map[string][]string{}
	for _, l := range r.plan.Leaves() {
		if _, ok := perUnit[l.ID]; ok {
			sources[l.ID] = r.shown(l).Sources
		}
	}
	theory, _ := os.ReadFile(filepath.Join(tree, ".einsteinOS", "theory.md"))
	out, fresh := askIntegrationModel(r.pid, r.repo, r.plan, integrationModelData(ms, perUnit, sources), string(theory), len(ms.TensorGroups()), len(perUnit), r.whileWaiting)
	if fresh && out != "" {
		devPlanSave(r.repo, r.devRepo, r.branch, r.plan, "plan: the planner's answer on how the port runs the model")
	}
	p := parseIntegrationModel(out, ms, perUnit)
	log.Printf(integrationModelLogFmt, r.repo, unit.ID, p.counts(), len(ms.TensorGroups()), len(perUnit))
	integrationAnswersMu.Lock()
	integrationAnswers[unit.ID] = p
	integrationAnswersMu.Unlock()
	return p
}

// IntegrationOp: a tensor group of the model and the unit kernel the planner found computing
// with it; Unit is empty for a group no unit's kernel computes with (What says what).
type IntegrationOp struct {
	Group, Unit, Kernel, What string
}

// IntegrationModelPlan is the planner's answer, each line checked against what was measured.
type IntegrationModelPlan struct {
	Ops []IntegrationOp
	// Unused: units none of whose kernels the model's forward needs — never a unit an op line
	// uses, and never every unit
	Unused []string
	// Parts: how the program that runs the model divides, "<name>: <what it does>"
	Parts []string
	// Dropped: lines of the answer's kinds that named something the data does not have
	Dropped int
}

// parseIntegrationModel reads the planner's lines; a line naming a group, unit or kernel the
// data does not have is dropped.
func parseIntegrationModel(out string, ms ModelShape, perUnit map[string][]string) IntegrationModelPlan {
	groups := map[string]bool{}
	for _, g := range ms.TensorGroups() {
		groups[g.Pattern] = true
	}
	var p IntegrationModelPlan
	for _, raw := range strings.Split(out, "\n") {
		l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "- "))
		l = strings.TrimSpace(strings.Trim(l, "`"))
		switch {
		case strings.HasPrefix(l, "op:"):
			f := strings.Fields(strings.TrimPrefix(l, "op:"))
			if len(f) != 3 || !groups[f[0]] || !hasString(perUnit[f[1]], f[2]) {
				p.Dropped++
				continue
			}
			p.Ops = append(p.Ops, IntegrationOp{Group: f[0], Unit: f[1], Kernel: f[2]})
		case strings.HasPrefix(l, "missing:"):
			f := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(l, "missing:")), " ", 2)
			if len(f) != 2 || !groups[f[0]] {
				p.Dropped++
				continue
			}
			p.Ops = append(p.Ops, IntegrationOp{Group: f[0], What: strings.TrimSpace(f[1])})
		case strings.HasPrefix(l, "part:"):
			if f := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(l, "part:")), ":", 2); len(f) == 2 && strings.TrimSpace(f[0]) != "" && strings.TrimSpace(f[1]) != "" {
				p.Parts = append(p.Parts, strings.TrimSpace(f[0])+": "+strings.TrimSpace(f[1]))
			} else {
				p.Dropped++
			}
		case strings.HasPrefix(l, "unused:"):
			if u := strings.TrimSpace(strings.TrimPrefix(l, "unused:")); perUnit[u] != nil && !hasString(p.Unused, u) {
				p.Unused = append(p.Unused, u)
			} else {
				p.Dropped++
			}
		}
	}
	// a unit an op line uses is used; and a model the port runs uses some unit's kernels
	var keep []string
	for _, u := range p.Unused {
		used := false
		for _, o := range p.Ops {
			used = used || o.Unit == u
		}
		if !used {
			keep = append(keep, u)
		}
	}
	sort.Strings(keep)
	if len(keep) >= len(perUnit) {
		keep = nil
	}
	p.Unused = keep
	return p
}

// e2eUsedPortKernels: e2ePortKernels without the units the planner found the model's forward
// does not need (integrationAnswerOf). 11/22 every unit's kernels were asked of the end-to-end
// run, the hyper-connection, per-layer-embedding and sparse-attention units' among them, which
// a run of this model has no use for: e2e.sh ran each unit's parity binary on made-up data
// (build/e2e_all_check) beside it to have them launched.
func e2eUsedPortKernels(ws, unitID string) map[string][]string {
	perUnit := e2ePortKernels(ws, unitID)
	for _, u := range integrationAnswerOf(unitID).Unused {
		delete(perUnit, u)
	}
	return perUnit
}

func hasString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

const (
	// %[1]d groups with a unit kernel, %[2]d groups no unit's kernel computes with
	integrationOpsHeadFmt = "[THE MODEL ON THE PORT — a planner's answer to the kernel's question, given the model and the units' kernels as measured: %[1]d tensor-group use(s) by a unit's kernel, %[2]d by none, which this unit writes]\n"
	// %[1]s group, %[2]s unit, %[3]s kernel
	integrationOpLineFmt = "  %[1]s: %[2]s %[3]s\n"
	// %[1]s group, %[2]s what the model computes with it
	integrationMissingLineFmt = "  %[1]s: no unit — %[2]s\n"
	// %[1]d units, %[2]s their ids
	integrationUnusedFmt = "%[1]d unit(s) whose kernels this model's forward does not need, so the end-to-end run is not asked to launch them: %[2]s\n"
	// %[1]d parts, %[2]d the spawn policy's children, %[3]d its tree
	integrationPartsFmt = "the program that runs the model, in the planner's %[1]d part(s) (the conversation commands start at most %[2]d work conversations from this one, %[3]d in its tree):\n"
)

// Block: the answer as the integration unit is given it.
func (p IntegrationModelPlan) Block() string {
	if len(p.Ops) == 0 && len(p.Unused) == 0 && len(p.Parts) == 0 {
		return ""
	}
	var b strings.Builder
	have, none := 0, 0
	for _, o := range p.Ops {
		if o.Unit != "" {
			have++
		} else {
			none++
		}
	}
	b.WriteString(fmt.Sprintf(integrationOpsHeadFmt, have, none))
	for _, o := range p.Ops {
		if o.Unit != "" {
			b.WriteString(fmt.Sprintf(integrationOpLineFmt, o.Group, o.Unit, o.Kernel))
		} else {
			b.WriteString(fmt.Sprintf(integrationMissingLineFmt, o.Group, o.What))
		}
	}
	if len(p.Unused) > 0 {
		b.WriteString(fmt.Sprintf(integrationUnusedFmt, len(p.Unused), strings.Join(p.Unused, " ")))
	}
	if len(p.Parts) > 0 {
		pol := DefaultSpawnPolicy()
		b.WriteString(fmt.Sprintf(integrationPartsFmt, len(p.Parts), pol.MaxChildren, pol.MaxTree))
		for _, s := range p.Parts {
			b.WriteString("  " + s + "\n")
		}
	}
	return b.String()
}

// %[1]s repo, %[2]s unit, %[3]s the answer's counts, %[4]d tensor groups, %[5]d units with kernels
const integrationModelLogFmt = "dev_from_theorems: %[1]s integration unit %[2]s: the planner's answer read: %[3]s (of %[4]d tensor groups and %[5]d units with ported kernels)"

// counts: how many lines of each kind the answer had that the kernel took, and dropped.
func (p IntegrationModelPlan) counts() string {
	op, missing := 0, 0
	for _, o := range p.Ops {
		if o.Unit != "" {
			op++
		} else {
			missing++
		}
	}
	return fmt.Sprintf("op %d, missing %d, unused %d (%s), part %d, dropped %d", op, missing, len(p.Unused), strings.Join(p.Unused, " "), len(p.Parts), p.Dropped)
}
