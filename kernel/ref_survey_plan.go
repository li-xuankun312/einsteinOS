package kernel

// ref_survey_plan.go — one conversation that classifies the reference's files
// AND groups the heavy ones into chunks.
//
// Before: a directory survey, then a file survey, then a planner — three
// conversations, each needing the previous answer, each a call in the
// 9-per-10-minutes window (Strata run 2026/11/02: the file survey alone waited
// 7m16s for its slot).
//
// The first version of this merge DROPPED the directory step on the theory that it
// only shrank the file survey's input. It did more: it told the model to skip tests,
// benchmarks, examples, docs, CI, vendored code and build scripts, and kept 10 of 29
// directories, which is what took 211 files down to 94 candidates and then 43 heavy
// ones. Without it the same repository planned 15 chunks instead of 5. The directory
// decision is back, as the first tagged section of the same conversation, and a
// missing section is asked for alone by the short directory prompt as before.
//
// Merging answers risks the answers not belonging to each other (a plan that
// names files the survey called light, a file placed twice). So the output is
// two tagged sections, each parsed ON ITS OWN and checked only with set
// relations the kernel can state without knowing the task:
//
//	<dirs>   every directory it keeps exists; files outside them are light   </dirs>
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
	"sort"
	"strings"
)

// dirSurveyMinFiles: below this many files the directory step is not worth asking.
const dirSurveyMinFiles = 200

// ---- the directory section ---------------------------------------------------------

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return "."
}

type dirStat struct{ files, lines int }

// dirStatsOf counts files and lines per directory.
func dirStatsOf(files []string, lines map[string]int) map[string]*dirStat {
	out := map[string]*dirStat{}
	for _, f := range files {
		d := dirOf(f)
		if out[d] == nil {
			out[d] = &dirStat{}
		}
		out[d].files++
		out[d].lines += lines[f]
	}
	return out
}

func dirTable(stats map[string]*dirStat) string {
	names := make([]string, 0, len(stats))
	for d := range stats {
		names = append(names, d)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, d := range names {
		fmt.Fprintf(&b, "%s  %d files %d lines\n", d, stats[d].files, stats[d].lines)
	}
	return b.String()
}

// parseDirsBlock reads keep=dir1,dir2. Only directories that exist count (a name that
// is an existing directory, or a parent of one).
func parseDirsBlock(block string, stats map[string]*dirStat) []string {
	var keep []string
	seen := map[string]bool{}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "keep=") {
			continue
		}
		for _, d := range strings.Split(line[5:], ",") {
			d = strings.Trim(strings.TrimSpace(d), "/")
			if d == "" || seen[d] {
				continue
			}
			for known := range stats {
				if known == d || strings.HasPrefix(known, d+"/") {
					seen[d] = true
					keep = append(keep, d)
					break
				}
			}
		}
	}
	return keep
}

func inKeptDir(path string, keep []string) bool {
	for _, d := range keep {
		if d == "." && !strings.Contains(path, "/") {
			return true
		}
		if strings.HasPrefix(path, d+"/") || path == d {
			return true
		}
	}
	return false
}

// pruneByDirs: files in a kept directory keep their class; every heavy file outside
// them becomes light. Light files stay light.
func pruneByDirs(heavy []string, light []lightFile, lines map[string]int, keep []string) ([]string, []lightFile) {
	var h []string
	l := append([]lightFile(nil), light...)
	for _, f := range heavy {
		if inKeptDir(f, keep) {
			h = append(h, f)
		} else {
			l = append(l, lightFile{f, lines[f]})
		}
	}
	return h, l
}

// askDirsAlone is the short directory prompt the old flow used, for when the combined
// answer had no usable <dirs> section.
func askDirsAlone(owner, name string, stats map[string]*dirStat) []string {
	var p strings.Builder
	fmt.Fprintf(&p, surveyDirsFmt, len(stats), owner, name)
	p.WriteString(dirTable(stats))
	p.WriteString(surveyDirsFact + surveyKeepLineFact + DoneInstruction())
	files := 0
	for _, st := range stats {
		files += st.files
	}
	prompt := surveyShell(owner, name, "", files, len(stats), p.String())
	for attempt := 1; attempt <= 5; attempt++ {
		child, err := Fork(prompt, ".")
		if err != nil {
			log.Printf("surveyAndPlan: dir fork failed (attempt %d/5): %v", attempt, err)
			continue
		}
		Wait(child)
		if keep := parseDirsBlock(GetResult(child), stats); len(keep) > 0 {
			return keep
		}
		log.Printf("surveyAndPlan: no keep dirs parsed (attempt %d/5), retrying", attempt)
	}
	return nil
}

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

func buildSurveyPlanPrompt(owner, name, sha, task string, files []string, lines map[string]int, previews map[string]string, withPlan, withDirs bool) string {
	var p strings.Builder
	p.WriteString(fmt.Sprintf("[TASK] %s\n\n", task))
	p.WriteString(fmt.Sprintf("[FILES] %s/%s commit %s — path, lines, first lines\n\n", owner, name, shortSHA12(sha)))
	for _, f := range files {
		p.WriteString(fmt.Sprintf("--- %s (%d lines) ---\n%s\n\n", f, lines[f], previews[f]))
	}
	stats := dirStatsOf(files, lines)
	if withDirs {
		p.WriteString(fmt.Sprintf(surveyDirsFmt, len(stats), owner, name))
		p.WriteString(dirTable(stats))
		p.WriteString(surveyDirsFact)
	}
	p.WriteString(surveyHeavyLightFact)
	if withPlan {
		p.WriteString(fmt.Sprintf(surveyPlanFact, TurnToolBudget, RoundBudget("theorize_chunk_1")))
	}
	p.WriteString(surveySectionsFact)
	if withDirs {
		p.WriteString("<dirs>\nkeep=dir1,dir2,dir3\n</dirs>\n")
	}
	p.WriteString("<survey>\nheavy=comma,separated,paths\nlight=comma,separated,paths\n</survey>\n")
	if withPlan {
		p.WriteString("<plan>\nchunk: path1, path2, path3\nchunk: path4, path5\n</plan>\n")
	}
	p.WriteString(DoneInstruction())
	return surveyShell(owner, name, sha, len(files), len(stats), p.String())
}

// surveyShell: a survey prompt — the shell with the reference's files inserted
func surveyShell(owner, name, sha string, files, dirs int, content string) string {
	return Plain(fmt.Sprintf(theorizeWhereFmt, owner, name, shortSHA12(sha)), fmt.Sprintf(theoryPlanWhatFmt, files, dirs), content)
}

// the survey's facts: %[1]d directories in %[2]s/%[3]s
const surveyDirsFmt = "[DIRECTORIES] %[1]d directories in %[2]s/%[3]s\n"

// what keeping a directory means to the kernel
const surveyDirsFact = "the files of a directory not kept are light.\n"

// what heavy and light mean to the kernel
const surveyHeavyLightFact = "[HEAVY / LIGHT] a heavy file is theorized: its .lean theory is written before its code. a light file is ported straight from the definitions the kernel measures in it, with no theory. a file the answer does not name keeps the kernel's own classification.\n"

// %[1]d/%[2]d the tool calls a round and the rounds of one chunk's conversation
const surveyPlanFact = "[CHUNKS] the heavy files are theorized in chunks; each chunk is one theory conversation: up to %[1]d tool calls a round, up to %[2]d rounds.\n"

// the one line the kernel reads when it asks for the directories alone
const surveyKeepLineFact = "[ANSWER] the kernel reads the line keep=<directory names from the list, comma-separated>\n"

// the sections the kernel reads
const surveySectionsFact = "[ANSWER] the kernel reads these tagged sections, with exact paths and directory names from the lists:\n"

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
	withDirs := len(files) > dirSurveyMinFiles
	stats := dirStatsOf(files, lines)
	out := surveyPlanRunner(parent, owner, name, buildSurveyPlanPrompt(owner, name, sha, task, files, lines, previews, withPlan, withDirs))
	log.Printf("surveyAndPlan: %s/%s %d files in %d dirs, dirs=%v plan=%v, answer %d bytes", owner, name, len(files), len(stats), withDirs, withPlan, len(out))

	// ---- section 0: directories, validated alone ----
	var keep []string
	if withDirs {
		if block, ok := extractTagged(out, "dirs"); ok {
			keep = parseDirsBlock(block, stats)
		}
		if len(keep) == 0 {
			log.Printf("surveyAndPlan: %s/%s <dirs> missing or empty — asking for the directories alone", owner, name)
			keep = askDirsAlone(owner, name, stats)
		}
		if len(keep) > 0 {
			initHeavy, initLight = pruneByDirs(initHeavy, initLight, lines, keep)
			log.Printf("surveyAndPlan: %s/%s keeping %d of %d dirs → %d heavy candidates, %d light", owner, name, len(keep), len(stats), len(initHeavy), len(initLight))
		} else {
			log.Printf("surveyAndPlan: %s/%s no usable directory answer, not pruning by directory", owner, name)
		}
	}

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
	if len(keep) > 0 {
		// the directory decision is the stricter, deliberate one: a file the survey
		// called heavy inside a directory the same answer skipped is light
		heavy, light = pruneByDirs(heavy, light, lines, keep)
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
