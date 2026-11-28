package kernel

// theory_store.go — where a theory chunk lives on the shared branch, named by what it IS.
//
// Chunks used to be stored as .einsteinOS/chunks/theorize_chunk_<N>/, a name that says
// only where the chunk sat in ONE plan. The 2026/11/03 run (same reference commit as the
// 11/02 run, 9 chunks instead of 5) found theorize_chunk_1 already on einsteinOS/theory
// with the other run's files in it: every merge failed (add/add on conv.json and on
// SessionTokenLoop.lean, a module name both plans had), and the later chunks, told to read
// "prior work" from that branch, read the OTHER run's theory instead of this one's.
//
// A chunk is now identified by
//
//	the reference commit  — what was read
//	the plan              — how the files were grouped (a hash of the groups, in order)
//	its position + conv   — which group, and the conversation that wrote it
//
// .einsteinOS/theory/<ref12>/<plan>/<NN>-<conv12>/<Module>.lean, conv.json
// .einsteinOS/theory/<ref12>/<plan>/plan.json   (the groups; identical for every chunk)
//
// Two plans, two runs, two retries of one chunk: different directories, so a merge into
// the shared branch cannot conflict and never overwrites another run's work. plan.json is
// written by every chunk of a plan with the same bytes, which git merges cleanly.
//
// Later chunks do not need git at all to read the earlier ones: the kernel keeps each
// chunk's theory as that conversation's result, and `einsteinos conv <addr> files` /
// `einsteinos conv <addr> file <name>` hand it over by address (spawn.go).

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	theoryStoreRoot   = ".einsteinOS/theory"
	theoryPlanDirFmt  = "%[1]s/%[2]s/%[3]s"   // root, ref12, plan id
	theoryChunkDirFmt = "%[1]s/%02[2]d-%[3]s" // plan dir, chunk number, conv12
	theoryPlanFile    = "plan.json"
)

// theoryPlanRef is the identity of one way of grouping one commit's files into chunks.
type theoryPlanRef struct {
	Ref    string     `json:"ref"`    // full reference commit
	Plan   string     `json:"plan"`   // hash of Ref and the groups, in order
	Chunks [][]string `json:"chunks"` // the groups, as planned
}

// newTheoryPlanRef names the plan. The same commit split the same way gets the same id
// (a restart continues into the same directory); any other grouping gets another.
func newTheoryPlanRef(sha string, chunks [][]string) theoryPlanRef {
	var b strings.Builder
	b.WriteString(sha)
	for _, c := range chunks {
		b.WriteString("\x01")
		b.WriteString(strings.Join(c, "\x00"))
	}
	cp := make([][]string, len(chunks))
	for i, c := range chunks {
		cp[i] = append([]string{}, c...)
	}
	return theoryPlanRef{Ref: sha, Plan: shortHash(b.String())[:10], Chunks: cp}
}

// Valid: a plan without a commit has no place on the branch.
func (p theoryPlanRef) Valid() bool { return p.Ref != "" && p.Plan != "" }

// Dir is the plan's directory in the repository.
func (p theoryPlanRef) Dir() string {
	return fmt.Sprintf(theoryPlanDirFmt, theoryStoreRoot, shortSHA12(p.Ref), p.Plan)
}

// ChunkDir is the directory of chunk number idx (1-based) as written by convID.
func (p theoryPlanRef) ChunkDir(idx int, convID string) string {
	return fmt.Sprintf(theoryChunkDirFmt, p.Dir(), idx, convShort(convID))
}

// PlanFile is the repository path of plan.json.
func (p theoryPlanRef) PlanFile() string { return p.Dir() + "/" + theoryPlanFile }

// Manifest is plan.json: deterministic, so every chunk of the plan writes the same bytes.
func (p theoryPlanRef) Manifest() []byte {
	b, _ := json.MarshalIndent(p, "", "  ")
	return append(b, '\n')
}

// convShort is the 12-character form of a conversation id used in branch and directory
// names (conv/<12> is already the chunk's branch).
func convShort(convID string) string {
	if len(convID) > 12 {
		return convID[:12]
	}
	return convID
}

// priorChunk is what a later chunk is told about an earlier one of the same plan.
type priorChunk struct {
	Idx   int    // 1-based position in the plan
	Conv  string // conversation address
	Files int    // .lean files the kernel holds for it
}

const (
	priorHeadFmt = "[PRIOR WORK — %[1]d earlier chunk(s) of this plan already produced theory for other files]\n" +
		"the kernel holds their files. read them by conversation address, in bash:\n" +
		"  einsteinos conv <addr> files          the .lean files that conversation wrote\n" +
		"  einsteinos conv <addr> file <name>    one of them, in full\n"
	priorLineFmt = "  chunk %[1]d: %[2]s (%[3]d file(s))\n"
	priorTailFmt = "this is READ-ONLY context for types and interfaces. produce theory for YOUR assigned files, then stop.\n\n"
)

// PriorChunksContext builds the [PRIOR WORK] section of a theory chunk prompt from the
// earlier chunks of the SAME plan, so a chunk never reads another run's theory.
func PriorChunksContext(prior []priorChunk) string {
	if len(prior) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf(priorHeadFmt, len(prior)))
	for _, c := range prior {
		b.WriteString(fmt.Sprintf(priorLineFmt, c.Idx, c.Conv, c.Files))
	}
	b.WriteString(priorTailFmt)
	return b.String()
}

// convFile is one "## FILE:" section of a conversation's stored result.
type convFile struct {
	Path    string
	Content string
}

// convResultFiles splits a result rendered by renderChunkFiles back into its files.
func convResultFiles(text string) []convFile {
	const head = "## FILE: "
	var out []convFile
	lines := strings.Split(text, "\n")
	cur := -1
	var body []string
	flush := func() {
		if cur >= 0 {
			out[cur].Content = strings.TrimRight(strings.Join(body, "\n"), "\n") + "\n"
		}
	}
	for _, l := range lines {
		if strings.HasPrefix(l, head) {
			flush()
			out = append(out, convFile{Path: strings.TrimSpace(l[len(head):])})
			cur = len(out) - 1
			body = body[:0]
			continue
		}
		if cur >= 0 {
			body = append(body, l)
		}
	}
	flush()
	return out
}

// findConvFile picks the file a child named: the full path, or the base name when only
// one file has it.
func findConvFile(files []convFile, name string) (convFile, bool) {
	name = strings.TrimSpace(name)
	var byBase []convFile
	for _, f := range files {
		if f.Path == name {
			return f, true
		}
		if base := f.Path[strings.LastIndex(f.Path, "/")+1:]; base == name {
			byBase = append(byBase, f)
		}
	}
	if len(byBase) == 1 {
		return byBase[0], true
	}
	return convFile{}, false
}
