package kernel

// dev_plan_io.go — getting THE plan: load it if it exists, otherwise ask the
// planner conversation exactly once and store the result on the dev branch.
//
// The planner conversation decides HOW to split (cc17ad67: the dev conv must not
// decide its own scope — it always tries to do everything). The kernel decides
// WHETHER to split, from numbers (bytes / modules per unit, fix attempts per
// unit): see rebalanceGroups, dev_node_needs_split.

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var (
	devPlanMu  sync.Mutex
	devPlanMem = map[string]*DevPlan{} // repo|branch -> plan, first-level cache

	// devPlannerRunner runs one planner conversation and returns its text. It is a
	// variable so the planning logic can be tested without a model.
	devPlannerRunner = func(pid int64, repo, prompt string) string {
		child, err := ForkWithRole(prompt, ".", "dev_planner")
		if err != nil {
			log.Printf("dev_plan: %s planner fork failed: %v", repo, err)
			return ""
		}
		var convID string
		if m := getMeta(child); m != nil && m.ConvID != "" {
			convID = m.ConvID
			RegisterConv(pid, m.ConvID, repo, "dev_planner", 0)
		}
		Wait(child)
		if convID != "" {
			ConvDone(convID, "done")
		}
		return GetResult(child)
	}
)

// devUnitBudget returns the per-unit limits, overridable per run.
func devUnitBudget() (maxBytes, maxModules int) {
	maxBytes, maxModules = DevUnitMaxTheoryBytes, DevUnitMaxModules
	if v, err := strconv.Atoi(os.Getenv("EINSTEINOS_DEV_UNIT_BYTES")); err == nil && v > 0 {
		maxBytes = v
	}
	if v, err := strconv.Atoi(os.Getenv("EINSTEINOS_DEV_UNIT_MODULES")); err == nil && v > 0 {
		maxModules = v
	}
	return
}

func devPlanKey(repo, branch string) string { return repo + "|" + branch }

// ---- storage on the branch ------------------------------------------------------

func loadDevPlanFromBranch(devRepo, branch string) (*DevPlan, error) {
	dir, err := devRepoLocalDir(devRepo)
	if err != nil {
		return nil, err
	}
	if err := gitUseBranchFromRemote(dir, branch, "main"); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(dir + "/" + devPlanFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no plan yet: the normal first-run case
		}
		return nil, err
	}
	return unmarshalDevPlan(b)
}

// storeDevPlan commits plan.json to the branch. A failure is returned, not
// swallowed: a plan that exists only in memory is exactly what this file is
// here to prevent — but the caller may still continue with the in-memory copy.
func storeDevPlan(devRepo, branch string, plan *DevPlan, msg string) error {
	dir, err := devRepoLocalDir(devRepo)
	if err != nil {
		return err
	}
	if err := gitUseBranchFromRemote(dir, branch, "main"); err != nil {
		return err
	}
	b, err := plan.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir+"/.einsteinOS", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dir+"/"+devPlanFile, b, 0o644); err != nil {
		return err
	}
	GitCommit(dir, msg)
	return devGitPush(dir, devRepo, branch, 20)
}

// devPlanSave records the plan in memory and (best effort) on the branch.
func devPlanSave(repo, devRepo, branch string, plan *DevPlan, msg string) {
	devPlanMu.Lock()
	devPlanMem[devPlanKey(repo, branch)] = plan
	devPlanMu.Unlock()
	if devRepo == "" {
		return
	}
	if err := storeDevPlan(devRepo, branch, plan, msg); err != nil {
		log.Printf("dev_plan: %s plan NOT stored on %s (%s): %v — continuing with the in-memory plan", repo, branch, msg, err)
	}
}

// ---- getting the plan -----------------------------------------------------------

// dev_get_plan returns the plan for this run. It plans ONCE: a plan found in
// memory or on the branch is reused (and reconciled with the current theory);
// the planner conversation runs only when there is none.
func dev_get_plan(pid int64, repo, refSHA, theory, branch string) *DevPlan {
	sections := dev_extract_file_sections(theory)
	mods := devModulesFromSections(sections)
	maxBytes, maxModules := devUnitBudget()
	size := map[string]int{}
	for _, m := range mods {
		size[m.Name] = m.Bytes
	}
	devRepo := ""
	if drs := GetDevRepos(); len(drs) > 0 {
		devRepo = drs[0]
	}

	var plan *DevPlan
	devPlanMu.Lock()
	plan = devPlanMem[devPlanKey(repo, branch)]
	devPlanMu.Unlock()
	if plan == nil && devRepo != "" {
		p, err := loadDevPlanFromBranch(devRepo, branch)
		if err != nil {
			log.Printf("dev_plan: %s could not read plan.json from %s: %v", repo, branch, err)
		} else if p != nil && p.Ref == repo {
			plan = p
			log.Printf("dev_plan: %s loaded stored plan from %s (%d leaves)", repo, branch, len(p.Leaves()))
		}
	}

	if plan != nil {
		added, changed := plan.Reconcile(refSHA, mods)
		if len(added) > 0 {
			groups, _ := rebalanceGroups([][]string{added}, size, maxBytes, maxModules)
			plan.addGroups(groups, "added after theory changed")
			log.Printf("dev_plan: %s %d module(s) new since the stored plan → %d new unit(s); existing units untouched", repo, len(added), len(groups))
			changed = true
		}
		if changed {
			devPlanSave(repo, devRepo, branch, plan, "plan: reconcile with current theory")
		} else {
			devPlanMu.Lock()
			devPlanMem[devPlanKey(repo, branch)] = plan
			devPlanMu.Unlock()
		}
		log.Printf("dev_plan: %s reusing plan (NOT re-planning)\n%s", repo, plan.Summary())
		return plan
	}

	// no plan anywhere: plan once
	var groups [][]string
	if len(mods) <= maxModules && devTotalBytes(mods) <= maxBytes {
		names := make([]string, 0, len(mods))
		for _, m := range mods {
			names = append(names, m.Name)
		}
		groups = [][]string{names}
		log.Printf("dev_plan: %s %d modules fit one unit", repo, len(mods))
	} else {
		groups = dev_run_planner(pid, repo, mods, branch)
	}
	groups, oversize := rebalanceGroups(groups, size, maxBytes, maxModules)
	plan = newDevPlan(repo, refSHA, mods, groups, "initial plan")
	log.Printf("dev_plan: %s planned %d modules into %d units (%d single module(s) over budget, split below module level when reached)\n%s",
		repo, len(mods), len(groups), len(oversize), plan.Summary())
	devPlanSave(repo, devRepo, branch, plan, "plan: initial")
	return plan
}

func devTotalBytes(mods []DevModule) int {
	t := 0
	for _, m := range mods {
		t += m.Bytes
	}
	return t
}

// dev_run_planner runs the planner conversation on the module list and returns
// groups of canonical module names, with anything unmatched or unclaimed
// appended as extra groups (never dropped, never one giant unit).
func dev_run_planner(pid int64, repo string, mods []DevModule, branch string) [][]string {
	var list strings.Builder
	for i, m := range mods {
		list.WriteString(fmt.Sprintf("  %d. %s (%d bytes)\n", i+1, m.Name, m.Bytes))
	}
	maxBytes, maxModules := devUnitBudget()
	prompt := fmt.Sprintf(
		"[TASK] group the %d modules below into implementation units.\n\n"+
			"[IMPORTANT] the complete list of modules is below. this IS the input.\n"+
			"you do NOT need to search for files, clone repos, or find anything.\n"+
			"just read the list and group them. you may NOT start other conversations.\n\n"+
			"[MODULES] (%d entries — each is a ## MODULE section from the theory)\n%s\n"+
			"[RULES]\n"+
			"- group related modules together (same subsystem)\n"+
			"- foundational modules first (types, config, utils), then core, then high-level\n"+
			"- a unit must stay under %d bytes of theory AND under %d modules: one developer session digests that much and no more\n"+
			"- a single module bigger than the byte limit goes in a unit by itself\n"+
			"- every module from the list must appear in exactly one unit\n\n"+
			"[OUTPUT FORMAT]\n"+
			"output ONLY lines like:\n"+
			"unit: module_name_1, module_name_2\n"+
			"unit: module_name_3\n"+
			"copy the module names EXACTLY from the list. one line per unit. nothing else.\n%s",
		len(mods), len(mods), list.String(), maxBytes, maxModules, DoneInstruction())

	out := devPlannerRunner(pid, repo, prompt)
	log.Printf("dev_plan: %s planner returned %d bytes", repo, len(out))
	groups, unmatched, unclaimed := parseDevPlanOutput(out, mods)
	if len(unmatched) > 0 {
		log.Printf("dev_plan: %s planner named %d module(s) that are not in the theory (ignored): %v", repo, len(unmatched), truncStr(strings.Join(unmatched, ", "), 300))
	}
	if len(groups) == 0 {
		log.Printf("dev_plan: %s planner output unusable, grouping by name order within the budget", repo)
		names := make([]string, 0, len(mods))
		for _, m := range mods {
			names = append(names, m.Name)
		}
		return [][]string{names} // rebalanceGroups cuts it to fit
	}
	if len(unclaimed) > 0 {
		log.Printf("dev_plan: %s %d module(s) the planner did not place → extra unit(s)", repo, len(unclaimed))
		groups = append(groups, unclaimed)
	}
	return groups
}

// ---- splitting a node ---------------------------------------------------------------

var devIdentRe = regexp.MustCompile(`(?m)^\s*(?:private\s+|protected\s+|noncomputable\s+)*(?:theorem|lemma|def|abbrev|structure|inductive|instance|axiom)\s+([A-Za-z0-9_.']+)`)

// devModuleIdents lists the named items (theorems, defs, structures…) of a
// module's theory text, in order. They are the grain a module can be split by.
func devModuleIdents(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range devIdentRe.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// dev_node_needs_split is the kernel's trigger — numbers, not a judgement:
// too many bytes or modules for one session, or the unit already used up its
// fix attempts.
func dev_node_needs_split(p *DevPlan, n DevPlanNode) (bool, string) {
	maxBytes, maxModules := devUnitBudget()
	if b := p.UnitTheoryBytes(n); n.Scope == "" && b > maxBytes {
		return true, fmt.Sprintf("theory %d bytes > budget %d", b, maxBytes)
	}
	if len(n.Modules) > maxModules {
		return true, fmt.Sprintf("%d modules > budget %d", len(n.Modules), maxModules)
	}
	if p.FixAttempts[n.ID] >= DevMaxFixAttempts {
		return true, fmt.Sprintf("%d fix attempts used", p.FixAttempts[n.ID])
	}
	return false, ""
}

// dev_split_node splits one node of the plan into children, by asking a planner
// conversation HOW. A node with several modules is regrouped by module; a node
// that is a single module is split below module level, by theorem group. If the
// planner gives nothing usable the kernel bisects deterministically — a node
// that must split is never left whole just because the planner failed.
// Returns the children (nil when the node cannot be split at all).
func dev_split_node(pid int64, p *DevPlan, nodeID, repo, devRepo, branch, theory, reason string) []DevPlanNode {
	n := p.node(nodeID)
	if n == nil {
		return nil
	}
	node := *n
	sections := dev_extract_file_sections(theory)
	text := map[string]string{}
	for _, s := range sections {
		text[strings.TrimSpace(s.path)] = s.content
	}

	var groups [][]string
	var scopes []string

	if len(node.Modules) > 1 {
		mods := make([]DevModule, 0, len(node.Modules))
		for _, m := range node.Modules {
			mods = append(mods, p.Modules[m])
		}
		prompt := fmt.Sprintf(
			"[TASK] this unit of %d modules is too big for one session (%s). split it into 2-4 smaller units.\n"+
				"you may NOT start other conversations. keep related modules together, foundational first.\n\n[MODULES]\n", len(mods), reason)
		for _, m := range mods {
			prompt += fmt.Sprintf("  %s (%d bytes)\n", m.Name, m.Bytes)
		}
		prompt += "\n[OUTPUT FORMAT]\nonly lines like `unit: a, b`. copy names exactly. every module exactly once.\n" + DoneInstruction()
		out := devPlannerRunner(pid, repo, prompt)
		g, _, unclaimed := parseDevPlanOutput(out, mods)
		if len(unclaimed) > 0 && len(g) > 0 {
			g = append(g, unclaimed)
		}
		groups = g
		if len(groups) < 2 {
			groups = bisectModules(node.Modules, p)
		}
	} else {
		name := node.Modules[0]
		body := text[name]
		idents := restrictToScope(devModuleIdents(body), node.Scope)
		if len(idents) < 4 {
			log.Printf("dev_plan: %s node %s (%s) has %d named items — too few to split below module level", repo, node.ID, name, len(idents))
			return nil
		}
		prompt := fmt.Sprintf(
			"[TASK] module %s (%d bytes of theory) is too big for one session (%s). split its work into 2-4 groups of items.\n"+
				"you may NOT start other conversations. items that depend on each other stay together; foundational items first.\n\n[ITEMS]\n",
			name, len(body), reason)
		for _, id := range idents {
			prompt += "  " + id + "\n"
		}
		prompt += "\n[OUTPUT FORMAT]\nonly lines like `group: item_a, item_b, item_c`. copy item names exactly. every item exactly once.\n" + DoneInstruction()
		out := devPlannerRunner(pid, repo, prompt)
		gs := parseItemGroups(out, idents)
		if len(gs) < 2 {
			gs = bisectIdents(idents)
		}
		for _, g := range gs {
			groups = append(groups, []string{name})
			scopes = append(scopes, "items: "+strings.Join(g, ", "))
		}
	}

	kids := p.Split(nodeID, groups, scopes, reason)
	if kids == nil {
		return nil
	}
	log.Printf("dev_plan: %s node %s split into %d (%s)", repo, nodeID, len(kids), reason)
	devPlanSave(repo, devRepo, branch, p, fmt.Sprintf("plan: split %s (%s)", nodeID, truncStr(reason, 60)))
	return kids
}

// restrictToScope keeps only the items a scoped node is responsible for, so a
// scoped node that must split again splits ITS items, not the whole module's.
func restrictToScope(idents []string, scope string) []string {
	if !strings.HasPrefix(scope, "items: ") {
		return idents
	}
	in := map[string]bool{}
	for _, f := range strings.Split(strings.TrimPrefix(scope, "items: "), ",") {
		in[strings.TrimSpace(f)] = true
	}
	var out []string
	for _, id := range idents {
		if in[id] {
			out = append(out, id)
		}
	}
	return out
}

// bisectModules cuts a node's modules into two halves by bytes.
func bisectModules(mods []string, p *DevPlan) [][]string {
	total := 0
	for _, m := range mods {
		total += p.Modules[m].Bytes
	}
	var a, b []string
	acc := 0
	for _, m := range mods {
		if acc*2 < total && len(a) < len(mods)-1 {
			a = append(a, m)
			acc += p.Modules[m].Bytes
		} else {
			b = append(b, m)
		}
	}
	return [][]string{a, b}
}

func bisectIdents(ids []string) [][]string {
	h := len(ids) / 2
	return [][]string{append([]string(nil), ids[:h]...), append([]string(nil), ids[h:]...)}
}

// parseItemGroups reads `group: a, b, c` lines, keeping only known items, each
// once; items nobody claimed go to the last group.
func parseItemGroups(out string, idents []string) [][]string {
	known := map[string]bool{}
	for _, id := range idents {
		known[id] = true
	}
	claimed := map[string]bool{}
	var groups [][]string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "group:") {
			continue
		}
		var g []string
		for _, f := range strings.Split(line[len("group:"):], ",") {
			f = strings.Trim(strings.TrimSpace(f), "`*")
			if known[f] && !claimed[f] {
				claimed[f] = true
				g = append(g, f)
			}
		}
		if len(g) > 0 {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		return nil
	}
	var rest []string
	for _, id := range idents {
		if !claimed[id] {
			rest = append(rest, id)
		}
	}
	if len(rest) > 0 {
		last := len(groups) - 1
		groups[last] = append(groups[last], rest...)
	}
	// keep every group's items in theory order
	pos := map[string]int{}
	for i, id := range idents {
		pos[id] = i
	}
	for _, g := range groups {
		sort.SliceStable(g, func(i, j int) bool { return pos[g[i]] < pos[g[j]] })
	}
	return groups
}
