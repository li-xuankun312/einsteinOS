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
	"os"
	"path/filepath"
	"regexp"
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
	unit   string
	dir    string
	part   map[string]string // conv -> its part ("" for the unit's own conversation)
	owner  map[string]string // file -> the conversation that first wrote it in this attempt
	named  map[string]string // file -> the part the planner's partfile lines give it
	writes []mirrorWrite
	seq    int
	heard  map[string]int // conv -> the last write it was told of
	// content: each file's bytes as the last command left them (at most mirrorKeepMax each),
	// which a file a command must not have changed is written back from
	content map[string][]byte
}

// mirrorKeepMax: a file larger than this is not kept and cannot be written back.
const mirrorKeepMax = 4 << 20

// keep reads the files' current bytes into the unit's record (removed ones dropped).
func (u *mirrorUnit) keep(files []string) {
	for _, f := range files {
		p := filepath.Join(u.dir, filepath.FromSlash(f))
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() > mirrorKeepMax {
			delete(u.content, f)
			continue
		}
		if b, err := os.ReadFile(p); err == nil {
			u.content[f] = b
		}
	}
}

// writeBack puts a file back as it was kept; false when it was not kept.
func (u *mirrorUnit) writeBack(f string) bool {
	b, ok := u.content[f]
	if !ok {
		return false
	}
	p := filepath.Join(u.dir, filepath.FromSlash(f))
	os.MkdirAll(filepath.Dir(p), 0o755)
	return os.WriteFile(p, b, 0o644) == nil
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
		u = &mirrorUnit{unit: unitID, dir: dir, part: map[string]string{}, owner: map[string]string{}, named: map[string]string{}, heard: map[string]int{}, content: map[string][]byte{}}
		var all []string
		for f := range statMirror(dir) {
			all = append(all, f)
		}
		u.keep(all)
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
	// what the command must not have done is undone first, and told
	if back := u.undo(conv, before, after); len(back) > 0 {
		log.Printf(mirrorUndoneLogFmt, shortID(conv), partName(u.part[conv]), len(back), strings.Join(back, "; "))
		out += fmt.Sprintf(mirrorUndoneFmt, len(back), "  "+strings.Join(back, "\n  ")+"\n")
		after = statMirror(u.dir)
	}
	others := u.othersTouched(conv, before, after)
	var said []string
	var kept []string
	for _, w := range mirrorDiff(before, after) {
		kept = append(kept, w.File)
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
	u.keep(kept)
	if len(said) > 0 {
		log.Printf(mirrorWroteFmt, shortID(conv), partName(u.part[conv]), len(said), strings.Join(said, " "))
		mirrorChanged(u.unit)
	}
	out += mirrorVerifyNews(u.unit, conv)
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

const (
	// %[1]s the conversation, %[2]s its part, %[3]d files, %[4]s them and why
	mirrorUndoneLogFmt = "mirror: a command of %[1]s (%[2]s) was undone for %[3]d file(s), written back as they were: %[4]s"
	// %[1]d files, %[2]s them and why
	mirrorUndoneFmt = "\n[THE UNIT'S MIRROR — measured on this machine]\nthe kernel wrote back %[1]d file(s) this command changed, as they were before it:\n%[2]s"
	// %[1]d the bytes it had
	mirrorEmptiedFmt = "left empty here, %[1]d bytes before"
)

// undo writes back the files conv's command must not have changed: a file it left empty that
// had bytes — 11/23 20:27:45 a command of the gdn part read 14 files out of a remote branch
// this machine's clone does not have (git show $CONT:path > path) and left each of them 0 bytes
// here, build.sh among them, and every verification after it ran an empty build.sh. Called with
// u.mu held; it returns each file written back and why.
func (u *mirrorUnit) undo(conv string, before, after map[string]mirrorStat) []string {
	var out []string
	for f, a := range after {
		b, had := before[f]
		if !had || b.size == 0 || a.size != 0 {
			continue
		}
		if u.writeBack(f) {
			out = append(out, f+" ("+fmt.Sprintf(mirrorEmptiedFmt, b.size)+")")
		}
	}
	// a part's conversation does not take out or change lines of another's file (it may add
	// lines: a declaration in a shared header, a row of the port map): 11/23 21:00-23:05 the moe, embed,
	// loader and qsa parts each rewrote gdn_bf16_layer.cu, the gdn part's, and were only told
	if mine := u.part[conv]; mine != "" {
		for _, w := range mirrorDiff(before, after) {
			if hasPrefixAny(out, w.File+" (") {
				continue
			}
			whose := ""
			if o, ok := u.owner[w.File]; ok && o != conv {
				whose = fmt.Sprintf(mirrorOwnedByFmt, partName(u.part[o]), shortID(o))
			} else if p, ok := u.named[w.File]; ok && p != partName(mine) {
				whose = fmt.Sprintf(mirrorNamedForFmt, p)
			}
			if whose == "" {
				continue
			}
			if !w.Removed && u.onlyAdded(w.File) {
				continue // lines added to another's file, none of its own taken out
			}
			if w.Removed || before[w.File].size > 0 {
				if u.writeBack(w.File) {
					out = append(out, w.File+" ("+whose+")")
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

const (
	// %[1]s the part, %[2]s its conversation
	mirrorOwnedByFmt = "the file of %[1]s, conversation %[2]s, which wrote it first"
	// %[1]s the part
	mirrorNamedForFmt = "the file the planner gave %[1]s"
)

// onlyAdded: whether the file as it is now holds every line of the kept one, in order — lines
// were added to it (a declaration in a shared header, a row in the port map) and none of its own
// removed or changed.
func (u *mirrorUnit) onlyAdded(f string) bool {
	was, ok := u.content[f]
	if !ok {
		return false
	}
	now, err := os.ReadFile(filepath.Join(u.dir, filepath.FromSlash(f)))
	if err != nil {
		return false
	}
	nl := strings.Split(string(now), "\n")
	j := 0
	for _, l := range strings.Split(string(was), "\n") {
		for j < len(nl) && nl[j] != l {
			j++
		}
		if j == len(nl) {
			return false
		}
		j++
	}
	return true
}

func hasPrefixAny(l []string, p string) bool {
	for _, s := range l {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

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

// compileErrPathRe: a source file and line a compiler names in an error.
var compileErrPathRe = regexp.MustCompile(`([A-Za-z0-9_./+-]+\.(?:cu|cuh|cpp|cc|c|hpp|h))[:(](\d+)`)

const (
	// %[1]d files, %[2]s them with whose they are
	compileOwnersFmt = "[WHOSE FILES THE COMPILER NAMED — measured from its output and the unit's mirror]\n%[1]d file(s):\n%[2]s"
	// %[1]s the file, %[2]d errors, %[3]s whose
	compileOwnerLineFmt = "  %[1]s: %[2]d line(s) named — %[3]s\n"
)

// compileErrorOwners: the files a failed compile's output names, each with the part whose file it
// is in the unit's mirror (first written by its conversation, or given it by the planner). 11/23
// 21:00-23:05 four parts each went to fix the GDN files, which were the gdn part's, as each
// part's own build failed on them.
func compileErrorOwners(unitID, out string) string {
	mirrorUnitsMu.Lock()
	u := mirrorUnits[unitID]
	mirrorUnitsMu.Unlock()
	if u == nil || out == "" {
		return ""
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	count := map[string]int{}
	var order []string
	for _, m := range compileErrPathRe.FindAllStringSubmatch(out, -1) {
		p := filepath.ToSlash(m[1])
		var file string
		for f := range u.content {
			if p == f || strings.HasSuffix(p, "/"+f) {
				file = f
				break
			}
		}
		if file == "" {
			continue
		}
		if count[file] == 0 {
			order = append(order, file)
		}
		count[file]++
	}
	if len(order) == 0 {
		return ""
	}
	sort.Strings(order)
	var b strings.Builder
	for _, f := range order {
		whose := "no part's (no conversation of the unit has written it)"
		if o, ok := u.owner[f]; ok {
			whose = fmt.Sprintf(mirrorOwnedByFmt, partName(u.part[o]), shortID(o))
		} else if p, ok := u.named[f]; ok {
			whose = fmt.Sprintf(mirrorNamedForFmt, p)
		}
		b.WriteString(fmt.Sprintf(compileOwnerLineFmt, f, count[f], whose))
	}
	return fmt.Sprintf(compileOwnersFmt, len(order), b.String())
}
