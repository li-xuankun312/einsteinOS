package kernel

// conv_tree.go — conversations as a tree, kept across restarts.
//
// ConvRecord used to know only ParentPid (the kernel's process number), so "the
// conversations this conversation started" had nowhere to live, and the whole
// registry was an in-memory slice that a kernel restart emptied. A conversation
// can now start others (spawn.go); the registry records the EDGE (ParentConv),
// answers "everything below this conversation" (ConvManifest), lets the kernel
// wait for a whole subtree before it judges a unit (WaitConvTree), and is
// written to disk on every change so a restart does not lose the tree.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// convKindForRole classifies a conversation by the job its role names. Judge and
// plan conversations decide and return; they are leaves of the tree.
func convKindForRole(role string) string {
	switch {
	case role == "dev_planner" || role == "theory_planner" || strings.HasPrefix(role, "spawn_plan"):
		return "plan"
	case role == "review_conv" || role == "dir_survey" || role == "file_survey" ||
		strings.HasPrefix(role, "review_unit") || strings.HasPrefix(role, "spawn_judge"):
		return "judge"
	default:
		return "work"
	}
}

// convKindOf reads a record's kind, defaulting old rows (no Kind) from the role.
func convKindOf(r ConvRecord) string {
	if r.Kind != "" {
		return r.Kind
	}
	return convKindForRole(r.Role)
}

// mergedConvLocked returns ONE record per conversation: the registry holds two
// rows for most conversations (a generic one from TrackChildConv at fork time,
// before the role is known, and a role-specific one added later), so fields are
// merged — the specific role and any parent edge win. Caller holds the lock.
func mergedConvLocked(convID string) (ConvRecord, bool) {
	var out ConvRecord
	found := false
	for _, r := range convRegistry {
		if r.ConvID != convID {
			continue
		}
		if !found {
			out, found = r, true
			continue
		}
		if r.Role != "" && r.Role != "child" {
			out.Role = r.Role
		}
		if r.Repo != "" {
			out.Repo = r.Repo
		}
		if r.ParentConv != "" {
			out.ParentConv = r.ParentConv
		}
		if r.Kind != "" && r.Kind != "work" {
			out.Kind = r.Kind
		}
		if r.Phase != 0 {
			out.Phase = r.Phase
		}
		out.Status = r.Status // later rows are newer
		if out.Created.After(r.Created) {
			out.Created = r.Created
		}
	}
	return out, found
}

// RegisterConvChild records a conversation that another conversation started.
func RegisterConvChild(parentConv, convID, repo, role, kind string, parentPid int64) {
	convRegistryMu.Lock()
	convRegistry = append(convRegistry, ConvRecord{
		ConvID:     convID,
		ParentPid:  parentPid,
		Repo:       repo,
		Role:       role,
		Status:     "running",
		Created:    time.Now(),
		ParentConv: parentConv,
		Kind:       kind,
	})
	persistConvRegistryLocked()
	convRegistryMu.Unlock()
	log.Printf("conv_registry: %s started %s role=%s kind=%s", shortID(parentConv), shortID(convID), role, kind)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "-"
	}
	return id
}

// ConvNode is one conversation and everything below it.
type ConvNode struct {
	Addr     string     `json:"addr"`
	Role     string     `json:"role"`
	Kind     string     `json:"kind"`
	Status   string     `json:"status"`
	Children []ConvNode `json:"children,omitempty"`
}

// ConvManifest returns the conversation at root and all its descendants. The
// answer to "what did this conversation start" is a LIST OF ADDRESSES with their
// states, not text: the work itself is on the branch.
func ConvManifest(root string) ConvNode {
	convRegistryMu.Lock()
	defer convRegistryMu.Unlock()
	return manifestLocked(root, map[string]bool{})
}

func manifestLocked(addr string, seen map[string]bool) ConvNode {
	rec, _ := mergedConvLocked(addr)
	n := ConvNode{Addr: addr, Role: rec.Role, Kind: convKindOf(rec), Status: rec.Status}
	if n.Status == "" {
		n.Status = "unknown"
	}
	if seen[addr] {
		return n // a cycle in the registry must not hang the kernel
	}
	seen[addr] = true
	var kids []string
	have := map[string]bool{}
	for _, r := range convRegistry {
		if r.ParentConv == addr && !have[r.ConvID] {
			have[r.ConvID] = true
			kids = append(kids, r.ConvID)
		}
	}
	for _, k := range kids {
		n.Children = append(n.Children, manifestLocked(k, seen))
	}
	return n
}

// Flatten lists every descendant (not the root), depth first.
func (n ConvNode) Flatten() []ConvNode {
	var out []ConvNode
	for _, c := range n.Children {
		out = append(out, c)
		out = append(out, c.Flatten()...)
	}
	return out
}

// Running counts descendants that have not finished.
func (n ConvNode) Running() int {
	r := 0
	for _, c := range n.Flatten() {
		if c.Status == "running" || c.Status == "unknown" {
			r++
		}
	}
	return r
}

// ConvDepth is how many parent edges lie above convID.
func ConvDepth(convID string) int {
	convRegistryMu.Lock()
	defer convRegistryMu.Unlock()
	d := 0
	seen := map[string]bool{}
	for cur := convID; !seen[cur]; {
		seen[cur] = true
		rec, ok := mergedConvLocked(cur)
		if !ok || rec.ParentConv == "" {
			break
		}
		d++
		cur = rec.ParentConv
	}
	return d
}

// convTreeRoot walks up to the conversation the kernel itself forked.
func convTreeRoot(convID string) string {
	convRegistryMu.Lock()
	defer convRegistryMu.Unlock()
	seen := map[string]bool{}
	cur := convID
	for !seen[cur] {
		seen[cur] = true
		rec, ok := mergedConvLocked(cur)
		if !ok || rec.ParentConv == "" {
			return cur
		}
		cur = rec.ParentConv
	}
	return cur
}

// WaitConvTree blocks until nothing below root is running, or max passes.
// dev_from_theorems calls it after a unit's own conversation ends: that
// conversation may have started helpers, and verifying (or reviewing) before they
// finish would judge half a unit. Reports whether everything finished.
func WaitConvTree(root string, max, poll time.Duration) bool {
	deadline := time.Now().Add(max)
	for {
		if ConvManifest(root).Running() == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(poll)
	}
}

// ---- results of spawned conversations ---------------------------------------------

var convResults = map[string]string{}

// SetConvResult stores the final text of a conversation the kernel is not going
// to Wait on itself (a spawned one).
func SetConvResult(convID, text string) {
	convRegistryMu.Lock()
	convResults[convID] = text
	persistConvRegistryLocked()
	convRegistryMu.Unlock()
}

// GetConvResult returns the stored final text and whether there is one.
func GetConvResult(convID string) (string, bool) {
	convRegistryMu.Lock()
	defer convRegistryMu.Unlock()
	t, ok := convResults[convID]
	return t, ok
}

// ---- persistence --------------------------------------------------------------------

var convRegistryPath string

// SetConvRegistryPath turns on persistence: the registry is loaded from path
// (if the file exists) and rewritten after every change.
func SetConvRegistryPath(path string) {
	convRegistryMu.Lock()
	convRegistryPath = path
	convRegistryMu.Unlock()
}

type convRegistryFile struct {
	Version int               `json:"version"`
	Records []ConvRecord      `json:"records"`
	Results map[string]string `json:"results"`
}

// persistConvRegistryLocked writes the registry. Caller holds convRegistryMu.
// A write failure is logged, never fatal: the registry is an index, the work is
// on the branch.
func persistConvRegistryLocked() {
	if convRegistryPath == "" {
		return
	}
	b, err := json.Marshal(convRegistryFile{Version: 1, Records: convRegistry, Results: convResults})
	if err != nil {
		log.Printf("conv_registry: marshal: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(convRegistryPath), 0o755); err != nil {
		log.Printf("conv_registry: %v", err)
		return
	}
	tmp := convRegistryPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("conv_registry: write: %v", err)
		return
	}
	if err := os.Rename(tmp, convRegistryPath); err != nil {
		log.Printf("conv_registry: rename: %v", err)
	}
}

// LoadConvRegistry reads a persisted registry. A conversation recorded as
// running belongs to a process that no longer exists: it is marked "lost", so
// nothing waits on it forever.
func LoadConvRegistry(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var f convRegistryFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	convRegistryMu.Lock()
	defer convRegistryMu.Unlock()
	convRegistry = f.Records
	for i := range convRegistry {
		if convRegistry[i].Status == "running" {
			convRegistry[i].Status = "lost"
		}
	}
	if f.Results != nil {
		convResults = f.Results
	}
	return nil
}

// sortedStatuses is for tests and /status dumps.
func sortedStatuses(n ConvNode) []string {
	var out []string
	for _, c := range n.Flatten() {
		out = append(out, shortID(c.Addr)+":"+c.Status)
	}
	sort.Strings(out)
	return out
}
