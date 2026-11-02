package kernel

// ref_survey_plan.go — one conversation that classifies the reference's files
// AND groups the heavy ones into chunks.
//
// Before: a directory survey, then a file survey, then a planner — three
// conversations, each needing the previous answer, each a call in the
// 9-per-10-minutes window (Strata run 2026/11/02: the file survey alone waited
// 7m16s for its slot). The directory survey existed only to shrink the file
// survey's input; the file survey and the planner read the same file list.
//
// Merging answers risks the answers not belonging to each other (a plan that
// names files the survey called light, a file placed twice). So the output is
// two tagged sections, each parsed ON ITS OWN and checked only with set
// relations the kernel can state without knowing the task:
//
//	<survey> every path it names exists; a path in both lists counts as heavy </survey>
//	<plan>   every path is in the (final) heavy set; each path appears once   </plan>
//
// A section that is missing or yields nothing is asked for again ALONE, by the
// single-purpose conversation that used to do it (fileSurveyChild, theory_plan).
// A section that is merely incomplete is completed by the kernel's own defaults
// (unmentioned files keep the kernel's classification; unplaced files are
// redistributed), exactly as before.

import (
	"fmt"
	"log"
	"regexp"
	"strings"
)

// surveyPlanRunner runs the combined conversation and returns its text. A
// variable so the logic can be tested without a model.
var surveyPlanRunner = func(parent int64, owner, repo, prompt string) string {
	child, err := ForkWithRole(prompt, ".", "theory_planner")
	if err != nil {
		log.Printf("surveyAndPlan: %s/%s fork failed: %v", owner, repo, err)
		return ""
	}
	var convID string
	if m := getMeta(child); m != nil && m.ConvID != "" {
		convID = m.ConvID
		RegisterConv(parent, m.ConvID, owner+"/"+repo, "theory_planner", 0)
	}
	Wait(child)
	if convID != "" {
		ConvDone(convID, "done")
	}
	return GetResult(child)
}

// extractTagged returns the text inside the LAST <tag>…</tag> block (the last,
// because a conversation that corrects itself ends with the answer it stands by).
func extractTagged(text, tag string) (string, bool) {
	re := regexp.MustCompile(`(?is)<` + regexp.QuoteMeta(tag) + `>(.*?)</` + regexp.QuoteMeta(tag) + `>`)
	m := re.FindAllStringSubmatch(text, -1)
	if len(m) == 0 {
		return "", false
	}
	return strings.TrimSpace(m[len(m)-1][1]), true
}

// parseSurveyBlock reads heavy=/light= lines. Only paths in known count. A path in
// both lists is heavy: dropping a file from the theory is the costlier mistake.
func parseSurveyBlock(block string, known map[string]int) (heavy, light []string) {
	h, l := map[string]bool{}, map[string]bool{}
	var hOrder, lOrder []string
	take := func(line, prefix string, set map[string]bool, order *[]string) {
		for _, p := range strings.Split(line[len(prefix):], ",") {
			p = strings.TrimSpace(p)
			if p != "" && known[p] > 0 && !set[p] {
				set[p] = true
				*order = append(*order, p)
			}
		}
	}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "heavy="):
			take(line, "heavy=", h, &hOrder)
		case strings.HasPrefix(line, "light="):
			take(line, "light=", l, &lOrder)
		}
	}
	heavy = hOrder
	for _, p := range lOrder {
		if !h[p] {
			light = append(light, p)
		}
	}
	return heavy, light
}

// applySurvey turns a classification into the final heavy/light sets. Files the
// conversation did not mention keep the kernel's own classification.
func applySurvey(heavyPaths, lightPaths, initHeavy []string, initLight []lightFile, allFiles map[string]int) *surveyOutput {
	mentioned := map[string]bool{}
	for _, p := range heavyPaths {
		mentioned[p] = true
	}
	for _, p := range lightPaths {
		mentioned[p] = true
	}
	for _, f := range initHeavy {
		if !mentioned[f] {
			heavyPaths = append(heavyPaths, f)
		}
	}
	var lightOut []lightFile
	for _, p := range lightPaths {
		lightOut = append(lightOut, lightFile{p, allFiles[p]})
	}
	for _, lf := range initLight {
		if !mentioned[lf.path] {
			lightOut = append(lightOut, lf)
		}
	}
	return &surveyOutput{heavy: heavyPaths, light: lightOut}
}

func buildSurveyPlanPrompt(owner, name, sha, task string, files []string, lines map[string]int, previews map[string]string, withPlan bool) string {
	var p strings.Builder
	p.WriteString(fmt.Sprintf("[TASK] %s\n\n", task))
	p.WriteString(fmt.Sprintf("[FILES] %s/%s commit %s — path, lines, first lines\n\n", owner, name, shortSHA12(sha)))
	for _, f := range files {
		p.WriteString(fmt.Sprintf("--- %s (%d lines) ---\n%s\n\n", f, lines[f], previews[f]))
	}
	p.WriteString("[PART 1 — SURVEY] classify EVERY file above: does it need mathematical specification (LAYER 1a/1b)?\n")
	p.WriteString("files with algorithms, state machines, concurrency, retry logic, invariants → heavy\n")
	p.WriteString("simple wrappers, RAII, getters/setters, configs, constants, tests → light\n")
	p.WriteString("a file is in exactly one of the two lists.\n\n")
	if withPlan {
		p.WriteString("[PART 2 — PLAN] group the files you marked HEAVY into chunks for theory analysis.\n")
		p.WriteString("- each chunk is analyzed by one conversation: roughly 8-12 files fits one session\n")
		p.WriteString("- a chunk of 1-3 files wastes a conversation; a chunk of 20+ files will not finish\n")
		p.WriteString("- files in the same directory stay together when possible; related directories (include/x/ and src/x/) can go together\n")
		p.WriteString("- plan ONLY files you marked heavy, each file once\n\n")
	}
	p.WriteString("[OUTPUT FORMAT] exactly these tagged sections, nothing outside them except the last line:\n")
	p.WriteString("<survey>\nheavy=comma,separated,paths\nlight=comma,separated,paths\n</survey>\n")
	if withPlan {
		p.WriteString("<plan>\nchunk: path1, path2, path3\nchunk: path4, path5\n</plan>\n")
	}
	p.WriteString("use EXACT paths from the list above.\n")
	p.WriteString(DoneInstruction())
	return p.String()
}

func shortSHA12(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// surveyAndPlan returns the final classification and, when the heavy set is too
// big for one conversation, the chunking (nil otherwise — ref_split_with then
// asks the planner itself, which only happens if this call failed to produce one).
func surveyAndPlan(parent int64, owner, name, sha string, initHeavy []string, initLight []lightFile, task string) ([]string, []lightFile, [][]string) {
	var files []string
	lines := map[string]int{}
	previews := map[string]string{}
	add := func(path string) {
		content, err := refFileAtFn(owner, name, sha, path)
		if err != nil {
			return
		}
		files = append(files, path)
		lines[path] = strings.Count(content, "\n") + 1
		previews[path] = codePreview(content, 10)
	}
	for _, f := range initHeavy {
		add(f)
	}
	for _, lf := range initLight {
		add(lf.path)
	}
	if len(files) == 0 {
		return initHeavy, initLight, nil
	}
	known := map[string]int{}
	for _, f := range files {
		known[f] = lines[f]
	}

	// a plan is only needed when the heavy set can exceed one conversation
	withPlan := len(files) > SPLIT_CHUNK
	out := surveyPlanRunner(parent, owner, name, buildSurveyPlanPrompt(owner, name, sha, task, files, lines, previews, withPlan))
	log.Printf("surveyAndPlan: %s/%s %d files, plan=%v, answer %d bytes", owner, name, len(files), withPlan, len(out))

	// ---- section 1: survey, validated alone ----
	var survey *surveyOutput
	if block, ok := extractTagged(out, "survey"); ok {
		if h, l := parseSurveyBlock(block, known); len(h)+len(l) > 0 {
			survey = applySurvey(h, l, initHeavy, initLight, known)
		}
	}
	if survey == nil {
		log.Printf("surveyAndPlan: %s/%s <survey> missing or empty — asking for the survey alone", owner, name)
		survey = fileSurveyChild(owner, name, sha, initHeavy, initLight, task)
	}
	heavy, light := initHeavy, initLight
	if survey != nil {
		heavy, light = survey.heavy, survey.light
	}
	log.Printf("surveyAndPlan: %s/%s survey → %d heavy, %d light", owner, name, len(heavy), len(light))

	if len(heavy) <= SPLIT_CHUNK {
		return heavy, light, nil // one conversation does it all; no plan to validate
	}

	// ---- section 2: plan, validated against the FINAL heavy set ----
	var chunks [][]string
	if block, ok := extractTagged(out, "plan"); ok {
		var unclaimed []string
		chunks, unclaimed = parsePlanLines(block, heavy)
		if len(chunks) > 0 && len(unclaimed) > 0 {
			chunks = redistributeUnclaimed(chunks, unclaimed)
			log.Printf("surveyAndPlan: %s/%s %d heavy files the plan did not place → redistributed", owner, name, len(unclaimed))
		}
	}
	if len(chunks) == 0 {
		log.Printf("surveyAndPlan: %s/%s <plan> missing or empty — asking for the plan alone", owner, name)
		chunks = theory_plan(parent, owner, name, heavy)
	}
	return heavy, light, chunks
}
