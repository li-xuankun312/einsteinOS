package kernel

// dev_plan_store.go — the dev plan as a persistent, stable object.
//
// Why this exists (Strata run 2026/11/01): dev_plan ran an LLM planner on EVERY
// entry into dev_from_theorems, and the function is re-entered after every
// review FAIL / fix. Three entries gave three different partitions of the same
// 31 modules (unit 1 held different modules each time; the third plan had a
// 26-module unit and a 0-byte unit), so anything keyed by unit NUMBER (the
// fix-attempt counter, the PASS marker) pointed at a different set of modules
// after every re-plan.
//
// The plan is now generated ONCE and stored on the dev branch
// (.einsteinOS/plan.json). Its identity is (ref commit, theory content hash).
// A unit's identity is the SET OF MODULE NAMES it covers (plus an optional
// scope inside a module), never its position. Pass state lives on the modules
// (or module#scope), so it survives anything that reshuffles units. The plan
// changes only by an explicit action: a module split into sub-units (a node
// that outgrew a session), or new/changed modules after the theory changed.
// Modules that already passed keep their state untouched.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
)

const (
	devPlanFile    = ".einsteinOS/plan.json"
	devPlanVersion = 1

	// DevUnitMaxTheoryBytes: the most theory one dev child should be asked to
	// digest. From the Strata run: units of 41-54 KB (8 modules) produced files;
	// units of 103/161/199 KB never did. Overridable per run with
	// EINSTEINOS_DEV_UNIT_BYTES (see devUnitBudget).
	DevUnitMaxTheoryBytes = 64 * 1024
	// DevUnitMaxModules: a unit with more modules than this is too wide for one
	// session regardless of bytes (the third Strata plan had a 26-module unit).
	DevUnitMaxModules = 10
	// DevMaxFixAttempts: fix attempts per UNIT (module set) before the unit is
	// split instead of retried.
	DevMaxFixAttempts = 3
)

// DevModule is one theory module as found in the theory text.
type DevModule struct {
	Name  string `json:"name"` // canonical section key, exactly as in the theory
	Hash  string `json:"hash"` // content hash of the section
	Bytes int    `json:"bytes"`
}

// DevModuleState is what is known about a module (or module#scope) so far.
type DevModuleState struct {
	Hash   string `json:"hash"`              // content hash when it passed / was last seen
	Pass   bool   `json:"pass"`              // unit containing it passed verify + review
	UnitID string `json:"unit_id,omitempty"` // the unit that passed it
}

// DevPlanNode is one node of the plan tree. Only leaves are executed; a node
// that was split keeps its place and lists its children.
type DevPlanNode struct {
	ID       string   `json:"id"`
	Parent   string   `json:"parent,omitempty"`
	Modules  []string `json:"modules"`
	Scope    string   `json:"scope,omitempty"` // sub-module scope (theorem group); "" = whole modules
	Children []string `json:"children,omitempty"`
	Reason   string   `json:"reason,omitempty"` // why it was split / created
	Conv     string   `json:"conv,omitempty"`   // the conversation that passed it (an address, not text)
	// Sources: a port unit's reference files (no theory module covers them); its pass is
	// recorded in DevPlan.PortPass
	Sources []string `json:"sources,omitempty"`
	// Integration: the commit's last unit, judged by the reference's registered tests
	Integration bool `json:"integration,omitempty"`
}

// DevFailure is why a unit's last attempt did not pass, and where its code is,
// so a re-entry (or a restart) resumes the SAME unit from the right branch with
// the real error in hand instead of starting over.
type DevFailure struct {
	Kind    string `json:"kind"`     // compile_fail | smoke_fail | missing | review_fail | nothing_written
	Text    string `json:"text"`     // the build/test output tail, or the reviewer's reason
	BaseRef string `json:"base_ref"` // branch holding the code to continue from (dev branch or the unit's wip branch)
	// Attribution: for a deferred unit's review failure, what the kernel measured about whose
	// code it faults (deferredFailure)
	Attribution string `json:"attribution,omitempty"`
	// Copies: for a review failure, the files on the dev branch that define a name the verdict
	// says, where there is more than one (review_copies.go)
	Copies string `json:"copies,omitempty"`
}

// DevPlan is the persisted plan.
type DevPlan struct {
	// Holder: the run that last stored the plan — the dev branch is that run's (dev_lease.go)
	Holder      DevHolder                 `json:"holder"`
	Version     int                       `json:"version"`
	Ref         string                    `json:"ref"`
	RefSHA      string                    `json:"ref_sha"`
	TheoryHash  string                    `json:"theory_hash"`
	Modules     map[string]DevModule      `json:"modules"`
	Nodes       []DevPlanNode             `json:"nodes"` // creation order; Leaves() gives execution order
	State       map[string]DevModuleState `json:"state"`
	FixAttempts map[string]int            `json:"fix_attempts"` // unit id -> attempts
	// FailHistory: every failed attempt's kind, per unit, kept after the unit passes —
	// FixAttempts is cleared on a pass, so the 11/03 summary said fix=0 for all ten units
	// while units 3, 4 and 5 had each needed a second attempt.
	FailHistory map[string][]string   `json:"fail_history,omitempty"`
	LastFailure map[string]DevFailure `json:"last_failure"` // unit id -> why the last attempt failed
	InfraFails  int                   `json:"infra_fails"`  // consecutive git/push failures (not the child's fault)
	// ReviewPending: units whose code passed the real-machine verification and is on the dev
	// branch, but whose review has not answered yet — unit id -> the verified status. A run
	// that stops here (11/04 10:16:54: u9dc1be2eae pushed at 10:16:47, Ctrl+C before its
	// review) resumes with the review instead of writing and verifying the unit again.
	ReviewPending map[string]map[string]string `json:"review_pending,omitempty"`
	// Reopen: a passed unit a later review faulted for one of its files — unit id -> what
	// the reviewer said and which unit it was reviewing (review_owner.go). The unit runs
	// again after the units already queued, with that text as its failure.
	Reopen map[string]DevFailure `json:"reopen,omitempty"`
	// ReopenCount: how often each unit has been reopened (devMaxReopens bounds it)
	ReopenCount map[string]int `json:"reopen_count,omitempty"`
	// Deferred: units that used their fix attempts and have nothing to split — unit id ->
	// the last thing a review said about them. They are not done: their files are carried
	// to the integration unit, which runs the port on the model end to end.
	Deferred map[string]DevFailure `json:"deferred,omitempty"`
	// PortUnits: one unit per reference directory whose code files no theory unit covers;
	// 11/04 the theory covered 58 of the commit's files and the rest had no unit at all.
	PortUnits []DevPlanNode   `json:"port_units,omitempty"`
	PortPass  map[string]bool `json:"port_pass,omitempty"`
	// VerifySeen: units one of whose conversations saw its verify scripts run here
	VerifySeen map[string]bool `json:"verify_seen,omitempty"`
	// Gates: the gate set the recorded passes were judged by (devGateSet)
	Gates string `json:"gates,omitempty"`
	// IntegrationUnit: judged by the reference's own registered tests (integration.go)
	IntegrationUnit *DevPlanNode `json:"integration_unit,omitempty"`

	// silent counts, per unit, the conversations IN A ROW that wrote no file. In memory
	// only: a restart is a person deciding to try again, and starts the count over.
	silent map[string]int
}

// ---- identity --------------------------------------------------------------

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:12]
}

// devTheoryHash hashes the module names and contents in a canonical order, so
// it does not depend on where sections sit in the theory file.
func devTheoryHash(mods []DevModule) string {
	names := make([]string, 0, len(mods))
	byName := map[string]DevModule{}
	for _, m := range mods {
		names = append(names, m.Name)
		byName[m.Name] = m
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(0)
		b.WriteString(byName[n].Hash)
		b.WriteByte(0)
	}
	return shortHash(b.String())
}

// devModulesFromSections turns parsed theory sections into DevModules.
func devModulesFromSections(sections []devFileSection) []DevModule {
	out := make([]DevModule, 0, len(sections))
	seen := map[string]bool{}
	for _, s := range sections {
		name := strings.TrimSpace(s.path)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, DevModule{Name: name, Hash: shortHash(s.content), Bytes: len(s.content)})
	}
	return out
}

// devUnitID is the identity of a unit: the sorted set of its module names and
// its scope. The same modules always give the same ID, whatever the order the
// planner listed them in and whatever position the unit ends up at.
func devUnitID(modules []string, scope string) string {
	ms := append([]string(nil), modules...)
	sort.Strings(ms)
	return "u" + shortHash(strings.Join(ms, "\x00") + "\x01" + scope)[:10]
}

// stateKey is the key of one module (or module#scope) in DevPlan.State.
func stateKey(module, scope string) string {
	if scope == "" {
		return module
	}
	return module + "#" + scope
}

// ---- planner-name matching ---------------------------------------------------

func normModuleName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`*")
	if i := strings.Index(s, "("); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".lean")
	s = strings.TrimPrefix(s, "/mnt/user-data/outputs/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// matchModuleName maps a name the planner wrote to a canonical module name.
// Exact match on the canonical name wins; then the normalized form (case,
// ".lean", parenthetical and directory stripped); then a UNIQUE prefix or
// substring. An ambiguous or unknown name does not match — the old matcher
// returned whichever map entry Go iterated first, which silently claimed the
// wrong module.
func matchModuleName(name string, canon []string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	for _, c := range canon {
		if c == name {
			return c, true
		}
	}
	n := normModuleName(name)
	if n == "" {
		return "", false
	}
	var exact []string
	for _, c := range canon {
		if normModuleName(c) == n {
			exact = append(exact, c)
		}
	}
	if len(exact) == 1 {
		return exact[0], true
	}
	if len(exact) > 1 {
		return "", false
	}
	var pre, sub []string
	for _, c := range canon {
		cn := normModuleName(c)
		if strings.HasPrefix(cn, n) {
			pre = append(pre, c)
		}
		if strings.Contains(cn, n) {
			sub = append(sub, c)
		}
	}
	if len(pre) == 1 {
		return pre[0], true
	}
	if len(pre) == 0 && len(sub) == 1 {
		return sub[0], true
	}
	return "", false
}

// parseDevPlanOutput reads "unit: a, b, c" lines. It returns the groups made of
// CANONICAL names only (each module claimed once, first claim wins), the names
// it could not match, and the canonical modules nobody claimed. A line that
// matched nothing is dropped: that was the 0-byte unit of the third Strata plan.
func parseDevPlanOutput(output string, mods []DevModule) (groups [][]string, unmatched, unclaimed []string) {
	canon := make([]string, 0, len(mods))
	for _, m := range mods {
		canon = append(canon, m.Name)
	}
	claimed := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		low := strings.ToLower(line)
		if !strings.HasPrefix(low, "unit:") {
			continue
		}
		rest := strings.TrimSpace(line[len("unit:"):])
		var g []string
		for _, f := range strings.Split(rest, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			c, ok := matchModuleName(f, canon)
			if !ok {
				unmatched = append(unmatched, f)
				continue
			}
			if claimed[c] {
				continue
			}
			claimed[c] = true
			g = append(g, c)
		}
		if len(g) > 0 {
			groups = append(groups, g)
		}
	}
	for _, c := range canon {
		if !claimed[c] {
			unclaimed = append(unclaimed, c)
		}
	}
	return groups, unmatched, unclaimed
}

// ---- budget ----------------------------------------------------------------

// rebalanceGroups enforces the unit budget on the planner's grouping: a group
// over maxBytes or maxModules is cut into consecutive pieces that fit. A single
// module that alone exceeds maxBytes cannot be cut here (module is the grain of
// a group); it is returned in oversize so the caller can have a planner split it
// BELOW module level (by theorem group).
func rebalanceGroups(groups [][]string, size map[string]int, maxBytes, maxModules int) (out [][]string, oversize []string) {
	for _, g := range groups {
		var cur []string
		curBytes := 0
		flush := func() {
			if len(cur) > 0 {
				out = append(out, cur)
				cur, curBytes = nil, 0
			}
		}
		for _, m := range g {
			sz := size[m]
			if sz > maxBytes {
				flush()
				out = append(out, []string{m})
				oversize = append(oversize, m)
				continue
			}
			if len(cur) > 0 && (curBytes+sz > maxBytes || len(cur) >= maxModules) {
				flush()
			}
			cur = append(cur, m)
			curBytes += sz
		}
		flush()
	}
	return out, oversize
}

// ---- construction and queries -------------------------------------------------

// newDevPlan builds a plan from groups of canonical module names.
func newDevPlan(ref, refSHA string, mods []DevModule, groups [][]string, reason string) *DevPlan {
	p := &DevPlan{
		Version:     devPlanVersion,
		Ref:         ref,
		RefSHA:      refSHA,
		TheoryHash:  devTheoryHash(mods),
		Modules:     map[string]DevModule{},
		State:       map[string]DevModuleState{},
		FixAttempts: map[string]int{},
		LastFailure: map[string]DevFailure{},
	}
	for _, m := range mods {
		p.Modules[m.Name] = m
	}
	p.addGroups(groups, reason)
	return p
}

func (p *DevPlan) addGroups(groups [][]string, reason string) {
	for _, g := range groups {
		if len(g) == 0 {
			continue
		}
		id := devUnitID(g, "")
		if p.node(id) != nil {
			continue
		}
		p.Nodes = append(p.Nodes, DevPlanNode{ID: id, Modules: append([]string(nil), g...), Reason: reason})
	}
}

func (p *DevPlan) node(id string) *DevPlanNode {
	for i := range p.Nodes {
		if p.Nodes[i].ID == id {
			return &p.Nodes[i]
		}
	}
	return nil
}

// Leaves returns the executable nodes in execution order. A split node is
// replaced, in place, by its children.
func (p *DevPlan) Leaves() []DevPlanNode {
	var out []DevPlanNode
	var walk func(n DevPlanNode)
	walk = func(n DevPlanNode) {
		if len(n.Children) == 0 {
			out = append(out, n)
			return
		}
		for _, cid := range n.Children {
			if c := p.node(cid); c != nil {
				walk(*c)
			}
		}
	}
	for _, n := range p.Nodes {
		if n.Parent == "" {
			walk(n)
		}
	}
	out = append(out, p.PortUnits...)
	if p.IntegrationUnit != nil {
		out = append(out, *p.IntegrationUnit)
	}
	return out
}

// SetIntegrationUnit makes the commit's integration unit the last unit of the plan.
func (p *DevPlan) SetIntegrationUnit(id string) bool {
	if p.IntegrationUnit != nil && p.IntegrationUnit.ID == id {
		return false
	}
	p.IntegrationUnit = &DevPlanNode{ID: id, Integration: true, Reason: "integration: the reference's registered tests"}
	return true
}

// AddPortUnits adds a unit for each reference directory with code files that covered does
// not contain, and returns how many it added.
func (p *DevPlan) AddPortUnits(all []string, covered map[string]bool) int {
	byDir := map[string][]string{}
	var dirs []string
	for _, f := range all {
		if covered[f] {
			continue
		}
		d := path.Dir(f)
		if _, ok := byDir[d]; !ok {
			dirs = append(dirs, d)
		}
		byDir[d] = append(byDir[d], f)
	}
	sort.Strings(dirs)
	have := map[string]bool{}
	for _, u := range p.PortUnits {
		have[u.ID] = true
	}
	added := 0
	for _, d := range dirs {
		id := "p" + devUnitID(byDir[d], "port")[1:]
		if have[id] {
			continue
		}
		p.PortUnits = append(p.PortUnits, DevPlanNode{ID: id, Sources: byDir[d], Reason: "port: reference files of " + d + " no theory unit covers"})
		added++
	}
	return added
}

func (p *DevPlan) keysOf(n DevPlanNode) []string {
	keys := make([]string, 0, len(n.Modules))
	for _, m := range n.Modules {
		keys = append(keys, stateKey(m, n.Scope))
	}
	return keys
}

// UnitPass reports whether every module of the node has passed AND the module
// content is the one that passed (a module whose theory changed has to be done
// again).
func (p *DevPlan) UnitPass(n DevPlanNode) bool {
	if p.Reopened(n.ID) {
		return false
	}
	if _, deferred := p.Deferred[n.ID]; deferred {
		return true
	}
	if len(n.Sources) > 0 || n.Integration {
		return p.PortPass[n.ID]
	}
	for _, m := range n.Modules {
		st, ok := p.State[stateKey(m, n.Scope)]
		if !ok || !st.Pass {
			return false
		}
		if mod, ok := p.Modules[m]; ok && st.Hash != "" && st.Hash != mod.Hash {
			return false
		}
	}
	return true
}

// MarkUnitPass records the pass on the modules.
// MarkReviewPending records that unit id's verified code is on the dev branch and its review
// has not answered yet.
func (p *DevPlan) MarkReviewPending(id string, status map[string]string) {
	if p.ReviewPending == nil {
		p.ReviewPending = map[string]map[string]string{}
	}
	keep := map[string]string{}
	for _, k := range []string{"compile", "smoke", "existing_code"} {
		if v := status[k]; v != "" {
			keep[k] = v
		}
	}
	p.ReviewPending[id] = keep
}

// TakeReviewPending returns and clears unit id's pending review, if any.
func (p *DevPlan) TakeReviewPending(id string) (map[string]string, bool) {
	st, ok := p.ReviewPending[id]
	delete(p.ReviewPending, id)
	return st, ok
}

func (p *DevPlan) MarkUnitPass(n DevPlanNode) {
	p.withdrawIntegrationPass(n)
	if len(n.Sources) > 0 || n.Integration {
		if p.PortPass == nil {
			p.PortPass = map[string]bool{}
		}
		p.PortPass[n.ID] = true
		return
	}
	for _, m := range n.Modules {
		p.State[stateKey(m, n.Scope)] = DevModuleState{Hash: p.Modules[m].Hash, Pass: true, UnitID: n.ID}
	}
	delete(p.FixAttempts, n.ID)
	delete(p.LastFailure, n.ID)
	p.ClearSilent(n.ID)
	p.setConv(n.ID, "")
}

// withdrawIntegrationPass: a unit that passes after the integration unit changed the code the
// integration judged, so the integration unit runs again. 11/10 23:35 the integration passed on
// the tree before the fixes of the reopened units uf4e6ff5513, uf9f69c2c43, ua9684a588f and
// u0063babba8, which passed after it.
func (p *DevPlan) withdrawIntegrationPass(n DevPlanNode) {
	if n.Integration || p.IntegrationUnit == nil || !p.PortPass[p.IntegrationUnit.ID] {
		return
	}
	delete(p.PortPass, p.IntegrationUnit.ID)
	log.Printf("dev_plan: unit %s passed after the integration unit %s — its pass is withdrawn and it runs again", n.ID, p.IntegrationUnit.ID)
}

func (p *DevPlan) setConv(id, conv string) {
	for i := range p.Nodes {
		if p.Nodes[i].ID == id && conv != "" {
			p.Nodes[i].Conv = conv
		}
	}
}

// SetConv records the conversation that did (or is doing) the unit.
func (p *DevPlan) SetConv(id, conv string) { p.setConv(id, conv) }

// Fail records a failed attempt: counts it and remembers why.
func (p *DevPlan) Fail(id string, f DevFailure) int {
	if p.LastFailure == nil {
		p.LastFailure = map[string]DevFailure{}
	}
	p.LastFailure[id] = f
	if p.FailHistory == nil {
		p.FailHistory = map[string][]string{}
	}
	p.FailHistory[id] = append(p.FailHistory[id], f.Kind)
	// a failure that measured the machine is not an attempt of the unit's: it does not count
	// toward the fix attempts that split it. 11/07 12:05 unit u5c4a635e52 was split as "too
	// big for one session (3 fix attempts used)" — all three were
	// build_toolchain_dependence_unmeasured, each with compile, smoke and launches passing.
	if gateIsMachine(f.Kind) {
		return p.FixAttempts[id]
	}
	return p.BumpFix(id)
}

// NoteSilent records that a conversation for this unit wrote nothing, and returns how
// many in a row have.
func (p *DevPlan) NoteSilent(id string) int {
	if p.silent == nil {
		p.silent = map[string]int{}
	}
	p.silent[id]++
	return p.silent[id]
}

// ClearSilent ends the streak: the unit's last conversation wrote something (or passed).
func (p *DevPlan) ClearSilent(id string) { delete(p.silent, id) }

// Silent is the current streak.
func (p *DevPlan) Silent(id string) int { return p.silent[id] }

// BumpFix counts a failed attempt for the unit and returns the new count.
func (p *DevPlan) BumpFix(id string) int {
	if p.FixAttempts == nil {
		p.FixAttempts = map[string]int{}
	}
	p.FixAttempts[id]++
	return p.FixAttempts[id]
}

// UnitTheory assembles the theory text of a node from the theory sections.
func (p *DevPlan) UnitTheoryBytes(n DevPlanNode) int {
	t := 0
	for _, m := range n.Modules {
		t += p.Modules[m].Bytes
	}
	return t
}

// Split replaces node id by children, one per group. Each child keeps the
// parent's modules it is given and the given scope label. Returns the children.
func (p *DevPlan) Split(id string, groups [][]string, scopes []string, reason string) []DevPlanNode {
	parent := p.node(id)
	if parent == nil {
		return nil
	}
	var kids []DevPlanNode
	for i, g := range groups {
		if len(g) == 0 {
			continue
		}
		scope := ""
		if i < len(scopes) {
			scope = scopes[i]
		}
		if scope == "" {
			scope = parent.Scope
		}
		cid := devUnitID(g, scope)
		if cid == id || p.node(cid) != nil {
			continue
		}
		kids = append(kids, DevPlanNode{ID: cid, Parent: id, Modules: append([]string(nil), g...), Scope: scope, Reason: reason})
	}
	if len(kids) < 2 {
		return nil // a split that does not split is not a split
	}
	for _, k := range kids {
		p.Nodes = append(p.Nodes, k)
		parent = p.node(id)
		parent.Children = append(parent.Children, k.ID)
	}
	delete(p.FixAttempts, id)
	delete(p.LastFailure, id)
	return kids
}

// Reconcile brings a stored plan in line with the CURRENT theory without
// re-planning what is already planned:
//   - modules that vanished are removed from their nodes (empty nodes drop);
//   - modules whose content changed lose their PASS (they must be redone);
//   - modules the plan has never seen are returned in added, for the caller to
//     group into NEW units appended after the existing ones.
//
// Everything that passed and did not change is left exactly as it was.
func (p *DevPlan) Reconcile(refSHA string, mods []DevModule) (added []string, changed bool) {
	cur := map[string]DevModule{}
	for _, m := range mods {
		cur[m.Name] = m
	}
	for name := range p.Modules {
		if _, ok := cur[name]; !ok {
			delete(p.Modules, name)
			for k := range p.State {
				if k == name || strings.HasPrefix(k, name+"#") {
					delete(p.State, k)
				}
			}
			changed = true
		}
	}
	// drop vanished modules from nodes
	var nodes []DevPlanNode
	for _, n := range p.Nodes {
		var keep []string
		for _, m := range n.Modules {
			if _, ok := cur[m]; ok {
				keep = append(keep, m)
			}
		}
		if len(keep) != len(n.Modules) {
			changed = true
		}
		if len(keep) == 0 {
			continue
		}
		n.Modules = keep
		nodes = append(nodes, n)
	}
	p.Nodes = nodes
	// fix dangling child links
	for i := range p.Nodes {
		var kids []string
		for _, c := range p.Nodes[i].Children {
			if p.node(c) != nil {
				kids = append(kids, c)
			}
		}
		p.Nodes[i].Children = kids
	}
	inPlan := map[string]bool{}
	for _, n := range p.Nodes {
		for _, m := range n.Modules {
			inPlan[m] = true
		}
	}
	for _, m := range mods {
		old, known := p.Modules[m.Name]
		p.Modules[m.Name] = m
		if known && old.Hash != m.Hash {
			changed = true
			for k, st := range p.State {
				if (k == m.Name || strings.HasPrefix(k, m.Name+"#")) && st.Pass {
					st.Pass = false
					p.State[k] = st
				}
			}
		}
		if !inPlan[m.Name] {
			added = append(added, m.Name)
		}
	}
	if p.RefSHA != refSHA {
		p.RefSHA = refSHA
		changed = true
	}
	th := devTheoryHash(mods)
	if p.TheoryHash != th {
		p.TheoryHash = th
		changed = true
	}
	return added, changed
}

// AllPass reports whether every leaf passed.
func (p *DevPlan) AllPass() bool {
	for _, l := range p.Leaves() {
		if !p.UnitPass(l) {
			return false
		}
	}
	return len(p.Leaves()) > 0
}

// Summary is a one-line-per-leaf view for logs.
func (p *DevPlan) Summary() string {
	var b strings.Builder
	for i, l := range p.Leaves() {
		st := "todo"
		if p.UnitPass(l) {
			st = "PASS"
		}
		scope := ""
		if l.Scope != "" {
			scope = " scope=" + truncStr(l.Scope, 40)
		}
		hist := p.FailHistory[l.ID]
		fails := ""
		if len(hist) > 0 {
			fails = " failed: " + strings.Join(hist, ",")
		}
		fmt.Fprintf(&b, "  unit %d id=%s %s modules=%d bytes=%d%s fix=%d attempts=%d%s\n",
			i+1, l.ID, st, len(l.Modules), p.UnitTheoryBytes(l), scope, p.FixAttempts[l.ID], len(hist)+1, fails)
	}
	return b.String()
}

// ---- (de)serialization -------------------------------------------------------

func (p *DevPlan) Marshal() ([]byte, error) { return json.MarshalIndent(p, "", "  ") }

func unmarshalDevPlan(b []byte) (*DevPlan, error) {
	var p DevPlan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if p.Version != devPlanVersion {
		return nil, fmt.Errorf("plan.json version %d, this kernel reads %d", p.Version, devPlanVersion)
	}
	if p.Modules == nil {
		p.Modules = map[string]DevModule{}
	}
	if p.State == nil {
		p.State = map[string]DevModuleState{}
	}
	if p.FixAttempts == nil {
		p.FixAttempts = map[string]int{}
	}
	if p.LastFailure == nil {
		p.LastFailure = map[string]DevFailure{}
	}
	return &p, nil
}

// devGateSet names what a pass means. A plan whose passes were judged by another set is
// judged again: 11/04 u89be81de0b and u0e60a9f272 passed before the port gate existed.
const devGateSet = "build+test+gpu+isa+port+integration"

// RejudgeIfGatesChanged clears the passes recorded under another gate set and reports how
// many units that reopened.
func (p *DevPlan) RejudgeIfGatesChanged() int {
	if p.Gates == devGateSet {
		return 0
	}
	n := 0
	for _, l := range p.Leaves() {
		if p.UnitPass(l) {
			n++
		}
	}
	for k, st := range p.State {
		st.Pass = false
		p.State[k] = st
	}
	p.PortPass = map[string]bool{}
	p.Gates = devGateSet
	return n
}

// MarkReopen records that a later review faulted a passed unit for one of its files: the unit
// is to run again, with f as its failure. The unit under review is not held to answer for it.
func (p *DevPlan) MarkReopen(id string, f DevFailure) {
	if p.Reopen == nil {
		p.Reopen = map[string]DevFailure{}
	}
	if _, already := p.Reopen[id]; already {
		return
	}
	if p.ReopenCount == nil {
		p.ReopenCount = map[string]int{}
	}
	p.Reopen[id] = f
	p.ReopenCount[id]++
}

// TakeReopen returns and clears a unit's reopen, if any.
//
// Taking it withdraws the unit's pass: until its fix passes the unit has not passed. 11/11
// 01:05 unit u0063babba8's reopen was taken and its fix conversation started; the run stopped,
// and at 05:48 the resumed run found the reopen gone and the pass of 11/10 06:46 still
// stored, and counted the unit as passed with no fix made.
func (p *DevPlan) TakeReopen(id string) (DevFailure, bool) {
	f, ok := p.Reopen[id]
	delete(p.Reopen, id)
	if ok {
		p.withdrawPass(id)
	}
	return f, ok
}

// withdrawPass: the unit id has no pass stored (port units and module units alike).
func (p *DevPlan) withdrawPass(id string) {
	delete(p.PortPass, id)
	for k, st := range p.State {
		if st.UnitID == id && st.Pass {
			st.Pass = false
			p.State[k] = st
		}
	}
}

// Reopened reports whether a unit is to run again because of another unit's review.
func (p *DevPlan) Reopened(id string) bool { _, ok := p.Reopen[id]; return ok }

// devMaxReopens: how often a unit is reopened by other units' reviews before its files are
// left to the integration unit, which runs the port on the model end to end. Without a limit
// two units whose reviews fault each other's files would reopen each other without end.
const devMaxReopens = 2

// ReopenableUnits: of ids, the units of this plan that have not reached devMaxReopens. A unit
// that is not a leaf of the plan never runs, so reopening it would only let the reviewed unit
// pass: 11/10 14:36 p81030368e8 passed on the reopen of four units of earlier plans, and the
// bug its review named stayed. With none of ids reopenable the verdict is the reviewed unit's.
// (A plan without a tree, as tests build it, takes ids as they are.)
func (p *DevPlan) ReopenableUnits(ids []string) []string {
	leaves := map[string]bool{}
	for _, l := range p.Leaves() {
		leaves[l.ID] = true
	}
	var out []string
	for _, id := range ids {
		if len(leaves) > 0 && !leaves[id] {
			continue
		}
		if p.ReopenCount[id] < devMaxReopens {
			out = append(out, id)
		}
	}
	return out
}

// MarkDeferred records that a unit used its fix attempts and has nothing to split: the run
// goes on to the units behind it and the unit's files are left to the integration unit's
// end-to-end run. 11/08 unit pf7b804cf95, a port unit of three headers, ended four runs of
// the pipeline this way, each time before any later unit had been tried.
// deferredAttribution: a deferred unit's review failure was measured against the port maps on
// the dev branch before it was deferred — dev_from_theorems runs resumeStoredReviewFailure
// before the split question — and no other unit owns a file or a definition it names.
const deferredAttribution = "measured on the dev branch: no file and no definition this verdict names is another unit's"

// deferredFailure: the failure a unit is deferred with. A review failure carries the fact that
// attributing it to another unit was measured and found no one, so a deferral says whether
// it is this unit's own finding or one nobody could be held to — 11/08 pf7b804cf95 was
// deferred with a verdict about gr_norm, and only the log of a run could tell that the
// attribution had found nobody.
func deferredFailure(lf DevFailure) DevFailure {
	if lf.Kind == "review_fail" {
		lf.Attribution = deferredAttribution
	}
	return lf
}

func (p *DevPlan) MarkDeferred(id string, f DevFailure) {
	if p.Deferred == nil {
		p.Deferred = map[string]DevFailure{}
	}
	p.Deferred[id] = f
}

// DeferredUnits: the deferred units and what a review last said about each, by unit id order.
func (p *DevPlan) DeferredUnits() []string {
	var out []string
	for id := range p.Deferred {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
