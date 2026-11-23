package kernel

// mirror_writes.go — which conversation's command wrote which file of an integration unit's mirror,
// measured.
//
// 11/23 the integration conversation and its parts each ran in a sandbox of its own and each of
// their commands ran here again, on the one mirror they share: what any of them changed reached
// the mirror, and none of them saw what the others had changed. At 16:02:33 one deleted
// gdn_qwen.cu, at 16:07:27 another deleted gdn_layer.cu and at 16:10:45 a third tried to delete
// gdn_layer.cu again; the log said which pid ran each command and not whose command it was. The
// mirror's files are read before and after each command of these conversations, the commands
// run one at a time on it, and each file changed is set down to the conversation that changed it.

import (
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type mirrorStat struct {
	size  int64
	mtime int64
}

// statMirror: the mirror's files by size and modification time (.git and build output left out).
func statMirror(dir string) map[string]mirrorStat {
	out := map[string]mirrorStat{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "build", "_build", "__pycache__", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = mirrorStat{info.Size(), info.ModTime().UnixNano()}
		return nil
	})
	return out
}

// mirrorWrite: a file one command changed, and whose command it was.
type mirrorWrite struct {
	File, Conv string
	Removed    bool
	Seq        int
}

// mirrorUnit: the conversations working on one unit's mirror and what their commands wrote.
type mirrorUnit struct {
	mu     sync.Mutex // one command at a time on the mirror
	dir    string
	part   map[string]string // conv -> its part ("" for the unit's own conversation)
	owner  map[string]string // file -> the conversation that first wrote it in this attempt
	named  map[string]string // file -> the part the planner's partfile lines give it
	writes []mirrorWrite
	seq    int
	heard  map[string]int // conv -> the last write it was told of
}

var (
	mirrorUnitsMu sync.Mutex
	mirrorUnits   = map[string]*mirrorUnit{} // unit -> its mirror's record
	mirrorConvs   = map[string]string{}      // conv -> unit
)

// noteMirrorConv: conv works on unit's mirror dir, for part ("" for the unit's own conversation).
func noteMirrorConv(unitID, dir, conv, part string) {
	if unitID == "" || dir == "" || conv == "" {
		return
	}
	mirrorUnitsMu.Lock()
	defer mirrorUnitsMu.Unlock()
	u := mirrorUnits[unitID]
	if u == nil || u.dir != dir {
		u = &mirrorUnit{dir: dir, part: map[string]string{}, owner: map[string]string{}, named: map[string]string{}, heard: map[string]int{}}
		mirrorUnits[unitID] = u
	}
	u.part[conv] = part
	mirrorConvs[conv] = unitID
}

func mirrorUnitOf(conv string) *mirrorUnit {
	mirrorUnitsMu.Lock()
	defer mirrorUnitsMu.Unlock()
	if unit, ok := mirrorConvs[conv]; ok {
		return mirrorUnits[unit]
	}
	return nil
}

// %[1]s the conversation, %[2]s its part, %[3]d files changed, %[4]s them
const mirrorWroteFmt = "mirror: a command of %[1]s (%[2]s) changed %[3]d file(s) of the unit's mirror: %[4]s"

// MirrorCommand runs one tool call of conv; when conv works on an integration unit's shared
// mirror the call runs alone on it, and the files it changed are set down as conv's.
func MirrorCommand(conv string, run func() string) string {
	u := mirrorUnitOf(conv)
	if u == nil {
		return run()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	before := statMirror(u.dir)
	out := run()
	after := statMirror(u.dir)
	others := u.othersTouched(conv, before, after)
	var said []string
	for _, w := range mirrorDiff(before, after) {
		u.seq++
		w.Conv, w.Seq = conv, u.seq
		u.writes = append(u.writes, w)
		if _, ok := u.owner[w.File]; !ok && !w.Removed {
			u.owner[w.File] = conv
		}
		if w.Removed {
			said = append(said, w.File+" (removed)")
		} else {
			said = append(said, w.File)
		}
	}
	if len(said) > 0 {
		log.Printf(mirrorWroteFmt, shortID(conv), partName(u.part[conv]), len(said), strings.Join(said, " "))
	}
	out += u.sinceHeard(conv)
	if len(others) > 0 {
		log.Printf(mirrorOthersLogFmt, shortID(conv), partName(u.part[conv]), len(others), strings.Join(others, "; "))
		out += fmt.Sprintf(mirrorOthersFmt, len(others), "  "+strings.Join(others, "\n  ")+"\n")
	}
	return out
}

const (
	// %[1]s the conversation, %[2]s its part, %[3]d files, %[4]s them with whose they were
	mirrorOthersLogFmt = "mirror: a command of %[1]s (%[2]s) changed or removed %[3]d file(s) another conversation of the unit wrote: %[4]s"
	// %[1]d files, %[2]s them with whose they were
	mirrorOthersFmt = "\n[THE UNIT'S MIRROR — measured on this machine]\nthis command changed or removed %[1]d file(s) that another conversation of the unit wrote or the planner gave another part:\n%[2]s"
)

// othersTouched: the files conv's command changed or removed that are another's — another
// conversation was first to write them in this attempt, or the planner gave them to another
// part — each with whose it was. Called with u.mu held.
func (u *mirrorUnit) othersTouched(conv string, before, after map[string]mirrorStat) []string {
	mine := partName(u.part[conv])
	var out []string
	for _, w := range mirrorDiff(before, after) {
		whose := ""
		if o, ok := u.owner[w.File]; ok && o != conv {
			whose = fmt.Sprintf("written by %s, conversation %s", partName(u.part[o]), shortID(o))
		} else if p, ok := u.named[w.File]; ok && p != mine {
			whose = "the planner gave it to " + p
		}
		if whose == "" {
			continue
		}
		what := "changed"
		if w.Removed {
			what = "removed"
		}
		out = append(out, fmt.Sprintf("%s %s (%s)", w.File, what, whose))
	}
	return out
}

const (
	// %[1]d files, %[2]s them
	mirrorSinceFmt = "\n[THE UNIT'S MIRROR — measured on this machine]\nsince this conversation was last told, the other conversations of the unit changed %[1]d file(s) of the mirror, which its sandbox does not have:\n%[2]s"
	// mirrorSinceMax: how many of them are named
	mirrorSinceMax = 40
)

// sinceHeard: the files the other conversations of the unit changed in the mirror since conv
// was last told — each file once, as it last stands — and conv is marked told of them. Called
// with u.mu held. 11/23 each part's sandbox kept the tree it had cloned while the mirror took
// every part's commands, and "exit differs: sandbox 0, here 1" (16:12:48, head gdn_qwen.cu) was
// the only trace that the two had parted.
func (u *mirrorUnit) sinceHeard(conv string) string {
	last := map[string]mirrorWrite{}
	var order []string
	for _, w := range u.writes {
		if w.Seq <= u.heard[conv] || w.Conv == conv {
			continue
		}
		if _, ok := last[w.File]; !ok {
			order = append(order, w.File)
		}
		last[w.File] = w
	}
	u.heard[conv] = u.seq
	if len(order) == 0 {
		return ""
	}
	sort.Strings(order)
	var ls []string
	for i, f := range order {
		if i == mirrorSinceMax {
			ls = append(ls, fmt.Sprintf("… and %d more", len(order)-mirrorSinceMax))
			break
		}
		w := last[f]
		what := "written"
		if w.Removed {
			what = "removed"
		}
		ls = append(ls, fmt.Sprintf("%s %s by %s (conversation %s)", f, what, partName(u.part[w.Conv]), shortID(w.Conv)))
	}
	return fmt.Sprintf(mirrorSinceFmt, len(order), "  "+strings.Join(ls, "\n  ")+"\n")
}

// noteMirrorPartFiles: the files the planner's partfile lines give each part of unit.
func noteMirrorPartFiles(unitID string, files map[string][]string) {
	mirrorUnitsMu.Lock()
	u := mirrorUnits[unitID]
	mirrorUnitsMu.Unlock()
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for part, fs := range files {
		for _, f := range fs {
			u.named[f] = part
		}
	}
}

// mirrorDiff: the files changed, created or removed between two readings of a mirror.
func mirrorDiff(before, after map[string]mirrorStat) []mirrorWrite {
	var out []mirrorWrite
	for f, a := range after {
		if b, ok := before[f]; !ok || b != a {
			out = append(out, mirrorWrite{File: f})
		}
	}
	for f := range before {
		if _, ok := after[f]; !ok {
			out = append(out, mirrorWrite{File: f, Removed: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// partName: a part's name ("the unit's conversation" for its own).
func partName(part string) string {
	if part == "" {
		return "the unit's conversation"
	}
	name, _, _ := strings.Cut(part, ":")
	return strings.TrimSpace(name)
}

// mirrorOwners: per part name, the files its conversation was first to write in the unit's
// mirror in this process.
func mirrorOwners(unitID string) map[string][]string {
	mirrorUnitsMu.Lock()
	u := mirrorUnits[unitID]
	mirrorUnitsMu.Unlock()
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	out := map[string][]string{}
	for f, conv := range u.owner {
		p := u.part[conv]
		if p == "" {
			continue
		}
		out[partName(p)] = append(out[partName(p)], f)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}
