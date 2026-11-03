package kernel

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	tsRef  = "01529aaa82b9c1d2e3f4a5b6c7d8e9f001234567"
	tsConv = "b8b098a8-9f28-4d4a-8ec6-c8928cc9f62f"
)

func TestTheoryPlanIdentity(t *testing.T) {
	a := [][]string{{"src/a.cpp", "src/b.cpp"}, {"src/c.cu"}}
	p := newTheoryPlanRef(tsRef, a)
	if !regexp.MustCompile(`^\.einsteinOS/theory/01529aaa82b9/[0-9a-f]{10}$`).MatchString(p.Dir()) {
		t.Fatalf("plan dir %q", p.Dir())
	}
	if got := p.ChunkDir(1, tsConv); got != p.Dir()+"/01-b8b098a8-9f2" {
		t.Fatalf("chunk dir %q", got)
	}
	if p.PlanFile() != p.Dir()+"/plan.json" {
		t.Fatalf("plan file %q", p.PlanFile())
	}
	// same commit, same grouping: same place (a restart continues there)
	if q := newTheoryPlanRef(tsRef, [][]string{{"src/a.cpp", "src/b.cpp"}, {"src/c.cu"}}); q.Plan != p.Plan {
		t.Fatalf("same plan, different id: %s %s", p.Plan, q.Plan)
	}
	// anything else that changes what a chunk number means: another place
	others := map[string]theoryPlanRef{
		"another commit":  newTheoryPlanRef("ffff"+tsRef[4:], a),
		"another order":   newTheoryPlanRef(tsRef, [][]string{{"src/c.cu"}, {"src/a.cpp", "src/b.cpp"}}),
		"another split":   newTheoryPlanRef(tsRef, [][]string{{"src/a.cpp"}, {"src/b.cpp", "src/c.cu"}}),
		"a file moved in": newTheoryPlanRef(tsRef, [][]string{{"src/a.cpp", "src/b.cpp", "src/c.cu"}}),
	}
	for why, q := range others {
		if q.Dir() == p.Dir() {
			t.Errorf("%s: same directory %s", why, q.Dir())
		}
	}
	// the caller's slices are copied, not aliased
	a[0][0] = "changed"
	if p.Chunks[0][0] != "src/a.cpp" {
		t.Fatal("plan aliases the caller's chunks")
	}
	if (theoryPlanRef{}).Valid() || !p.Valid() {
		t.Fatal("Valid")
	}
	// plan.json: deterministic and complete
	if string(p.Manifest()) != string(newTheoryPlanRef(tsRef, p.Chunks).Manifest()) {
		t.Fatal("manifest not deterministic")
	}
	var back theoryPlanRef
	if err := json.Unmarshal(p.Manifest(), &back); err != nil || back.Ref != tsRef || back.Plan != p.Plan || len(back.Chunks) != 2 {
		t.Fatalf("manifest round trip: %v %+v", err, back)
	}
}

// The 2026/11/03 failure, replayed with the kernel's own git helpers: the shared branch
// already holds the old run's .einsteinOS/chunks/theorize_chunk_1 (and another plan of
// the same commit), and this run's chunk branches start from a base that does not have
// them. Every chunk must merge, and nothing already there may change.
func TestTheoryChunksMergeWithoutConflict(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("commit", "-q", "--allow-empty", "-m", "init")

	// what the earlier runs left on the shared branch
	oldPlan := newTheoryPlanRef(tsRef, [][]string{{"x.cpp"}, {"y.cpp"}})
	git("checkout", "-q", "-b", "einsteinOS/theory")
	write(".einsteinOS/chunks/theorize_chunk_1/conv.json", `{"run":"11-02"}`)
	write(".einsteinOS/chunks/theorize_chunk_1/SessionTokenLoop.lean", "-- 11-02\n")
	write(oldPlan.ChunkDir(1, "aaaaaaaa-old")+"/SessionTokenLoop.lean", "-- other plan\n")
	write(oldPlan.PlanFile(), string(oldPlan.Manifest()))
	git("add", "-A")
	git("commit", "-q", "-m", "earlier runs")
	before := git("rev-parse", "HEAD:.einsteinOS")

	// this run: 9 chunks in the real run, two are enough; each on its own branch from main
	plan := newTheoryPlanRef(tsRef, [][]string{{"src/a.cpp"}, {"src/b.cpp"}})
	convs := []string{tsConv, "59dc69f8-51a1-4d3e-80a5-a5e1b780e989"}
	for i, conv := range convs {
		branch := "conv/" + convShort(conv)
		if err := GitCheckoutOrCreate(dir, branch, "main"); err != nil {
			t.Fatal(err)
		}
		if cur := strings.TrimSpace(git("branch", "--show-current")); cur != branch {
			t.Fatalf("on %q, want %q", cur, branch)
		}
		write(plan.ChunkDir(i+1, conv)+"/conv.json", `{"run":"11-03"}`)
		write(plan.ChunkDir(i+1, conv)+"/SessionTokenLoop.lean", "-- 11-03 chunk\n")
		write(plan.PlanFile(), string(plan.Manifest()))
		if !GitCommit(dir, "chunk") {
			t.Fatal("commit")
		}
		git("checkout", "-q", "einsteinOS/theory")
		if err := GitMerge(dir, branch, "merge chunk"); err != nil {
			t.Fatalf("chunk %d: %v", i+1, err)
		}
	}
	// the earlier runs' files are exactly as they were
	for _, rel := range []string{".einsteinOS/chunks/theorize_chunk_1/conv.json", oldPlan.PlanFile(),
		oldPlan.ChunkDir(1, "aaaaaaaa-old") + "/SessionTokenLoop.lean"} {
		if git("show", "HEAD:"+rel) != git("show", "HEAD~2:"+rel) {
			t.Errorf("%s changed", rel)
		}
	}
	if before == git("rev-parse", "HEAD:.einsteinOS") {
		t.Fatal("nothing merged")
	}
	for i, conv := range convs {
		if got := git("show", "HEAD:"+plan.ChunkDir(i+1, conv)+"/SessionTokenLoop.lean"); got != "-- 11-03 chunk\n" {
			t.Errorf("chunk %d: %q", i+1, got)
		}
	}
	if git("show", "HEAD:"+plan.PlanFile()) != string(plan.Manifest()) {
		t.Error("plan.json")
	}
}

func TestPriorChunksContextByAddress(t *testing.T) {
	if PriorChunksContext(nil) != "" {
		t.Fatal("no prior chunks: no section")
	}
	got := PriorChunksContext([]priorChunk{{1, tsConv, 7}, {2, "59dc69f8-51a1-4d3e-80a5-a5e1b780e989", 10}})
	for _, want := range []string{"2 earlier chunk(s)", "chunk 1: " + tsConv + " (7 file(s))",
		"chunk 2: 59dc69f8-51a1-4d3e-80a5-a5e1b780e989 (10 file(s))", "einsteinos conv <addr> files", "einsteinos conv <addr> file <name>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	// nothing that points at a branch shared with other runs, and no format damage
	for _, bad := range []string{".einsteinOS/chunks", "git clone", "git checkout", "%!"} {
		if strings.Contains(got, bad) {
			t.Errorf("contains %q:\n%s", bad, got)
		}
	}
}

func TestConvFilesCommand(t *testing.T) {
	conv := "c0ffee00-1111-2222-3333-444455556666"
	files := []chunkFile{
		{RemotePath: "/mnt/user-data/outputs/ExpertVramCache.lean", Name: "ExpertVramCache.lean", Content: []byte("theorem a : True := trivial\n\n-- blank line above stays\n")},
		{RemotePath: "/mnt/user-data/outputs/sub/Dup.lean", Name: "Dup.lean", Content: []byte("x\n")},
		{RemotePath: "/mnt/user-data/outputs/other/Dup.lean", Name: "Dup.lean", Content: []byte("y\n")},
	}
	SetConvResult(conv, renderChunkFiles(files))

	// the parse is the inverse of the render
	back := convResultFiles(renderChunkFiles(files))
	if len(back) != 3 || back[0].Path != files[0].RemotePath || back[0].Content != string(files[0].Content) {
		t.Fatalf("round trip: %+v", back)
	}

	run := func(line string) map[string]any {
		t.Helper()
		in, _ := json.Marshal(map[string]string{"command": line})
		cmd, ok := EinsteinCommandFrom("bash_tool", string(in))
		if !ok {
			t.Fatalf("%q not recognised as a kernel command", line)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(HandleEinsteinCommand("parent", cmd)), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	list := run("einsteinos conv " + conv + " files")["files"].([]any)
	if len(list) != 3 || list[0].(map[string]any)["name"] != files[0].RemotePath {
		t.Fatalf("files: %v", list)
	}
	if b := list[0].(map[string]any)["bytes"].(float64); int(b) != len(files[0].Content) {
		t.Fatalf("bytes %v", b)
	}
	// by base name and by full path, the whole file
	for _, name := range []string{"ExpertVramCache.lean", files[0].RemotePath} {
		if got := run("einsteinos conv " + conv[:8] + " file " + name)["content"]; got != string(files[0].Content) {
			t.Fatalf("file %s: %q", name, got)
		}
	}
	// ambiguous or unknown names say what there is
	for _, name := range []string{"Dup.lean", "Nope.lean"} {
		e, _ := run("einsteinos conv " + conv + " file " + name)["error"].(string)
		if !strings.Contains(e, "ExpertVramCache.lean") {
			t.Fatalf("%s: %q", name, e)
		}
	}
	if got := run("einsteinos conv " + conv + " file " + files[1].RemotePath)["content"]; got != "x\n" {
		t.Fatalf("full path picks one of the duplicates: %q", got)
	}
	// a conversation with no files: an empty list, not an error
	if l := run("einsteinos conv deadbeef-0000 files")["files"].([]any); len(l) != 0 {
		t.Fatalf("unknown conv: %v", l)
	}
}
