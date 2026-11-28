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
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	integrationModelFile = "integration_model.md"
	integrationTheory    = "theory.md"
	// inserted into the shell: %[1]d tensor groups of the model, %[2]d units with ported kernels
	integrationModelWhatFmt = "内核测量了这台机器上的模型（%[1]d 组张量）和移植版 %[2]d 个 unit 已移植的 kernel"
	// %[1]s the data file, %[2]s the theory
	integrationModelContentFmt = "[DATA] %[1]s: the model on this machine as its own files say it is (config.json, every tensor group's name, dtype and shape), " +
		"per unit of the port the kernels it ported and the reference files they came from, the port's tree as this attempt starts on it (its programs, the sources naming the model's weight files, the tensor groups no source names), " +
		"the integration unit's last verification, and the previous answer to this question; %[2]s: the theory the units were written from.\n" +
		"[ANSWER] the kernel reads lines:\n" +
		"'op: <tensor group> <unit id> <kernel>' — that unit's kernel computes with that tensor group of the model (the group as the data names it); given to the integration unit.\n" +
		"'missing: <tensor group> <what>' — what the model computes with that group that no unit's kernel does; given to the integration unit, which writes it.\n" +
		"'unused: <unit id>' — a unit none of whose kernels the model's forward needs; its kernels are not asked of the end-to-end run.\n" +
		"'part: <name>: <what it does>' — one part of the program that runs the model; the parts are given to the integration unit with their count.\n" +
		"'partfile: <part name> <path>' — a file of the tree that part writes, one line per file. The kernel gives each part's conversation its own files and the files of the others, and a part that writes another's file is logged; a path the tree does not have, and a file two parts name, are read for the part that names it last.\n" +
		"a line naming a group, unit, kernel or path the data does not have is not read.\n"
)

// integrationModelData is the planner's data file.
func integrationModelData(ms ModelShape, perUnit map[string][]string, sources map[string][]string) string {
	return integrationModelDataFiles(ms, perUnit, sources, "")
}

// integrationModelDataFiles is integrationModelData with the tree's files a part may be given.
func integrationModelDataFiles(ms ModelShape, perUnit map[string][]string, sources map[string][]string, files string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## the model at %s (measured: config.json and the safetensors headers)\n\n### config.json\n```\n%s```\n\n### tensor groups (name, dtype, shape, count, the numeric segments' values)\n```\n%s```\n\n",
		ms.Dir, ms.ConfigLines(), ms.TensorLines()))
	if files != "" {
		b.WriteString("## the files of the tree, which a partfile line names (measured)\n\n```\n" + files + "```\n\n")
	}
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
// takes says whether the kernel reads anything of an answer: one it reads nothing of is not
// kept in plan.json — nor read back from it — and is not asked again in this run.
func askIntegrationModel(pid int64, repo string, plan *DevPlan, data, theory string, groups, units int, wait func(func()), takes func(string) bool) (string, bool) {
	key := integrationModelKey(data, theory)
	integrationModelMu.Lock()
	out, ok := integrationModelMem[key]
	integrationModelMu.Unlock()
	if !ok && plan != nil && plan.IntegrationModel != nil {
		if stored, has := plan.IntegrationModel[key]; has {
			if takes(stored) {
				out, ok = stored, true
			} else {
				// 11/22 14:09:43 the planner declined and its text was kept as the answer
				delete(plan.IntegrationModel, key)
				log.Printf(integrationModelDroppedFmt, repo, firstSentence(stored))
			}
		}
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
		out = devPlannerRunner(pid, repo, prompt, RoleIntegrationPlanner)
	})
	log.Printf("dev_from_theorems: %s the planner answered how the port's kernels run the model (%d bytes)", repo, len(out))
	if strings.TrimSpace(out) == "" {
		return "", true
	}
	integrationModelMu.Lock()
	integrationModelMem[key] = out
	integrationModelMu.Unlock()
	if !takes(out) {
		log.Printf(integrationModelUntakenFmt, repo, firstSentence(out))
		return out, false
	}
	if plan != nil {
		if plan.IntegrationModel == nil {
			plan.IntegrationModel = map[string]string{}
		}
		plan.IntegrationModel[key] = out
		plan.IntegrationModelLast = key
	}
	return out, true
}

const (
	// %[1]s repo, %[2]s the answer's first sentence
	integrationModelUntakenFmt = "dev_from_theorems: %[1]s the planner's answer has no line the kernel reads (it begins: %[2]q) — not kept in plan.json, and not asked again in this run"
	integrationModelDroppedFmt = "dev_from_theorems: %[1]s plan.json held a planner's answer with no line the kernel reads (it begins: %[2]q) — dropped"
)

// firstSentence: s up to its first sentence's end, at most 200 bytes.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if (r == '.' || r == '。' || r == '\n') && i > 0 {
			return truncStr(s[:i], 200)
		}
	}
	return truncStr(s, 200)
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
func (r *devRun) integrationModelAnswer(unit DevPlanNode, baseRef string) IntegrationModelPlan {
	ms, err := modelShapeOf(evalModel())
	if err != nil {
		return IntegrationModelPlan{}
	}
	dir, err := devRepoLocalDir(r.devRepo)
	if err != nil {
		return IntegrationModelPlan{}
	}
	// the tree this attempt starts on (its wip branch), not the dev branch: the integration's
	// work is there
	ref := baseRef
	if ref == "" {
		ref = r.branch
	}
	tree, err := branchTree(dir, ref)
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
	treeFiles, treeHas := integrationTreeFiles(tree)
	takes := func(a string) bool { return parseIntegrationModelIn(a, ms, perUnit, treeHas).taken() > 0 }
	planHad := len(r.plan.IntegrationModel)
	prev := r.plan.IntegrationModel[r.plan.IntegrationModelLast]
	var book *DirectionBook
	if dir, err := evalStateDir(r.devRepo); err == nil {
		book = loadDirections(dir, unit.ID)
	}
	closed := closedDirectionsBlock(e2eClosedDirections(tree, unit.ID, book))
	data := integrationModelDataFiles(ms, perUnit, sources, treeFiles) +
		measurePortInventory(tree, ms).Block(len(ms.TensorGroups())) + closed +
		integrationFeedback(r.plan.LastFailure[unit.ID], prev) + prevUnclaimedBlock(ms, prev, perUnit, treeHas) +
		prevPartFilesBlock(tree, unit.ID, book, parseIntegrationModelIn(prev, ms, perUnit, treeHas))
	out, fresh := askIntegrationModel(r.pid, r.repo, r.plan, data, string(theory), len(ms.TensorGroups()), len(perUnit), r.whileWaiting, takes)
	if !takes(out) && prev != "" {
		// a new answer the kernel reads nothing of leaves the last one standing
		out = prev
	}
	if len(r.plan.IntegrationModel) < planHad {
		devPlanSave(r.repo, r.devRepo, r.branch, r.plan, "plan: a planner's answer with nothing the kernel reads dropped")
	}
	if fresh && out != "" {
		devPlanSave(r.repo, r.devRepo, r.branch, r.plan, "plan: the planner's answer on how the port runs the model")
	}
	p := parseIntegrationModelIn(out, ms, perUnit, treeHas)
	if prev != "" && out != prev {
		if moved := answerMoves(parseIntegrationModelIn(prev, ms, perUnit, treeHas), p); moved != "" {
			log.Printf(integrationAnswerMovedFmt, unit.ID, moved)
		}
	}
	if p.Parts, p.OffPath = offPathParts(ms, p.Parts); len(p.OffPath) > 0 {
		log.Printf(integrationOffPathLogFmt, unit.ID, len(p.OffPath), strings.Join(p.OffPath, "; "), measureModelPath(ms).TextRoot)
	}
	// which missing groups the text never goes through: 11/26 07:12 all 21 were model.visual.*,
	// and kernel_unported gave them as the groups a GDN kernel's row may name
	for _, o := range p.Ops {
		if o.Group != "" && measureModelPath(ms).Off(o.Group) {
			p.GroupsOff = append(p.GroupsOff, o.Group)
		}
	}
	// where the port's tree stands is the unit's to know too, not the planner's alone
	p.Inventory = measurePortInventory(tree, ms).Block(len(ms.TensorGroups())) + closed
	// the text path's groups no part started names (model_path.go)
	if u := unclaimedPathGroups(ms, p.Parts); len(u) > 0 && len(p.Parts) > 0 {
		log.Printf(integrationUnclaimedLogFmt, unit.ID, len(u), len(p.Parts), strings.Join(u, " "))
		p.Inventory += fmt.Sprintf(integrationUnclaimedFmt, len(p.Parts), len(u), strings.Join(u, "\n  "))
	}
	for _, d := range closedProgramPartFiles(&p, book) {
		log.Printf(integrationClosedPartFileFmt, unit.ID, d)
	}
	if line := runFilesNoPartWrites(tree, unit.ID, p.PartFiles); line != "" && len(p.PartFiles) > 0 {
		p.Facts = append(p.Facts, line)
		log.Printf(integrationPlanFactLogFmt, unit.ID, line)
	}
	for _, line := range copiedPartFiles(tree, unit.ID, p.PartFiles) {
		p.Facts = append(p.Facts, line)
		log.Printf(integrationPlanFactLogFmt, unit.ID, line)
	}
	log.Printf(integrationModelLogFmt, r.repo, unit.ID, p.counts(), len(ms.TensorGroups()), len(perUnit))
	integrationAnswersMu.Lock()
	integrationAnswers[unit.ID] = p
	integrationAnswersMu.Unlock()
	return p
}

// %[1]s unit, %[2]s the part, its file and the direction
const integrationClosedPartFileFmt = "dev_from_theorems: integration unit %[1]s: a partfile line not given to its part: %[2]s"

// %[1]s the part, %[2]s its file, %[3]s the direction, %[4]s when it closed
const closedPartFileFmt = "%[1]s %[2]s — the one program of the direction %[3]s, closed at %[4]s"

// closedProgramPartFiles takes out of p's partfile lines the files that are the one program of a
// direction the unit's end-to-end runs closed, and says each. 11/28 05:39:14 the planner's answer
// of an earlier run, the one standing, gave pytools/e2e_infer.py to the part embed, the program
// of python3.10 pytools/e2e_infer.py [port], closed at 11/22 18:11, and the part worked in it.
func closedProgramPartFiles(p *IntegrationModelPlan, book *DirectionBook) []string {
	if book == nil || len(p.PartFiles) == 0 {
		return nil
	}
	closed := map[string][2]string{} // program -> direction, when it closed
	for key, d := range book.Dirs {
		if progs := directionProgramPaths(key); d.Closed() && len(progs) == 1 {
			closed[progs[0]] = [2]string{key, d.ClosedAt.UTC().Format("01/02 15:04")}
		}
	}
	var said []string
	for part, files := range p.PartFiles {
		var kept []string
		for _, f := range files {
			if c, ok := closed[f]; ok {
				said = append(said, fmt.Sprintf(closedPartFileFmt, part, f, c[0], c[1]))
				continue
			}
			kept = append(kept, f)
		}
		if len(kept) == 0 {
			delete(p.PartFiles, part)
		} else {
			p.PartFiles[part] = kept
		}
	}
	sort.Strings(said)
	return said
}

const (
	// %[1]s unit, %[2]d groups, %[3]d parts, %[4]s the groups
	integrationUnclaimedLogFmt = "dev_from_theorems: integration unit %[1]s: %[2]d tensor group(s) on the model's text path are named by none of the planner's %[3]d part(s) started: %[4]s"
	// %[1]d parts, %[2]d groups, %[3]s the groups
	integrationUnclaimedFmt = "\n[TENSOR GROUPS ON THE MODEL'S TEXT PATH THAT NONE OF THE PLANNER'S %[1]d PARTS NAMES — measured from the parts' text and the model's files]\n%[2]d group(s):\n  %[3]s\n"
)

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
	// PartFiles: per part name, the files of the tree it writes — a path the tree has, and a
	// file of one part only (the part that names it last keeps it)
	PartFiles map[string][]string
	// OffPath: parts not started — their text names tensors off the model's text path only
	// (model_path.go)
	OffPath []string
	// Inventory: where the port's tree stands toward running the model (integration_inventory.go)
	Inventory string
	// GroupsOff: the groups of its lines off the model's text path (model_path.go)
	GroupsOff []string
	// Dropped: lines of the answer's kinds that named something the data does not have
	Dropped int
	// Facts: what the tree measures of the parts' files (runFilesNoPartWrites, copiedPartFiles)
	Facts []string
}

// parseIntegrationModel reads the planner's lines; a line naming a group, unit or kernel the
// data does not have is dropped.
func parseIntegrationModel(out string, ms ModelShape, perUnit map[string][]string) IntegrationModelPlan {
	return parseIntegrationModelIn(out, ms, perUnit, func(string) bool { return false })
}

// parseIntegrationModelIn is parseIntegrationModel where treeHas says whether the tree has a
// path a partfile line names.
func parseIntegrationModelIn(out string, ms ModelShape, perUnit map[string][]string, treeHas func(string) bool) IntegrationModelPlan {
	groups := map[string]bool{}
	for _, g := range ms.TensorGroups() {
		groups[g.Pattern] = true
	}
	var p IntegrationModelPlan
	var partFiles [][2]string // the partfile lines in the order the answer names them
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
		case strings.HasPrefix(l, "partfile:"):
			f := strings.Fields(strings.TrimPrefix(l, "partfile:"))
			if len(f) != 2 || !treeHas(f[1]) {
				p.Dropped++
				continue
			}
			partFiles = append(partFiles, [2]string{f[0], f[1]})
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
	// a file is of the part that names it last, among the parts the answer names
	owner := map[string]string{}
	for _, pf := range partFiles {
		named := false
		for _, part := range p.Parts {
			named = named || strings.HasPrefix(part, pf[0]+": ")
		}
		if !named {
			p.Dropped++
			continue
		}
		owner[pf[1]] = pf[0]
	}
	for file, name := range owner {
		if p.PartFiles == nil {
			p.PartFiles = map[string][]string{}
		}
		p.PartFiles[name] = append(p.PartFiles[name], file)
	}
	for name := range p.PartFiles {
		sort.Strings(p.PartFiles[name])
	}
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
	if len(p.Ops) == 0 && len(p.Unused) == 0 && len(p.Parts) == 0 && p.Inventory == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(p.Inventory)
	if len(p.Ops) == 0 && len(p.Unused) == 0 && len(p.Parts) == 0 {
		return b.String()
	}
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
	if len(p.Facts) > 0 {
		b.WriteString(integrationPlanFactsHead)
		for _, f := range p.Facts {
			b.WriteString("  " + f + "\n")
		}
	}
	return b.String()
}

const (
	integrationPlanFactsHead = "### the parts' files, measured from the tree\n"
	// %[1]s unit, %[2]s the fact
	integrationPlanFactLogFmt = "dev_from_theorems: integration unit %[1]s: %[2]s"
	// %[1]s e2e.sh, %[2]s the sources of the programs it runs
	runFilesNoPartFmt = "no part's files are %[1]s or the sources of the programs it runs on the model (%[2]s)"
	// %[1]s the part, %[2]s its file, %[3]s the kernels with each unit's file
	copiedPartFileFmt = "%[1]s %[2]s: every __global__ kernel it defines has the name and the parameter types of a kernel another unit's port map ports into another file — %[3]s"
)

// e2eProgramRe: a program e2e.sh runs from the build directory — build/<name>, $BUILD/<name>, ${OUT}/<name>
var e2eProgramRe = regexp.MustCompile(`(?:build|BUILD|OUT|BIN)\}?"?/([A-Za-z0-9_]+)`)

// e2eProgramSources: the tree's .cu, .cpp and .cc files whose name, extension aside, is that of a
// program the unit's e2e.sh runs from its build directory on a line naming the model's directory
// (a variable named MODEL…), comments aside — 11/28 "$BUILD/e2e_all_check" "$MODEL_REAL".
func e2eProgramSources(tree, unitID string) []string {
	b, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(devE2EScript(unitID))))
	if err != nil {
		return nil
	}
	progs := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		// a program run on the model: the line hands it the model's directory
		if !strings.Contains(l, "MODEL") {
			continue
		}
		for _, m := range e2eProgramRe.FindAllStringSubmatch(l, -1) {
			progs[m[1]] = true
		}
	}
	var out []string
	filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "build" || n == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(p)
		if (ext == ".cu" || ext == ".cpp" || ext == ".cc") && progs[strings.TrimSuffix(d.Name(), ext)] {
			rel, _ := filepath.Rel(tree, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// runFilesNoPartWrites: when no part's files are the unit's e2e.sh or a source of the programs it
// runs, that fact; "" when a part has one, or e2e.sh runs no program of the tree. 11/28 13:04:43 the
// planner's 54 partfile lines gave its parts the kernels' files and parity tests, and none was
// .einsteinOS/verify/uintegration01529aaa82b9/e2e.sh or tests/k39_integration/e2e_all_check.cu,
// the source of build/e2e_all_check, the port's program that had taken in the whole model.
func runFilesNoPartWrites(tree, unitID string, files map[string][]string) string {
	srcs := e2eProgramSources(tree, unitID)
	if len(srcs) == 0 {
		return ""
	}
	run := map[string]bool{devE2EScript(unitID): true}
	for _, s := range srcs {
		run[s] = true
	}
	for _, list := range files {
		for _, f := range list {
			if run[f] {
				return ""
			}
		}
	}
	return fmt.Sprintf(runFilesNoPartFmt, devE2EScript(unitID), strings.Join(srcs, " "))
}

// copiedPartFiles: each part's file all of whose __global__ kernels integrationCopiedKernels finds
// copies of another unit's. 11/28 13:04:43 the planner gave src/ix_gdn.cu to gdn, src/ix_moe_combine.cu
// to moe and src/ilu_qsa_step.cu to qsa, and kernel_copied had held their kernels at every
// verification since 05:55.
func copiedPartFiles(tree, unitID string, files map[string][]string) []string {
	var out []string
	for part, list := range files {
		for _, f := range list {
			if isTestPath(f) || !strings.HasSuffix(f, ".cu") {
				continue
			}
			ks := unitKernels(tree, []string{f})[f]
			if len(ks) == 0 {
				continue
			}
			copies := integrationCopiedKernels(tree, unitID, map[string][]string{f: ks})
			if len(copies) < len(ks) {
				continue
			}
			var said []string
			for _, c := range copies {
				said = append(said, strings.TrimSpace(c))
			}
			out = append(out, fmt.Sprintf(copiedPartFileFmt, part, f, strings.Join(said, "; ")))
		}
	}
	sort.Strings(out)
	return out
}

// %[1]s repo, %[2]s unit, %[3]s the answer's counts, %[4]d tensor groups, %[5]d units with kernels
// %[1]d lines, %[2]s them
const closedDirectionsFmt = "### the unit's e2e.sh runs a closed direction (measured from the script and the unit's directions)\n%[2]s\n"

func closedDirectionsBlock(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return fmt.Sprintf(closedDirectionsFmt, len(l), lines(l))
}

// %[1]s unit, %[2]s what moved
const integrationAnswerMovedFmt = "dev_from_theorems: integration unit %[1]s: the planner's new answer moves tensor groups between a unit's kernel and the integration unit's own code: %[2]s"

// answerMoves: the tensor groups the new answer moves from an op line (a unit's kernel computes
// with them) to a missing line (the integration unit writes them) and back, against the previous
// answer. 11/23 15:45:59 the answer read op 8, missing 59 where at 01:53 it had read op 78,
// missing 21, and the log said only the counts.
func answerMoves(prev, now IntegrationModelPlan) string {
	kind := func(p IntegrationModelPlan) map[string]string {
		m := map[string]string{}
		for _, o := range p.Ops {
			if o.Unit != "" {
				m[o.Group] = "op"
			} else if m[o.Group] == "" {
				m[o.Group] = "missing"
			}
		}
		return m
	}
	was, is := kind(prev), kind(now)
	var toMissing, toOp []string
	for g, k := range is {
		switch {
		case k == "missing" && was[g] == "op":
			toMissing = append(toMissing, g)
		case k == "op" && was[g] == "missing":
			toOp = append(toOp, g)
		}
	}
	if len(toMissing) == 0 && len(toOp) == 0 {
		return ""
	}
	sort.Strings(toMissing)
	sort.Strings(toOp)
	return fmt.Sprintf("%d from op to missing (%s); %d from missing to op (%s)", len(toMissing), strings.Join(toMissing, " "), len(toOp), strings.Join(toOp, " "))
}

// %[1]s unit, %[2]d parts, %[3]s them, %[4]s the text path's root
const integrationOffPathLogFmt = "dev_from_theorems: integration unit %[1]s: %[2]d of the planner's part(s) are not started — they name only tensors off the path the model's text goes through (%[4]s, the layers num_hidden_layers counts): %[3]s"

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

// opGroups: per tensor group the op lines name, the kernels they name for it (of the units in
// perUnit), sorted — the kernels of one group are ways to compute it, any of which does.
func (p IntegrationModelPlan) opGroups(perUnit map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, o := range p.Ops {
		if o.Unit != "" && perUnit[o.Unit] != nil && !hasString(out[o.Group], o.Kernel) {
			out[o.Group] = append(out[o.Group], o.Kernel)
		}
	}
	for g := range out {
		sort.Strings(out[g])
	}
	return out
}

// The parts of the program that runs the model are started by the kernel as work
// conversations under the integration unit's conversation, one per part, on the same tree:
// 11/22 05:01-12:52 and 14:10-15:15 the integration conversation was given the spawn
// commands, and the parts at 14:10, and started none. Each is given its part as its scope and
// what the integration unit is given of the model; the kernel waits for the tree before it
// judges the unit (dev_run.go, WaitConvTree).
var (
	integrationPartsMu      sync.Mutex
	integrationPartsStarted = map[string]bool{} // unit|parts -> started in this process
)

// %[1]s unit, %[2]d started, %[3]d parts, %[4]s parent, %[5]s the addresses or why not
const integrationPartsLogFmt = "dev_from_theorems: integration unit %[1]s: %[2]d of the planner's %[3]d part(s) started as work conversations under %[4]s: %[5]s"

// startIntegrationParts starts, under parent, work conversations for the parts — at most
// integrationPartsAtOnce of them running at once, each next part started when one of them ends,
// a part whose conversation ended with an error started once more — once per unit and set of
// parts in this process; body is what each is given beside its part. It returns the ones it
// started at once; the rest are started in the background. 11/23 01:53:37 the six parts allowed
// were started at once beside the integration conversation, seven running, and at 02:19:23 the
// server refused one of them for the account's concurrency.
func startIntegrationParts(unitID, parent string, parts []string, body string, files map[string][]string) []SpawnResult {
	if parent == "" || len(parts) == 0 {
		return nil
	}
	key := unitID + "|" + strings.Join(parts, "\n")
	integrationPartsMu.Lock()
	if integrationPartsStarted[key] {
		integrationPartsMu.Unlock()
		return nil
	}
	integrationPartsStarted[key] = true
	integrationPartsMu.Unlock()

	partMirrorMu.Lock()
	mirror := partMirrorPath[unitID]
	partMirrorMu.Unlock()
	noteMirrorConv(unitID, mirror, parent, "")
	noteMirrorPartFiles(unitID, files)
	atOnce := integrationPartsAtOnce()
	queue := append([]string(nil), parts...)
	retried := map[string]bool{}
	prompts := map[string]string{} // addr -> the prompt its part was started with
	start := func(part string) (SpawnResult, bool) {
		prompt := spawnPrompt(parent, "work", part, body+partFilesOf(unitID, part, files)+partSharedBlock(unitID))
		res, err := spawnConvWith(parent, "work", prompt, kernelPartPolicy(parent))
		if err != nil {
			log.Printf(integrationPartsLogFmt, unitID, 0, len(parts), shortID(parent), fmt.Sprintf("%q: %v", part, err))
			return res, false
		}
		noteMirrorConv(unitID, mirror, res.Addr, part)
		prompts[res.Addr] = prompt
		return res, true
	}
	var first []SpawnResult
	running := map[string]string{} // addr -> part
	var said []string
	for len(queue) > 0 && len(running) < atOnce {
		part := queue[0]
		queue = queue[1:]
		res, ok := start(part)
		if !ok {
			break
		}
		first = append(first, res)
		running[res.Addr] = part
		said = append(said, shortID(res.Addr)+" "+part)
	}
	log.Printf(integrationPartsLogFmt, unitID, len(first), len(parts), shortID(parent), strings.Join(said, "; ")+fmt.Sprintf(integrationPartsQueuedFmt, atOnce, len(queue)))
	if len(queue) == 0 && len(running) == 0 {
		return first
	}
	go func() {
		for len(running) > 0 {
			time.Sleep(integrationPartsPoll)
			for addr, part := range running {
				out, done := GetConvResult(addr)
				if !done {
					continue
				}
				delete(running, addr)
				if wrote := partWroteOthersBy(unitID, addr, part, partFilesOr(unitID, files)); len(wrote) > 0 {
					log.Printf(partWroteOthersFmt, unitID, shortID(addr), part, len(wrote), strings.Join(wrote, "; "))
				}
				if why := conversationError(out); why != "" {
					if strings.Contains(why, concurrentLimitCode) {
						// the account's concurrency, not the part: started again once a place is free,
						// its one retry kept (conv_room.go waits for the place)
						forgetSpawn(parent, "work", prompts[addr])
						queue = append(queue, part)
						log.Printf(integrationPartsRoomFmt, unitID, shortID(addr), partName(part))
					} else if !retried[part] {
						retried[part] = true
						forgetSpawn(parent, "work", prompts[addr])
						queue = append(queue, part)
						log.Printf(integrationPartsAgainFmt, unitID, shortID(addr), part, why)
					} else {
						log.Printf(integrationPartsGaveUpFmt, unitID, shortID(addr), part, why)
					}
				}
				for len(queue) > 0 && len(running) < atOnce {
					next := queue[0]
					queue = queue[1:]
					if res, ok := start(next); ok {
						running[res.Addr] = next
						log.Printf(integrationPartsNextFmt, unitID, shortID(res.Addr), next, len(queue))
					}
				}
			}
		}
	}()
	return first
}

const (
	// %[1]d at once, %[2]d queued
	integrationPartsQueuedFmt = " — at most %[1]d at once, %[2]d queued"
	// %[1]s unit, %[2]s the conversation, %[3]s its part, %[4]s how its result begins
	integrationPartsAgainFmt = "dev_from_theorems: integration unit %[1]s: part conversation %[2]s (%[3]s) ended with an error (%[4]s) — its part is started once more"
	// %[1]s unit, %[2]s the conversation, %[3]s its part, %[4]s why
	integrationPartsGaveUpFmt = "dev_from_theorems: integration unit %[1]s: part conversation %[2]s (%[3]s) ended with an error again (%[4]s) — not started a third time"
	// %[1]s unit, %[2]s the conversation, %[3]s its part, %[4]d still queued
	integrationPartsNextFmt = "dev_from_theorems: integration unit %[1]s: part conversation %[2]s started for %[3]s; %[4]d part(s) still queued"
)

// concurrentLimitCode: the server's word for a conversation refused for the account's concurrency.
const concurrentLimitCode = "concurrent_limit_reached"

// %[1]s unit, %[2]s the conversation, %[3]s its part — 11/24 03:34:27 and 03:47:50 the loader and
// layer_norm parts were refused for the account's concurrency and each used its one retry on it
const integrationPartsRoomFmt = "dev_from_theorems: integration unit %[1]s: part conversation %[2]s (%[3]s) was refused for the account's concurrency — started again when a place is free, its retry kept"

// integrationPartsPoll: how often the started parts' conversations are looked at (a variable
// for tests).
var integrationPartsPoll = 15 * time.Second

// integrationPartsAtOnce: how many part conversations run at once — one fewer than the
// conversations the server took at once, when it refused one for the account's concurrency
// (conv_room.go), leaving the integration conversation its place; else the spawn policy's.
var integrationPartsAtOnce = func() int {
	n := DefaultSpawnPolicy().MaxChildren
	if room := ConversationRoom(); room > 0 && room-1 < n {
		n = room - 1
	}
	if n < 1 {
		n = 1
	}
	return n
}

// conversationError: why a conversation ended with the model layer's error exit — its final
// text from the last error marker on, which doExit writes at the end of it ("" when it did not).
// 11/23 06:29:25 and 06:45:59 the parts 8f60a081 and 579c0d30 ended so, and a test of the text's
// start found nothing: the marker is where the conversation's text ends.
func conversationError(out string) string {
	tail := out
	if len(tail) > conversationErrorTail {
		tail = tail[len(tail)-conversationErrorTail:]
	}
	at := -1
	for _, m := range []string{"[Error]", "[API Error]"} {
		if i := strings.LastIndex(tail, m); i > at {
			at = i
		}
	}
	if at < 0 {
		return ""
	}
	return truncStr(strings.TrimSpace(tail[at:]), 300)
}

// conversationErrorTail: how much of a conversation's end is read for its error exit.
const conversationErrorTail = 4096

// startIntegrationPartsOf waits for the forked conversation child to report its address and
// starts the parts under it (in the background: the unit waits on the conversation).
func startIntegrationPartsOf(unitID string, child int64, parts []string, body string, files map[string][]string) {
	if len(parts) == 0 {
		return
	}
	go func() {
		var parent string
		for deadline := time.Now().Add(spawnPollTo); time.Now().Before(deadline); time.Sleep(spawnPollIn) {
			if parent = spawnConvID(child); parent != "" {
				break
			}
		}
		if parent == "" {
			log.Printf(integrationPartsLogFmt, unitID, 0, len(parts), "-", "the conversation did not report its address")
			return
		}
		startIntegrationParts(unitID, parent, parts, body, files)
	}()
}

// taken: how many of the answer's lines the kernel took.
func (p IntegrationModelPlan) taken() int {
	return len(p.Ops) + len(p.Unused) + len(p.Parts) + len(p.PartFiles)
}

const (
	// %[1]s the gate, %[2]s its text
	integrationLastVerificationFmt = "## the integration unit's last verification\n\ngate: %[1]s\n```\n%[2]s\n```\n\n"
	// %[1]s the answer
	integrationPreviousAnswerFmt = "## the previous answer to this question\n\n```\n%[1]s\n```\n"
)

// integrationFeedback: what the planner is told of the attempts since its last answer — the
// integration unit's last verification (plan.json's last failure: its gate and text) and that
// answer. 11/23 01:53 the planner was asked once; the four verifications and 3 hours after it
// (direction closed, a compile failure, a refusal over the branch) never reached it.
func integrationFeedback(last DevFailure, prev string) string {
	var b strings.Builder
	if last.Kind != "" || last.Text != "" {
		b.WriteString(fmt.Sprintf(integrationLastVerificationFmt, last.Kind, truncStr(last.Text, 6000)))
	}
	if prev != "" {
		b.WriteString(fmt.Sprintf(integrationPreviousAnswerFmt, prev))
	}
	return b.String()
}

const (
	// %[1]d the part's files, %[2]s them, %[3]d the other parts' files, %[4]s them
	partFilesFmt = "[THE FILES OF THIS PART — the planner named them, and the kernel checked each against the tree]\nthis part writes these %[1]d file(s):\n%[2]s"
	// %[1]d files, %[2]s them with their part
	partOthersFmt = "the other parts write these %[1]d file(s), which this part does not write:\n%[3]s"
	// %[1]s the file, %[2]s the part
	partOtherLineFmt = "  %[1]s (%[2]s)\n"
)

// partFilesBlock: the files of the part, and the files of the others, as its conversation is
// given them. 11/23 the six parts shared one mirror with no file of their own, and their edits
// of core_layer.hpp, rt_native.hpp and e2e_all_check.cu undid one another (str_replace found
// its text gone), where the 44 units of the plan are independent by what the kernel measures
// (acd1e1df).
func partFilesBlock(part string, files map[string][]string) string {
	return partFilesBlockWith(partFilesFmt, part, files)
}

// partFilesBlockWith: partFilesBlock, its first lines head (%[1]d the part's files, %[2]s them).
func partFilesBlockWith(head, part string, files map[string][]string) string {
	name, _, _ := strings.Cut(part, ":")
	mine := files[strings.TrimSpace(name)]
	if len(mine) == 0 && len(files) == 0 {
		return ""
	}
	var b strings.Builder
	var lines strings.Builder
	for _, f := range mine {
		lines.WriteString("  " + f + "\n")
	}
	b.WriteString(fmt.Sprintf(head, len(mine), lines.String()))
	var others []string
	var owner []string
	for n, fs := range files {
		if strings.TrimSpace(name) == n {
			continue
		}
		for _, f := range fs {
			others = append(others, f)
			owner = append(owner, n)
		}
	}
	if len(others) > 0 {
		idx := make([]int, len(others))
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(i, j int) bool { return others[idx[i]] < others[idx[j]] })
		var ol strings.Builder
		for _, i := range idx {
			ol.WriteString(fmt.Sprintf(partOtherLineFmt, others[i], owner[i]))
		}
		b.WriteString(fmt.Sprintf(strings.Replace(partOthersFmt, "%[3]s", "%[2]s", 1), len(others), ol.String()))
	}
	return b.String()
}

// %[1]d the part's files, %[2]s them — the files of a part measured, where the planner named none
const partFilesMeasuredFmt = "[THE FILES OF THIS PART — measured: the files its earlier conversation was first to write in the unit's mirror]\nthis part wrote these %[1]d file(s):\n%[2]s"

// partFilesOr: the planner's files of each part, or — where its answer gave none — the files
// each part's conversation was first to write in the unit's mirror (mirror_writes.go).
func partFilesOr(unitID string, files map[string][]string) map[string][]string {
	if len(files) > 0 {
		return files
	}
	return mirrorOwners(unitID)
}

// partFilesOf: the files block a part's conversation is started with — the planner's, or the
// measured ones when it named none. 11/23 15:45:59 the planner's answer had no partfile line
// and the parts started at 16:58:59 and 17:12:02, when the others had written for an hour, were
// given no file of theirs or of anyone's.
func partFilesOf(unitID, part string, files map[string][]string) string {
	if len(files) > 0 {
		return partFilesBlock(part, files)
	}
	measured := mirrorOwners(unitID)
	if len(measured) == 0 {
		return ""
	}
	return partFilesBlockWith(partFilesMeasuredFmt, part, measured)
}

// integrationTreeFilesMax: how many of the tree's files the planner is given.
const integrationTreeFilesMax = 400

// integrationTreeFiles: the tree's source files, one per line, and whether the tree has a path.
// A partfile line names one of these.
func integrationTreeFiles(tree string) (string, func(string) bool) {
	var all []string
	filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "build", "_build", "node_modules", "__pycache__":
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".c", ".cc", ".cpp", ".cu", ".cuh", ".h", ".hpp", ".py", ".sh", ".lean":
			rel, rerr := filepath.Rel(tree, p)
			if rerr == nil {
				all = append(all, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	sort.Strings(all)
	has := map[string]bool{}
	for _, f := range all {
		has[f] = true
	}
	shown := all
	more := 0
	if len(shown) > integrationTreeFilesMax {
		more = len(shown) - integrationTreeFilesMax
		shown = shown[:integrationTreeFilesMax]
	}
	var b strings.Builder
	for _, f := range shown {
		b.WriteString("  " + f + "\n")
	}
	if more > 0 {
		b.WriteString(fmt.Sprintf("  … and %d more\n", more))
	}
	// a path the tree has, whether or not it was shown; a file a part is to create is named
	// under a directory the tree has
	return b.String(), func(p string) bool {
		p = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(p), "./"))
		if has[p] {
			return true
		}
		if d := path.Dir(p); d != "." && d != "/" {
			st, err := os.Stat(filepath.Join(tree, filepath.FromSlash(d)))
			return err == nil && st.IsDir()
		}
		return false
	}
}

// %[1]s unit, %[2]s the conversation, %[3]s its part, %[4]d files, %[5]s them with their part
const partWroteOthersFmt = "dev_from_theorems: integration unit %[1]s: part conversation %[2]s (%[3]s) wrote %[4]d file(s) of other parts: %[5]s"

// partMirrorBase: per unit, the mirror's files as they were when its parts were started.
var (
	partMirrorMu   sync.Mutex
	partMirrorBase = map[string]fileDigest{}
	partMirrorPath = map[string]string{} // unit -> its mirror
)

// notePartMirrorBase records the mirror as it is before the parts are started.
func notePartMirrorBase(unitID, mirror string) {
	d, err := snapshotTree(mirror)
	if err != nil {
		return
	}
	partMirrorMu.Lock()
	partMirrorBase[unitID] = d
	partMirrorPath[unitID] = mirror
	partMirrorMu.Unlock()
}

// partWroteOthers: the files of other parts that changed in the mirror since the parts were
// started — measured, not asked of the conversation, which cannot be taken at its word for it.
func partWroteOthers(unitID, part string, files map[string][]string) []string {
	name, _, _ := strings.Cut(part, ":")
	name = strings.TrimSpace(name)
	partMirrorMu.Lock()
	base, ok := partMirrorBase[unitID]
	mirror := partMirrorPath[unitID]
	partMirrorMu.Unlock()
	if !ok || mirror == "" || len(files) == 0 {
		return nil
	}
	var out []string
	for other, fs := range files {
		if other == name {
			continue
		}
		for _, f := range fs {
			now, err := digestFile(filepath.Join(mirror, filepath.FromSlash(f)))
			was, had := base[f]
			if (err == nil) != had || (had && now != was) {
				out = append(out, fmt.Sprintf("%s (%s)", f, other))
			}
		}
	}
	sort.Strings(out)
	return out
}

// %[1]d parts, %[2]d groups, %[3]s them
const integrationPrevUnclaimedFmt = "[TENSOR GROUPS ON THE MODEL'S TEXT PATH THAT NONE OF THE LAST ANSWER'S %[1]d STARTED PARTS NAMES — measured from those parts' text and the model's files]\n%[2]d group(s):\n  %[3]s\n"

// prevUnclaimedBlock: for the planner asked again, the text path's groups no part of its last
// answer that was started names. 11/25 04:08, 06:01 and 07:47 the planner gave the same six
// parts three times, none naming lm_head.weight.
const prevPartFilesHead = "## the previous answer's partfile lines, measured from the tree\n"

// prevPartFilesBlock: what the tree measures of the previous answer's partfile lines — the files
// that are a closed direction's one program, that no part's files are e2e.sh or the sources of the
// programs it runs on the model, the files of copies — given to the planner beside that answer.
// 11/28 17:15:05 the planner's answer was the previous one line for line, its 54 partfile lines
// with pytools/e2e_infer.py and pytools/e2e_weights.py, src/ix_gdn.cu and src/ix_moe_combine.cu,
// and none of e2e.sh and tests/k39_integration/e2e_all_check.cu; the kernel measured each of
// these at 13:04:43 and at 17:15:23, after the answer, and told the planner none of them.
func prevPartFilesBlock(tree, unitID string, book *DirectionBook, prev IntegrationModelPlan) string {
	if len(prev.PartFiles) == 0 {
		return ""
	}
	cp := prev
	cp.PartFiles = map[string][]string{}
	for k, v := range prev.PartFiles {
		cp.PartFiles[k] = append([]string(nil), v...)
	}
	lines := closedProgramPartFiles(&cp, book)
	if l := runFilesNoPartWrites(tree, unitID, prev.PartFiles); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, copiedPartFiles(tree, unitID, prev.PartFiles)...)
	if len(lines) == 0 {
		return ""
	}
	return prevPartFilesHead + "  " + strings.Join(lines, "\n  ") + "\n"
}

func prevUnclaimedBlock(ms ModelShape, prev string, perUnit map[string][]string, treeHas func(string) bool) string {
	if prev == "" {
		return ""
	}
	parts, _ := offPathParts(ms, parseIntegrationModelIn(prev, ms, perUnit, treeHas).Parts)
	u := unclaimedPathGroups(ms, parts)
	if len(parts) == 0 || len(u) == 0 {
		return ""
	}
	return fmt.Sprintf(integrationPrevUnclaimedFmt, len(parts), len(u), strings.Join(u, "\n  "))
}

const (
	// %[1]s what the names are measured against
	runCallsRemovedHeadFmt = "\n[NAMES THE PROGRAMS E2E.SH RUNS ON THE MODEL NO LONGER CALL — measured against %[1]s]\n"
	runCallsAgainstTip     = "the tree the unit started from"
	// %[1]s the commit, %[2]s its date, %[3]s its gate
	runCallsAgainstEvalFmt = "the last tree of the branch the unit started from whose run reached the evaluation on the model, %[1]s (%[2]s, gate=%[3]s)"
	// %[1]s the source, %[2]s the name, %[3]s the unit, %[4]s its kernel
	runCallRemovedFmt = "%[1]s no longer names %[2]s; %[3]s ports %[4]s\n"
)

// runCallsRemoved: the names the sources of the programs e2e.sh runs on the model held in the tree
// the unit started from (tip) and hold no longer in the mirror, comments aside, each a kernel some
// other unit's port map ports, or that kernel's launcher (the name with _kernel after it).
// 11/28 13:36:45 the unit's conversation took out of tests/k39_integration/e2e_all_check.cu its
// call of rms_norm_bf16w_inplace, the launcher of pcd027bc02f's rms_norm_bf16w_inplace_kernel,
// to clear kernel_unported (b756e5f6): each layer's input_layernorm, from the program that had
// taken in the whole model.
func runCallsRemoved(tip, mirror, unitID string) string {
	return runCallsRemovedSince(tip, mirror, unitID, "")
}

var evalGateSubjectRe = regexp.MustCompile(`gate=(integration_[a-z_]+)`)

// lastEvaluatedCommit: the latest commit of origin/<baseRef> in the clone whose subject records a
// gate of the evaluation on the model (gate=integration_…) — a tree whose unit gates all passed and
// whose e2e.sh ran on the model — with its date and that gate; "" when none.
func lastEvaluatedCommit(clone, baseRef string) (sha, date, gate string) {
	if baseRef == "" {
		return "", "", ""
	}
	out, err := gitIn(clone, "log", "--format=%H %cI %s", "-200", "origin/"+baseRef)
	if err != nil {
		return "", "", ""
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, " ", 3)
		if len(f) == 3 {
			if m := evalGateSubjectRe.FindStringSubmatch(f[2]); m != nil {
				return f[0], f[1], m[1]
			}
		}
	}
	return "", "", ""
}

// runCallsRemovedSince: runCallsRemoved measured against the last tree of origin/<baseRef> whose
// run reached the evaluation on the model, else against the tree the unit started from. 11/28
// 17:35:47 the attempt continued from the tree parked at 14:07, from which the call of
// rms_norm_bf16w_inplace had been gone since 13:36, and nothing was said; d6a1a01, parked at 05:29
// with gate=integration_direction_closed — e2e.sh run on the model — still called it twice.
func runCallsRemovedSince(tip, mirror, unitID, baseRef string) string {
	against := runCallsAgainstTip
	read := func(f string) ([]byte, error) { return os.ReadFile(filepath.Join(tip, filepath.FromSlash(f))) }
	if sha, date, gate := lastEvaluatedCommit(tip, baseRef); sha != "" {
		against = fmt.Sprintf(runCallsAgainstEvalFmt, sha[:8], date, gate)
		read = func(f string) ([]byte, error) {
			out, err := gitIn(tip, "show", sha+":"+f)
			return []byte(out), err
		}
	}
	ported := map[string]string{} // kernel -> unit
	maps, _ := filepath.Glob(filepath.Join(mirror, ".einsteinOS", "port", "*.tsv"))
	for _, m := range maps {
		u := strings.TrimSuffix(filepath.Base(m), ".tsv")
		if u == unitID {
			continue
		}
		for _, e := range readPortMap(mirror, u) {
			if n := symbolBase(e.name); strings.HasSuffix(n, "_kernel") {
				ported[n] = u
			}
		}
	}
	idents := func(text string) map[string]bool {
		out := map[string]bool{}
		for _, w := range cIdentRe.FindAllString(stripCComments(text), -1) {
			out[w] = true
		}
		return out
	}
	var lines []string
	for _, f := range e2eProgramSources(mirror, unitID) {
		then, err := read(f)
		if err != nil {
			continue
		}
		nowB, _ := os.ReadFile(filepath.Join(mirror, filepath.FromSlash(f)))
		now := idents(string(nowB))
		for w := range idents(string(then)) {
			if now[w] {
				continue
			}
			k := w
			if !strings.HasSuffix(k, "_kernel") {
				k += "_kernel"
			}
			if u, ok := ported[k]; ok {
				lines = append(lines, fmt.Sprintf(runCallRemovedFmt, f, w, u, k))
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	sort.Strings(lines)
	return fmt.Sprintf(runCallsRemovedHeadFmt, against) + strings.Join(lines, "")
}
