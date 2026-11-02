package kernel

import (
	"fmt"
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
