package kernel

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestExtractTaggedTakesTheLastBlockAndToleratesCaseAndFences(t *testing.T) {
	out := "thinking...\n<survey>\nheavy=a\n</survey>\nactually:\n```\n<SURVEY>\nheavy=b\n</SURVEY>\n```\n" + DoneMarker
	got, ok := extractTagged(out, "survey")
	if !ok || got != "heavy=b" {
		t.Fatalf("%q %v", got, ok)
	}
	if _, ok := extractTagged("no tags here", "plan"); ok {
		t.Fatal("found a block that is not there")
	}
}

func TestParseSurveyBlockSetRelations(t *testing.T) {
	known := map[string]int{"a.cpp": 10, "b.cpp": 20, "c.cpp": 30}
	h, l := parseSurveyBlock("heavy=a.cpp, ghost.cpp, b.cpp\nlight=b.cpp,c.cpp,c.cpp", known)
	if strings.Join(h, ",") != "a.cpp,b.cpp" {
		t.Fatalf("heavy=%v (unknown path must be dropped)", h)
	}
	if strings.Join(l, ",") != "c.cpp" {
		t.Fatalf("light=%v: b.cpp is in both lists and must count as heavy; c.cpp once", l)
	}
}

// fixture: n files named src/f00.cpp.., every fifth one light by the kernel's own rule
func fixtureRef(t *testing.T, n int) (heavy []string, light []lightFile) {
	old := refFileAtFn
	refFileAtFn = func(owner, name, sha, path string) (string, error) {
		return strings.Repeat("line\n", 40), nil
	}
	t.Cleanup(func() { refFileAtFn = old })
	for i := 0; i < n; i++ {
		f := fmt.Sprintf("src/f%02d.cpp", i)
		if i%5 == 4 {
			light = append(light, lightFile{f, 40})
		} else {
			heavy = append(heavy, f)
		}
	}
	return heavy, light
}

func stubSurveyRunner(t *testing.T, answer func(prompt string) string) *[]string {
	var prompts []string
	old := surveyPlanRunner
	surveyPlanRunner = func(parent int64, owner, repo, prompt string) string {
		prompts = append(prompts, prompt)
		return answer(prompt)
	}
	t.Cleanup(func() { surveyPlanRunner = old })
	return &prompts
}

func lightPaths(ls []lightFile) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.path)
	}
	return out
}

func chunkLines(files []string, per int) string {
	var b strings.Builder
	for i := 0; i < len(files); i += per {
		end := i + per
		if end > len(files) {
			end = len(files)
		}
		b.WriteString("chunk: " + strings.Join(files[i:end], ", ") + "\n")
	}
	return b.String()
}

func TestSurveyAndPlanOneCallWhenBothSectionsAreValid(t *testing.T) {
	heavy, light := fixtureRef(t, 60)
	prompts := stubSurveyRunner(t, func(string) string {
		return "<survey>\nheavy=" + strings.Join(heavy, ",") + "\nlight=" + strings.Join(lightPaths(light), ",") +
			"\n</survey>\n<plan>\n" + chunkLines(heavy, 10) + "</plan>\n" + DoneMarker
	})
	h, l, chunks := surveyAndPlan(1, "o", "r", "abcdef0123456789", heavy, light, "task")
	if len(*prompts) != 1 {
		t.Fatalf("wanted ONE conversation, got %d", len(*prompts))
	}
	if !strings.Contains((*prompts)[0], "<plan>") || !strings.Contains((*prompts)[0], DoneMarker) {
		t.Fatal("the prompt must ask for the plan section and for the done marker")
	}
	if len(h)+len(l) != 60 {
		t.Fatalf("lost files: %d+%d", len(h), len(l))
	}
	seen := map[string]int{}
	for _, c := range chunks {
		for _, f := range c {
			seen[f]++
		}
	}
	if len(seen) != len(h) {
		t.Fatalf("plan covers %d of %d heavy files", len(seen), len(h))
	}
	for f, c := range seen {
		if c != 1 {
			t.Fatalf("%s placed %d times", f, c)
		}
	}
}

func TestSurveyAndPlanCrossChecksTheSectionsAgainstEachOther(t *testing.T) {
	heavy, light := fixtureRef(t, 60)
	// the plan names a file the survey called LIGHT, names a ghost, and forgets many heavy files
	stubSurveyRunner(t, func(string) string {
		return "<survey>\nheavy=" + strings.Join(heavy[:len(heavy)-1], ",") + "\nlight=" + light[0].path + "\n</survey>\n<plan>\n" +
			"chunk: " + strings.Join(heavy[:20], ", ") + ", " + light[0].path + ", ghost.cpp\n" +
			"chunk: " + strings.Join(heavy[20:30], ", ") + "\n</plan>\n" + DoneMarker
	})
	h, _, chunks := surveyAndPlan(1, "o", "r", "abcdef0123456789", heavy, light, "task")
	inHeavy := map[string]bool{}
	for _, f := range h {
		inHeavy[f] = true
	}
	placed := map[string]int{}
	for _, c := range chunks {
		for _, f := range c {
			if !inHeavy[f] {
				t.Fatalf("plan contains %s, which is not heavy", f)
			}
			placed[f]++
		}
	}
	for f := range inHeavy {
		if placed[f] != 1 {
			t.Fatalf("heavy file %s placed %d times (unplaced files must be redistributed, none dropped)", f, placed[f])
		}
	}
}

func TestSurveyAndPlanSmallRepoAsksNoPlan(t *testing.T) {
	heavy, light := fixtureRef(t, 20)
	prompts := stubSurveyRunner(t, func(string) string {
		return "<survey>\nheavy=" + strings.Join(heavy, ",") + "\nlight=\n</survey>\n" + DoneMarker
	})
	_, _, chunks := surveyAndPlan(1, "o", "r", "abcdef0123456789", heavy, light, "task")
	if chunks != nil || strings.Contains((*prompts)[0], "<plan>") {
		t.Fatal("20 files fit one conversation: no plan section, no chunks")
	}
}

// ---- only the failing section is asked again ------------------------------------------------

type seqModel struct {
	mu      sync.Mutex
	prompts []string
	reply   func(prompt string) string
}

func (m *seqModel) exec(prompt string) (string, string) {
	m.mu.Lock()
	m.prompts = append(m.prompts, prompt)
	n := len(m.prompts)
	m.mu.Unlock()
	id := fmt.Sprintf("%08x-1111-4111-8111-%012x", n*104729, n)
	SetForkConvID(id)
	SetConvID(GetLastForkedPid(), id)
	return m.reply(prompt), ""
}

func useSeqModel(t *testing.T, reply func(prompt string) string) *seqModel {
	bootKernel()
	m := &seqModel{reply: reply}
	old := convExecFn
	SetConvExec(m.exec)
	t.Cleanup(func() { convExecFn = old })
	return m
}

func TestMissingPlanSectionIsAskedForAlone(t *testing.T) {
	heavy, light := fixtureRef(t, 60)
	stubSurveyRunner(t, func(string) string { // survey fine, plan section absent
		return "<survey>\nheavy=" + strings.Join(heavy, ",") + "\nlight=\n</survey>\n" + DoneMarker
	})
	m := useSeqModel(t, func(prompt string) string {
		if strings.Contains(prompt, "[TASK] group") {
			return chunkLines(heavy, 20)
		}
		return ""
	})
	_, _, chunks := surveyAndPlan(1, "o", "r", "abcdef0123456789", heavy, light, "task")
	if len(m.prompts) != 1 || !strings.Contains(m.prompts[0], "[TASK] group") {
		t.Fatalf("exactly the planner should have been asked again, got %d prompts", len(m.prompts))
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks=%d", len(chunks))
	}
}

func TestMissingSurveySectionIsAskedForAlone(t *testing.T) {
	heavy, light := fixtureRef(t, 60)
	stubSurveyRunner(t, func(string) string { // plan fine, survey section absent
		return "<plan>\n" + chunkLines(heavy, 20) + "</plan>\n" + DoneMarker
	})
	m := useSeqModel(t, func(prompt string) string {
		if strings.Contains(prompt, "[FILE SURVEY]") {
			return "heavy=" + strings.Join(heavy, ",") + "\nlight=" + light[0].path
		}
		return ""
	})
	h, l, chunks := surveyAndPlan(1, "o", "r", "abcdef0123456789", heavy, light, "task")
	if len(m.prompts) != 1 || !strings.Contains(m.prompts[0], "[FILE SURVEY]") {
		t.Fatalf("exactly the file survey should have been asked again, got %d prompts", len(m.prompts))
	}
	if len(h) != len(heavy) || len(l) == 0 || len(chunks) != 3 {
		t.Fatalf("h=%d l=%d chunks=%d", len(h), len(l), len(chunks))
	}
}

func TestParsePlanLinesIsDeterministic(t *testing.T) {
	files := []string{"a", "b", "c", "d", "e", "f", "g"}
	for i := 0; i < 20; i++ {
		chunks, unclaimed := parsePlanLines("chunk: a, b\nchunk: b, ghost, c\n", files)
		if strings.Join(unclaimed, ",") != "d,e,f,g" {
			t.Fatalf("unclaimed order must follow the file list: %v", unclaimed)
		}
		if len(chunks) != 2 || strings.Join(chunks[1], ",") != "c" {
			t.Fatalf("%v", chunks)
		}
	}
}

// ---- the directory section ---------------------------------------------------------------

// 240 files: core code, and the directories the old directory survey existed to drop
func fixtureDirs(t *testing.T) (heavy []string, light []lightFile, byDir map[string][]string) {
	old := refFileAtFn
	refFileAtFn = func(owner, name, sha, path string) (string, error) { return strings.Repeat("line\n", 40), nil }
	t.Cleanup(func() { refFileAtFn = old })
	byDir = map[string][]string{}
	for dir, n := range map[string]int{"src/core": 90, "src/kernels": 60, "tests": 40, "bench": 30, "examples": 20} {
		for i := 0; i < n; i++ {
			f := fmt.Sprintf("%s/f%02d.cpp", dir, i)
			byDir[dir] = append(byDir[dir], f)
			heavy = append(heavy, f) // the kernel's own line-count rule calls every one of them heavy
		}
	}
	sort.Strings(heavy)
	return heavy, nil, byDir
}

func TestParseDirsBlock(t *testing.T) {
	stats := map[string]*dirStat{"src/core": {1, 1}, "src/kernels/cuda": {1, 1}, "tests": {1, 1}, ".": {1, 1}}
	got := parseDirsBlock("keep=src/core, ghost, src/kernels/, tests,src/core\nkeep=.", stats)
	if strings.Join(got, ",") != "src/core,src/kernels,tests,." {
		// src/kernels is a PARENT of an existing directory: accepted; ghost dropped; duplicates once; "." is a real directory here
		t.Fatalf("%q", strings.Join(got, ","))
	}
}

func TestDirsSectionPrunesTestsBenchmarksAndExamplesAndTheSurveyCannotUndoIt(t *testing.T) {
	heavy, light, byDir := fixtureDirs(t)
	kept := append(append([]string{}, byDir["src/core"]...), byDir["src/kernels"]...)
	sort.Strings(kept)
	prompts := stubSurveyRunner(t, func(string) string {
		// the model keeps two directories, but its survey ALSO lists a test file and a benchmark as heavy
		surveyHeavy := append(append([]string{}, kept...), byDir["tests"][0], byDir["bench"][0])
		return "<dirs>\nkeep=src/core,src/kernels\n</dirs>\n<survey>\nheavy=" + strings.Join(surveyHeavy, ",") +
			"\nlight=" + strings.Join(byDir["examples"], ",") + "\n</survey>\n<plan>\n" + chunkLines(surveyHeavy, 10) + "</plan>\n" + DoneMarker
	})
	h, l, chunks := surveyAndPlan(1, "o", "r", "abcdef0123456789", heavy, light, "task")
	if len(*prompts) != 1 {
		t.Fatalf("one conversation, got %d", len(*prompts))
	}
	if !strings.Contains((*prompts)[0], "PART 0 — DIRECTORIES") || !strings.Contains((*prompts)[0], "skip tests, benchmarks, examples, docs, CI, vendored, build scripts") {
		t.Fatal("the directory part and the skip rule must be in the prompt")
	}
	if len(h) != len(kept) {
		t.Fatalf("heavy=%d want exactly the %d files of the kept directories (the test and benchmark file the survey called heavy must be light)", len(h), len(kept))
	}
	for _, f := range h {
		if !strings.HasPrefix(f, "src/") {
			t.Fatalf("%s is heavy but is outside the kept directories", f)
		}
	}
	if len(h)+len(l) != len(heavy) {
		t.Fatalf("files lost: %d+%d vs %d", len(h), len(l), len(heavy))
	}
	placed := map[string]int{}
	for _, c := range chunks {
		for _, f := range c {
			placed[f]++
			if !strings.HasPrefix(f, "src/") {
				t.Fatalf("the plan contains %s from a directory that was skipped", f)
			}
		}
	}
	if len(placed) != len(h) {
		t.Fatalf("plan covers %d of %d heavy files", len(placed), len(h))
	}
	// the point of the whole section: 150 heavy files → 15 chunks of 10; without the
	// directories the same repository planned 240 files' worth
	if len(chunks) > 16 {
		t.Fatalf("chunks=%d", len(chunks))
	}
}

func TestMissingDirsSectionIsAskedForAloneWithTheShortPrompt(t *testing.T) {
	heavy, light, byDir := fixtureDirs(t)
	stubSurveyRunner(t, func(string) string { // survey and plan present, <dirs> absent
		return "<survey>\nheavy=" + strings.Join(heavy, ",") + "\nlight=\n</survey>\n<plan>\n" + chunkLines(heavy, 10) + "</plan>\n" + DoneMarker
	})
	m := useSeqModel(t, func(prompt string) string {
		if strings.Contains(prompt, "which have core algorithms") {
			return "keep=src/core"
		}
		return ""
	})
	h, _, _ := surveyAndPlan(1, "o", "r", "abcdef0123456789", heavy, light, "task")
	if len(m.prompts) < 1 || !strings.Contains(m.prompts[0], "directories in o/r") {
		t.Fatalf("the directory question must have been asked alone, got %d prompts", len(m.prompts))
	}
	if len(h) != len(byDir["src/core"]) {
		t.Fatalf("heavy=%d want the %d files of src/core", len(h), len(byDir["src/core"]))
	}
}

func TestNoDirectoryPartForSmallRepositories(t *testing.T) {
	heavy, light := fixtureRef(t, 60)
	prompts := stubSurveyRunner(t, func(string) string {
		return "<survey>\nheavy=" + strings.Join(heavy, ",") + "\nlight=\n</survey>\n<plan>\n" + chunkLines(heavy, 10) + "</plan>\n" + DoneMarker
	})
	surveyAndPlan(1, "o", "r", "abcdef0123456789", heavy, light, "task")
	if strings.Contains((*prompts)[0], "PART 0") || strings.Contains((*prompts)[0], "<dirs>") {
		t.Fatal("60 files: no directory step (the old threshold was 200)")
	}
}

// ---- chunk sizes ------------------------------------------------------------------------------

func mkChunks(sizes ...int) [][]string {
	var out [][]string
	n := 0
	for _, sz := range sizes {
		var c []string
		for i := 0; i < sz; i++ {
			c = append(c, fmt.Sprintf("f%03d.cpp", n))
			n++
		}
		out = append(out, c)
	}
	return out
}

func flat(chunks [][]string) string {
	var all []string
	for _, c := range chunks {
		all = append(all, c...)
	}
	return strings.Join(all, ",")
}

func TestCoalesceChunksMergesTheTinyChunksOfTheFifteenChunkRun(t *testing.T) {
	// what the 2026/11/02 run produced: 15 chunks of 2-3 files for ~40 files
	in := mkChunks(2, 3, 2, 3, 2, 3, 3, 2, 3, 2, 3, 3, 2, 3, 3)
	out := coalesceChunks(in)
	if flat(out) != flat(in) {
		t.Fatal("files lost, duplicated or reordered")
	}
	if len(out) > 7 || len(out) < 4 {
		t.Fatalf("%d chunks (sizes %v): 15 tiny chunks should come down to about 5", len(out), chunkSizes(out))
	}
	for i, c := range out {
		if len(c) > chunkHardMax {
			t.Fatalf("chunk %d has %d files, over the hard max", i, len(c))
		}
	}
	for i, c := range out[:len(out)-1] {
		if len(c) < chunkSmall {
			t.Fatalf("chunk %d still has %d files (sizes %v)", i, len(c), chunkSizes(out))
		}
	}
}

func TestCoalesceChunksLeavesAGoodPlanAlone(t *testing.T) {
	for _, sizes := range [][]int{{9, 10, 10, 10, 4}, {10, 10, 10}, {12, 12}, {8}, {}} {
		in := mkChunks(sizes...)
		out := coalesceChunks(in)
		if fmt.Sprint(chunkSizes(out)) != fmt.Sprint(sizes) && len(sizes) > 0 {
			t.Errorf("a plan of %v must not change, got %v", sizes, chunkSizes(out))
		}
	}
}

func TestCoalesceChunksNeverBuildsAChunkPastTheLimits(t *testing.T) {
	// a small chunk next to a big one cannot join it
	out := coalesceChunks(mkChunks(5, 12, 2))
	if fmt.Sprint(chunkSizes(out)) != "[5 12 2]" && fmt.Sprint(chunkSizes(out)) != "[5 14]" {
		t.Fatalf("%v", chunkSizes(out))
	}
	for _, c := range out {
		if len(c) > chunkHardMax {
			t.Fatalf("%v", chunkSizes(out))
		}
	}
	// a last chunk of 1-3 files joins the previous one when it fits
	if got := chunkSizes(coalesceChunks(mkChunks(10, 10, 2))); fmt.Sprint(got) != "[10 12]" {
		t.Fatalf("%v", got)
	}
	// but not past the hard max
	if got := chunkSizes(coalesceChunks(mkChunks(10, 15, 3))); fmt.Sprint(got) != "[10 15 3]" {
		t.Fatalf("%v", got)
	}
}
