package kernel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func resetMirrorUnits() {
	mirrorUnitsMu.Lock()
	mirrorUnits = map[string]*mirrorUnit{}
	mirrorConvs = map[string]string{}
	mirrorUnitsMu.Unlock()
}

// 11/23 16:02:33 and 16:07:27 two conversations each deleted one of the GDN files from the one
// mirror they shared, and the log named a pid, not a conversation: each command of a
// conversation on the mirror is set down with the files it changed and whose it was
func TestEachMirrorWriteIsSetDownAsTheConversations(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	putTree(t, dir, map[string]string{"src/kernels/gpu/gdn_qwen.cu": "a", "src/kernels/gpu/gdn_layer.cu": "b"})
	noteMirrorConv("uint", dir, "92e537ed-0000", "")
	noteMirrorConv("uint", dir, "0ea8884d-0000", "gdn: runs one GDN sub-layer")
	noteMirrorConv("uint", dir, "ffdec91c-0000", "qsa: runs one QSA sub-layer")
	MirrorCommand("0ea8884d-0000", func() string {
		os.WriteFile(filepath.Join(dir, "src/kernels/gpu/gdn_new.cu"), []byte("x"), 0o644)
		return "ok"
	})
	MirrorCommand("ffdec91c-0000", func() string {
		os.Remove(filepath.Join(dir, "src/kernels/gpu/gdn_qwen.cu"))
		os.WriteFile(filepath.Join(dir, "src/kernels/gpu/qsa_layer.cu"), []byte("y"), 0o644)
		return "ok"
	})
	if out := MirrorCommand("nobody", func() string { return "ran" }); out != "ran" {
		t.Fatalf("a conversation of no mirror ran as %q", out)
	}
	u := mirrorUnitOf("0ea8884d-0000")
	var got []string
	for _, w := range u.writes {
		got = append(got, w.File+"|"+shortID(w.Conv)+"|"+map[bool]string{true: "removed", false: "written"}[w.Removed])
	}
	// the removal of gdn_qwen.cu, a file the mirror held, was the qsa part's to make no more:
	// it was undone and is not a write of its
	want := "src/kernels/gpu/gdn_new.cu|0ea8884d|written src/kernels/gpu/qsa_layer.cu|ffdec91c|written"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v", got)
	}
	o := mirrorOwners("uint")
	if strings.Join(o["gdn"], " ") != "src/kernels/gpu/gdn_new.cu" || strings.Join(o["qsa"], " ") != "src/kernels/gpu/qsa_layer.cu" {
		t.Fatalf("owners %v", o)
	}
}

// the commands of the conversations on one mirror run one at a time on it
func TestMirrorCommandsRunOneAtATime(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	noteMirrorConv("uint", dir, "a", "")
	noteMirrorConv("uint", dir, "b", "p: x")
	var mu sync.Mutex
	in, most := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			MirrorCommand(c, func() string {
				mu.Lock()
				in++
				if in > most {
					most = in
				}
				mu.Unlock()
				for j := 0; j < 1000; j++ {
					_ = j * j
				}
				mu.Lock()
				in--
				mu.Unlock()
				return ""
			})
		}([]string{"a", "b"}[i%2])
	}
	wg.Wait()
	if most != 1 {
		t.Fatalf("%d commands ran at once on the mirror", most)
	}
}

// 11/23 16:07:27 a conversation deleted gdn_layer.cu, which another had written, and was told
// nothing of it: the command's result says which files of another conversation's — or another
// part's, by the planner's partfile lines — it changed or removed, and the log says so too
func TestACommandTouchingAnothersFileIsToldSo(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	putTree(t, dir, map[string]string{"include/baloon/rt_native.hpp": "x"})
	noteMirrorConv("uint", dir, "0ea8884d", "gdn: runs one GDN sub-layer")
	noteMirrorConv("uint", dir, "ffdec91c", "qsa: runs one QSA sub-layer")
	noteMirrorPartFiles("uint", map[string][]string{"gdn": {"include/baloon/rt_native.hpp"}})
	gdn := filepath.Join(dir, "src/kernels/gpu/gdn_layer.cu")
	os.MkdirAll(filepath.Dir(gdn), 0o755)
	if out := MirrorCommand("0ea8884d", func() string { os.WriteFile(gdn, []byte("a"), 0o644); return "ok" }); out != "ok" {
		t.Fatalf("its own write was noted: %q", out)
	}
	out := MirrorCommand("ffdec91c", func() string {
		os.Remove(gdn)
		os.WriteFile(filepath.Join(dir, "include/baloon/rt_native.hpp"), []byte("yy"), 0o644)
		return "ok"
	})
	for _, w := range []string{"wrote back 2 file(s)", "src/kernels/gpu/gdn_layer.cu (the file of gdn, conversation 0ea8884d, which wrote it first)", "include/baloon/rt_native.hpp (the file the planner gave gdn)"} {
		if !strings.Contains(out, w) {
			t.Fatalf("missing %q in %q", w, out)
		}
	}
	if out := MirrorCommand("0ea8884d", func() string { os.WriteFile(gdn, []byte("b"), 0o644); return "ok" }); strings.Contains(out, "changed or removed") {
		t.Fatalf("a file it was first to write again is its own: %q", out)
	}
}

// 11/23 16:12:48 "exit differs: sandbox 0, here 1" for head gdn_qwen.cu was the one trace that a
// part's sandbox and the mirror had parted: each command's result carries the files the other
// conversations changed in the mirror since the conversation was last told, once
func TestEachCommandIsToldWhatTheOthersChangedSince(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	putTree(t, dir, map[string]string{"src/kernels/gpu/.keep": ""})
	noteMirrorConv("uint", dir, "92e537ed", "")
	noteMirrorConv("uint", dir, "0ea8884d", "gdn: runs one GDN sub-layer")
	MirrorCommand("0ea8884d", func() string {
		os.WriteFile(filepath.Join(dir, "src/kernels/gpu/gdn_qwen.cu"), []byte("a"), 0o644)
		return "ok"
	})
	MirrorCommand("92e537ed", func() string { return "" }) // told of the write
	MirrorCommand("0ea8884d", func() string {
		os.Remove(filepath.Join(dir, "src/kernels/gpu/gdn_qwen.cu"))
		return "ok"
	})
	out := MirrorCommand("92e537ed", func() string { return "head: cannot open" })
	if !strings.Contains(out, "changed 1 file(s) of the mirror, which its sandbox does not have:\n  src/kernels/gpu/gdn_qwen.cu removed by gdn (conversation 0ea8884d)") {
		t.Fatalf("%q", out)
	}
	if out := MirrorCommand("92e537ed", func() string { return "again" }); out != "again" {
		t.Fatalf("told twice: %q", out)
	}
}

// 11/23 15:45:59 the planner gave no partfile line, and the parts started later (visual
// 16:58:59, mtp 17:12:02, the errored ones again) were given no file of theirs or of the
// others': a part is then given the files each part's conversation was first to write
func TestWithoutPartfileLinesThePartsAreGivenTheFilesMeasured(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	noteMirrorConv("uint", dir, "0ea8884d", "gdn: runs one GDN sub-layer")
	noteMirrorConv("uint", dir, "ffdec91c", "qsa: runs one QSA sub-layer")
	if got := partFilesOf("uint", "gdn: x", nil); got != "" {
		t.Fatalf("nothing written yet, given %q", got)
	}
	MirrorCommand("0ea8884d", func() string {
		os.MkdirAll(filepath.Join(dir, "src"), 0o755)
		os.WriteFile(filepath.Join(dir, "src/gdn_layer.cu"), []byte("a"), 0o644)
		return ""
	})
	MirrorCommand("ffdec91c", func() string {
		os.WriteFile(filepath.Join(dir, "src/qsa_layer.cu"), []byte("b"), 0o644)
		return ""
	})
	got := partFilesOf("uint", "qsa: runs one QSA sub-layer", nil)
	for _, w := range []string{"measured: the files its earlier conversation was first to write", "this part wrote these 1 file(s):\n  src/qsa_layer.cu", "src/gdn_layer.cu (gdn)"} {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q in\n%s", w, got)
		}
	}
	planned := partFilesOf("uint", "qsa: x", map[string][]string{"qsa": {"a.cu"}})
	if !strings.Contains(planned, "the planner named them") || strings.Contains(planned, "measured") {
		t.Fatalf("the planner's files give way: %s", planned)
	}
}

// 11/23 20:27:45 a command of the gdn part read 14 files out of a remote branch the mirror's
// clone did not have and left each of them 0 bytes here, build.sh among them: a file a command
// leaves empty that had bytes is written back and the command is told
func TestAFileACommandLeftEmptyIsWrittenBack(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	build := ".einsteinOS/verify/uint/build.sh"
	putTree(t, dir, map[string]string{build: "#!/bin/bash\nnvcc a.cu\n", "src/empty.cu": ""})
	noteMirrorConv("uint", dir, "25f1be30", "gdn: x")
	noteMirrorConv("uint", dir, "8c680f2c", "")
	out := MirrorCommand("25f1be30", func() string {
		os.WriteFile(filepath.Join(dir, build), nil, 0o644)
		os.WriteFile(filepath.Join(dir, "src/empty.cu"), nil, 0o644)
		return "ok"
	})
	if b, _ := os.ReadFile(filepath.Join(dir, build)); string(b) != "#!/bin/bash\nnvcc a.cu\n" {
		t.Fatalf("not written back: %q", b)
	}
	if !strings.Contains(out, "wrote back 1 file(s)") || !strings.Contains(out, build+" (left empty here, 22 bytes before)") {
		t.Fatalf("%q", out)
	}
	// a file it writes with bytes stays as written, and the next empty-making is undone from it
	MirrorCommand("8c680f2c", func() string { os.WriteFile(filepath.Join(dir, build), []byte("make\n"), 0o644); return "" })
	MirrorCommand("8c680f2c", func() string { os.WriteFile(filepath.Join(dir, build), nil, 0o644); return "" })
	if b, _ := os.ReadFile(filepath.Join(dir, build)); string(b) != "make\n" {
		t.Fatalf("%q", b)
	}
}

// 11/23 21:00-23:05 the moe, embed, loader and qsa parts each rewrote gdn_bf16_layer.cu, the gdn
// part's, and were only told: a part's command that changes or removes another's file is undone
// for it; the unit's own conversation, which puts the parts together, is told and not undone
func TestAPartsCommandOnAnothersFileIsUndone(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	f := "src/kernels/gpu/gdn_bf16_layer.cu"
	putTree(t, dir, map[string]string{"src/kernels/gpu/.keep": ""})
	noteMirrorConv("uint", dir, "8c680f2c", "")
	noteMirrorConv("uint", dir, "25f1be30", "gdn: x")
	noteMirrorConv("uint", dir, "a6d47c28", "moe: y")
	MirrorCommand("25f1be30", func() string { os.WriteFile(filepath.Join(dir, f), []byte("gdn's"), 0o644); return "" })
	out := MirrorCommand("a6d47c28", func() string { os.WriteFile(filepath.Join(dir, f), []byte("moe's"), 0o644); return "" })
	if b, _ := os.ReadFile(filepath.Join(dir, f)); string(b) != "gdn's" || !strings.Contains(out, f+" (the file of gdn, conversation 25f1be30") {
		t.Fatalf("%q %q", b, out)
	}
	// 11/24 04:38:52-04:39:37 three parts each added to moe_parity.cu, the moe part's: lines added
	// to another part's own file are undone too
	if out := MirrorCommand("a6d47c28", func() string {
		os.WriteFile(filepath.Join(dir, f), []byte("gdn's\n// moe's declaration\n"), 0o644)
		return ""
	}); !strings.Contains(out, "wrote back") {
		t.Fatalf("lines added to another part's file stood: %q", out)
	}
	MirrorCommand("a6d47c28", func() string { os.Remove(filepath.Join(dir, f)); return "" })
	if b, _ := os.ReadFile(filepath.Join(dir, f)); string(b) != "gdn's" {
		t.Fatalf("a removal stands: %q", b)
	}
	out = MirrorCommand("8c680f2c", func() string { os.WriteFile(filepath.Join(dir, f), []byte("joined"), 0o644); return "" })
	if b, _ := os.ReadFile(filepath.Join(dir, f)); string(b) != "joined" || !strings.Contains(out, "changed or removed 1 file(s)") {
		t.Fatalf("the unit's conversation: %q %q", b, out)
	}
}

// 11/23 21:00-23:05 four parts each went to fix the GDN files their own builds failed on: the
// files a failed compile names go back with whose they are
func TestTheFilesACompileErrorNamesGoBackWithWhoseTheyAre(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	noteMirrorConv("uint", dir, "25f1be30", "gdn: x")
	noteMirrorConv("uint", dir, "a6d47c28", "moe: y")
	MirrorCommand("25f1be30", func() string {
		os.MkdirAll(filepath.Join(dir, "src/kernels/gpu"), 0o755)
		os.WriteFile(filepath.Join(dir, "src/kernels/gpu/gdn_bf16_layer.cu"), []byte("x"), 0o644)
		return ""
	})
	out := "/tmp/einsteinos_verify_uint/src/kernels/gpu/gdn_bf16_layer.cu:12:3: error: use of undeclared identifier 'kgc'\n" +
		"/tmp/einsteinos_verify_uint/src/kernels/gpu/gdn_bf16_layer.cu(40): error: x\n/usr/include/stdio.h:3: note\n"
	got := compileErrorOwners("uint", out)
	if !strings.Contains(got, "1 file(s):\n  src/kernels/gpu/gdn_bf16_layer.cu: 2 line(s) named — the file of gdn, conversation 25f1be30") {
		t.Fatalf("%q", got)
	}
	if compileErrorOwners("uint", "ld: undefined reference to foo") != "" {
		t.Fatal("no file named, something said")
	}
}

// 11/23 the port map's 168 rows were cut to one by the layer_norm part, first to touch it in the
// attempt, and at 20:27:45 the gdn part overwrote 14 files the tree had held all along: a file
// the mirror held when the conversations began is no part's own — a part adds lines to it, and
// a part's command taking lines out is undone; the unit's conversation may
func TestAFileTheMirrorHeldIsSharedByTheParts(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	pm := ".einsteinOS/port/uint.tsv"
	putTree(t, dir, map[string]string{pm: "a\tb\tc\td\ne\tf\tg\th\n"})
	noteMirrorConv("uint", dir, "8c680f2c", "")
	noteMirrorConv("uint", dir, "c845b59f", "layer_norm: x")
	noteMirrorConv("uint", dir, "41b9095a", "loader: y")
	MirrorCommand("c845b59f", func() string {
		os.WriteFile(filepath.Join(dir, pm), []byte("a\tb\tc\td\ne\tf\tg\th\nmissing:n\tk\tf\tk\n"), 0o644)
		return ""
	})
	out := MirrorCommand("41b9095a", func() string { os.WriteFile(filepath.Join(dir, pm), []byte("only\n"), 0o644); return "" })
	if b, _ := os.ReadFile(filepath.Join(dir, pm)); !strings.HasSuffix(string(b), "missing:n\tk\tf\tk\n") || !strings.Contains(out, "shared by all its parts") {
		t.Fatalf("%q %q", b, out)
	}
	MirrorCommand("8c680f2c", func() string { os.WriteFile(filepath.Join(dir, pm), []byte("joined\n"), 0o644); return "" })
	if b, _ := os.ReadFile(filepath.Join(dir, pm)); string(b) != "joined\n" {
		t.Fatalf("the unit's conversation: %q", b)
	}
}

// 11/24 03:34:27 the loader part was said to have written elementwise_parity.cu, layer_norm's,
// which the unit's own conversation had changed: only the part's own commands count
func TestAPartIsSaidToHaveWrittenOnlyWhatItsCommandsWrote(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	noteMirrorConv("uint", dir, "fcaad41c", "")
	noteMirrorConv("uint", dir, "826ed16e", "loader: x")
	noteMirrorConv("uint", dir, "c845b59f", "layer_norm: y")
	f := "tests/k39_integration/elementwise_parity.cu"
	files := map[string][]string{"layer_norm": {f}}
	MirrorCommand("fcaad41c", func() string {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), []byte("joined"), 0o644)
		return ""
	})
	if got := partWroteOthersBy("uint", "826ed16e", "loader: x", files); len(got) != 0 {
		t.Fatalf("the unit's conversation's write laid on the loader: %v", got)
	}
	MirrorCommand("826ed16e", func() string {
		os.WriteFile(filepath.Join(dir, f), []byte("joined\nloader's line\n"), 0o644)
		return ""
	})
	if got := partWroteOthersBy("uint", "826ed16e", "loader: x", files); len(got) != 1 || got[0] != f+" (the unit's conversation)" {
		t.Fatalf("%v", got)
	}
}

// 11/24 03:43:56 the qsa part's edit of qsa_layer_parity.cu — a file the tree held, and one the
// planner gave the qsa part — was undone as a shared file's: a file the planner gave a part is
// that part's to change, and another part's to add lines to only
func TestAFileThePlannerGaveAPartIsItsToChange(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	f := "tests/k39_integration/qsa_layer_parity.cu"
	putTree(t, dir, map[string]string{f: "old line\n"})
	noteMirrorConv("uint", dir, "1be9d73f", "qsa: x")
	noteMirrorConv("uint", dir, "46fd9bb0", "moe: y")
	noteMirrorPartFiles("uint", map[string][]string{"qsa": {f}})
	if out := MirrorCommand("1be9d73f", func() string { os.WriteFile(filepath.Join(dir, f), []byte("new line\n"), 0o644); return "" }); strings.Contains(out, "wrote back") {
		t.Fatalf("the qsa part's own file was undone: %q", out)
	}
	out := MirrorCommand("46fd9bb0", func() string { os.WriteFile(filepath.Join(dir, f), []byte("moe's\n"), 0o644); return "" })
	if b, _ := os.ReadFile(filepath.Join(dir, f)); string(b) != "new line\n" || !strings.Contains(out, "the file the planner gave qsa") {
		t.Fatalf("%q %q", b, out)
	}
}

// 11/24 03:34:27 and 03:47:50 the parts refused for the account's concurrency ended with this
// error; it is told apart from a part's own failure
func TestAPartRefusedForConcurrencyIsToldApart(t *testing.T) {
	out := `... [Error] {"type":"error","error":{"type":"rate_limit_error","message":"{\"type\":\"exceeded_limit\",\"representativeClaim\":\"concurrents\"}","details":{"error_code":"concurrent_limit_reached","message":"x"}}}`
	if why := conversationError(out); !strings.Contains(why, concurrentLimitCode) {
		t.Fatalf("%q", why)
	}
}

// 11/24 09:39:46 a replayed `bash build.sh` held the mirror 7 minutes and every other command
// waited: a command waits at most mirrorTurnWait for another's, then runs beside it
func TestALongCommandDoesNotHoldTheMirrorFromTheOthers(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	noteMirrorConv("uint", dir, "117e9977", "")
	noteMirrorConv("uint", dir, "1ea83d67", "moe: x")
	release := make(chan bool)
	started := make(chan bool)
	go MirrorCommand("117e9977", func() string { started <- true; <-release; return "" })
	<-started
	done := make(chan time.Duration)
	go func() {
		t0 := time.Now()
		MirrorCommand("1ea83d67", func() string { return "" })
		done <- time.Since(t0)
	}()
	select {
	case d := <-done:
		if d < mirrorTurnWait-time.Second {
			t.Fatalf("ran beside a running command after %v, before the wait", d)
		}
	case <-time.After(mirrorTurnWait + 5*time.Second):
		t.Fatal("waited past mirrorTurnWait for a command still running")
	}
	close(release)
}

// 11/25 04:23:56 machine_down_during_test held on src/ix_router_top10_wave.cu: the gate goes back
// with whose file each one it holds on is — written first by a part, or given one by the planner
func TestTheFilesAGateHoldsOnGoBackWithWhoseTheyAre(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	noteMirrorConv("uint", dir, "a6d47c28", "moe: y")
	noteMirrorPartFiles("uint", map[string][]string{"router: z": {"src/ix_router_top10_wave.cu"}})
	MirrorCommand("a6d47c28", func() string {
		os.MkdirAll(filepath.Join(dir, "src"), 0o755)
		os.WriteFile(filepath.Join(dir, "src/moe.cu"), []byte("x"), 0o644)
		return ""
	})
	got := gateFileOwners("uint", "machine_down_during_test", []string{"src/ix_router_top10_wave.cu", "src/moe.cu", "src/none.cu"})
	for _, want := range []string{
		"[WHOSE FILES THE GATE machine_down_during_test HOLDS ON",
		"3 file(s):\n",
		"  src/ix_router_top10_wave.cu: the file the planner gave router: z\n",
		"  src/moe.cu: the file of moe, conversation a6d47c28, which wrote it first\n",
		"  src/none.cu: no part's",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
	if gateFileOwners("uother", "g", []string{"a"}) != "" || gateFileOwners("uint", "g", nil) != "" {
		t.Fatal("no mirror or no file, something said")
	}
}

// 11/25 04:34:25 the linker named tests/k39_integration/e2e_all_check.cu by its base name and a
// section: the one file of the mirror with that name is said, with whose it is
func TestAFileTheLinkerNamesByItsBaseNameGoesBackWithWhoseItIs(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	noteMirrorConv("uint", dir, "8c680f2c", "")
	MirrorCommand("8c680f2c", func() string {
		os.MkdirAll(filepath.Join(dir, "tests/k39_integration"), 0o755)
		os.WriteFile(filepath.Join(dir, "tests/k39_integration/e2e_all_check.cu"), []byte("x"), 0o644)
		os.WriteFile(filepath.Join(dir, "tests/k39_integration/a.cu"), []byte("x"), 0o644)
		os.MkdirAll(filepath.Join(dir, "src"), 0o755)
		os.WriteFile(filepath.Join(dir, "src/a.cu"), []byte("x"), 0o644)
		return ""
	})
	out := "e2e_all_check.cu:(.text+0x58d): undefined reference to `e2e_embed_main()'\na.cu:(.text+0x1): undefined reference to `g()'\n"
	got := compileErrorOwners("uint", out)
	if !strings.Contains(got, "1 file(s):\n  tests/k39_integration/e2e_all_check.cu: 1 line(s) named") {
		t.Fatalf("%q", got)
	}
	if strings.Contains(got, "a.cu: ") && !strings.Contains(got, "e2e_all_check.cu") {
		t.Fatalf("a base name two files have was said: %q", got)
	}
}

// 11/25 06:31-06:48 src/rt/graph.cpp, a file the mirror held when the conversations began, had
// been made `static bool fail` by a command, and the compiler's files were said to be no part's:
// a shared file is said to be shared, with the conversations whose commands changed it
func TestASharedFileTheCompilerNamesSaysWhoChangedIt(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src/rt"), 0o755)
	os.MkdirAll(filepath.Join(dir, "include/baloon"), 0o755)
	os.WriteFile(filepath.Join(dir, "src/rt/graph.cpp"), []byte("bool fail(const char* m) { return false; }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "include/baloon/rt_graph.hpp"), []byte("bool fail(const char* msg);\n"), 0o644)
	noteMirrorConv("uint", dir, "8c680f2c", "")
	MirrorCommand("8c680f2c", func() string {
		os.WriteFile(filepath.Join(dir, "src/rt/graph.cpp"), []byte("static bool fail(const char* m) { return false; }\n"), 0o644)
		return ""
	})
	out := "src/rt/graph.cpp:9:13: error: static declaration of 'fail' follows non-static declaration\ninclude/baloon/rt_graph.hpp:56:6: note: previous declaration is here\n"
	got := compileErrorOwners("uint", out)
	for _, want := range []string{
		"  src/rt/graph.cpp: 1 line(s) named — shared — the mirror held it when the conversations began; changed since by the unit's conversation (conversation 8c680f2c) (the last first)",
		"  include/baloon/rt_graph.hpp: 1 line(s) named — shared — the mirror held it when the conversations began, and no command of theirs has changed it",
		"    - bool fail(const char* m) { return false; }\n    + static bool fail(const char* m) { return false; }\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "    its lines changed since the conversations began, as the mirror holds it now (") {
		t.Fatalf("not said to be the mirror now:\n%s", got)
	}
	if strings.Contains(got, "rt_graph.hpp: 1 line(s) named — shared — the mirror held it when the conversations began, and no command of theirs has changed it\n    its lines") {
		t.Fatalf("an unchanged file given changed lines:\n%s", got)
	}
}

// 11/25 07:53:13 tests/gpu_unit7/test_ilu_qsa.cu and tests/k14gpu_mmvq/test_mmvq.cu were given
// among the compiler's files for their -Wformat warnings: only an error line and the two after it
// name a file — the 'note: previous declaration' line among them
func TestAWarningAloneNamesNoFile(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	for _, f := range []string{"src/rt/layout_check.cpp", "include/baloon/rt_layout.hpp", "tests/k14gpu_mmvq/test_mmvq.cu"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644)
	}
	noteMirrorConv("uint", dir, "3289499f", "")
	out := "tests/k14gpu_mmvq/test_mmvq.cu:120:91: warning: format specifies type 'float' [-Wformat]\n  x\n  y\n" +
		"src/rt/layout_check.cpp:9:13: error: static declaration of 'fail' follows non-static declaration\n" +
		"static bool fail(std::string* why, const std::string& msg) {}\n" +
		"include/baloon/rt_layout.hpp:32:6: note: previous declaration is here\n"
	got := compileErrorOwners("uint", out)
	if strings.Contains(got, "test_mmvq.cu") || !strings.Contains(got, "2 file(s):") || !strings.Contains(got, "src/rt/layout_check.cpp: 1 line(s)") || !strings.Contains(got, "include/baloon/rt_layout.hpp: 1 line(s)") {
		t.Fatalf("%s", got)
	}
}

// 11/25 07:05 a run started again: the mirror began at the parked tree, src/rt/graph.cpp already
// `static bool fail` from the run before, and nothing it changed was seen. The lines are compared
// with the branch the unit started from, where it was `bool fail`.
func TestAFilesLinesAreComparedWithTheBranchTheUnitStartedFrom(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	defer func() {
		mirrorBranchMu.Lock()
		delete(mirrorBranch, "uint")
		delete(mirrorBranchAt, "uint")
		mirrorBranchMu.Unlock()
	}()
	clone := t.TempDir()
	git := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "dev")
	os.MkdirAll(filepath.Join(clone, "src/rt"), 0o755)
	os.WriteFile(filepath.Join(clone, "src/rt/graph.cpp"), []byte("bool fail(const char* m) { return false; }\n"), 0o644)
	git("add", ".")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "dev")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src/rt"), 0o755)
	os.WriteFile(filepath.Join(dir, "src/rt/graph.cpp"), []byte("static bool fail(const char* m) { return false; }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "src/rt/new.cpp"), []byte("int x;\n"), 0o644)
	noteMirrorBranch("uint", clone, "dev")
	noteMirrorConv("uint", dir, "3289499f", "")
	out := "src/rt/graph.cpp:9:13: error: static declaration of 'fail' follows non-static declaration\nsrc/rt/new.cpp:1:1: error: y\n"
	got := compileErrorOwners("uint", out)
	if !strings.Contains(got, "    its lines changed since dev, the branch the unit started from, as the mirror holds it now (") ||
		!strings.Contains(got, "    - bool fail(const char* m) { return false; }\n    + static bool fail(const char* m) { return false; }\n") {
		t.Fatalf("%s", got)
	}
	if strings.Count(got, "its lines changed") != 1 {
		t.Fatalf("a file the branch has not given lines:\n%s", got)
	}
}

// 11/25 12:48:22 tests/k39_integration/embed_parity.cu was not on the dev branch the unit
// started from: its lines are compared with what the mirror held as the conversations began,
// and that the branch has no such file is said
func TestAFileTheBranchHasNotIsComparedWithTheMirrorsStart(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	defer func() {
		mirrorBranchMu.Lock()
		delete(mirrorBranch, "uint")
		delete(mirrorBranchAt, "uint")
		mirrorBranchMu.Unlock()
	}()
	clone := t.TempDir()
	git := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "dev")
	os.WriteFile(filepath.Join(clone, "README"), []byte("x\n"), 0o644)
	git("add", ".")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "dev")
	dir := t.TempDir()
	f := "tests/k39_integration/embed_parity.cu"
	os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
	os.WriteFile(filepath.Join(dir, f), []byte("int main() { return 0; }\n"), 0o644)
	noteMirrorBranch("uint", clone, "dev")
	noteMirrorConv("uint", dir, "b10706e5", "")
	os.WriteFile(filepath.Join(dir, f), []byte("int e2e_embed_main() { return 0; }\nint main() { return 0; }\n"), 0o644)
	mirrorUnitsMu.Lock()
	u := mirrorUnits["uint"]
	mirrorUnitsMu.Unlock()
	u.mu.Lock()
	u.content[f] = []byte("int e2e_embed_main() { return 0; }\nint main() { return 0; }\n")
	got := u.changedSinceStart(f)
	u.mu.Unlock()
	if !strings.Contains(got, "its lines changed since the conversations began (dev, the branch the unit started from, has no such file)") ||
		!strings.Contains(got, "    + int e2e_embed_main() { return 0; }\n") {
		t.Fatalf("%q", got)
	}
}

// 11/25 12:11:52 the embed part took the port map's duplicate rows out and made graph.cpp's
// fail static, both written back: a part starting is given the shared files written so far,
// the port map among them, and why it takes no line out of them
func TestAPartStartsKnowingTheFilesItShares(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	dir := t.TempDir()
	putTree(t, dir, map[string]string{
		PortMapPath("uint"): "a\tb\tc\n",
		"src/rt/graph.cpp":  "bool fail(const char* m) { return false; }\n",
		"src/other.cpp":     "int x;\n",
	})
	noteMirrorConv("uint", dir, "a3e69c50", "")
	MirrorCommand("a3e69c50", func() string {
		os.WriteFile(filepath.Join(dir, "src/rt/graph.cpp"), []byte("bool fail(const char* m) { return false; }\nint y;\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "src/new.cu"), []byte("int z;\n"), 0o644)
		return ""
	})
	got := partSharedBlock("uint")
	for _, want := range []string{
		"each is " + mirrorSharedFmt + ";",
		"2 of them written in this attempt so far:\n  " + PortMapPath("uint") + "\n  src/rt/graph.cpp\n",
		"all 3 file(s) the mirror held as the conversations began are shared alike",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "src/new.cu") || strings.Contains(got, "src/other.cpp") {
		t.Fatalf("a file of no one's, or not written, named:\n%s", got)
	}
	if partSharedBlock("nounit") != "" {
		t.Fatal("a unit with no mirror given a block")
	}
}

// 11/25 16:03:11 and 16:22:14 the compile failed on embed_parity.cu:133, the e2e_embed_main the
// embed part had added: a part takes out a line the attempt put in a shared file, and is
// written back only for a line the branch the unit started from holds
func TestAPartTakesOutWhatTheAttemptPutInASharedFile(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	defer func() {
		mirrorBranchMu.Lock()
		delete(mirrorBranch, "uint")
		delete(mirrorBranchAt, "uint")
		mirrorBranchMu.Unlock()
	}()
	clone := t.TempDir()
	git := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "dev")
	putTree(t, clone, map[string]string{"src/rt/graph.cpp": "bool fail(const char* m) { return false; }\nint g;\n"})
	git("add", ".")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "dev")
	dir := t.TempDir()
	embed := "tests/k39_integration/embed_parity.cu"
	putTree(t, dir, map[string]string{
		embed:              "int e2e_embed_main() {\n  return 0;\n}\nint main() { return 1; }\n",
		"src/rt/graph.cpp": "bool fail(const char* m) { return false; }\nint g;\nint added_by_a_part;\n",
	})
	noteMirrorBranch("uint", clone, "dev")
	noteMirrorConv("uint", dir, "a3e69c50", "")
	noteMirrorConv("uint", dir, "0e57c6bd", "loader: the safetensors loader")
	read := func(f string) string { b, _ := os.ReadFile(filepath.Join(dir, f)); return string(b) }
	// a file the branch has not: every line of it the attempt's
	MirrorCommand("0e57c6bd", func() string {
		os.WriteFile(filepath.Join(dir, embed), []byte("int main() { return 1; }\n"), 0o644)
		return ""
	})
	if got := read(embed); got != "int main() { return 1; }\n" {
		t.Fatalf("the attempt's lines of a file the branch has not were written back: %q", got)
	}
	// a line the attempt put in a file the branch has: taken out
	MirrorCommand("0e57c6bd", func() string {
		os.WriteFile(filepath.Join(dir, "src/rt/graph.cpp"), []byte("bool fail(const char* m) { return false; }\nint g;\n"), 0o644)
		return ""
	})
	if got := read("src/rt/graph.cpp"); strings.Contains(got, "added_by_a_part") {
		t.Fatalf("a line the attempt put in was written back: %q", got)
	}
	// a line the branch holds: written back
	MirrorCommand("0e57c6bd", func() string {
		os.WriteFile(filepath.Join(dir, "src/rt/graph.cpp"), []byte("static bool fail(const char* m) { return false; }\nint g;\n"), 0o644)
		return ""
	})
	if got := read("src/rt/graph.cpp"); !strings.HasPrefix(got, "bool fail(") {
		t.Fatalf("a line of the branch changed and kept: %q", got)
	}
}

// 11/25 23:24:24 a command of the loader part changed 699 files of the mirror at once — the
// whole tree — and was told only of the 90 written back: a command that changes many files is
// measured against the branch the unit started from, and what it set back is given
func TestACommandThatSetsManyFilesBackToTheBranchIsTold(t *testing.T) {
	resetMirrorUnits()
	defer resetMirrorUnits()
	defer func() {
		mirrorBranchMu.Lock()
		delete(mirrorBranch, "uint")
		delete(mirrorBranchAt, "uint")
		mirrorBranchMu.Unlock()
	}()
	clone, dir := t.TempDir(), t.TempDir()
	files := map[string]string{}
	for i := 0; i < mirrorBulkMin+5; i++ {
		files[fmt.Sprintf("src/f%03d.cu", i)] = fmt.Sprintf("branch %d\n", i)
	}
	putTree(t, clone, files)
	putTree(t, dir, files)
	for _, args := range [][]string{{"init", "-q", "-b", "dev"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "dev"}} {
		if out, err := exec.Command("git", append([]string{"-C", clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	noteMirrorBranch("uint", clone, "dev")
	noteMirrorConv("uint", dir, "e3bf792a", "loader: the safetensors loader")
	// the attempt's work: every file carries a line of its own
	MirrorCommand("e3bf792a", func() string {
		for f, c := range files {
			os.WriteFile(filepath.Join(dir, f), []byte(c+"the attempt's line\n"), 0o644)
		}
		return ""
	})
	// a git command writing the working tree: every file back to the branch's
	got := MirrorCommand("e3bf792a", func() string {
		for f, c := range files {
			os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644)
		}
		return "ok"
	})
	if !strings.Contains(got, fmt.Sprintf("this one command changed %d file(s) of the mirror, and %d of them now hold what the branch the unit started from holds", len(files), len(files))) ||
		!strings.Contains(got, "src/f000.cu") {
		t.Fatalf("%q", got)
	}
	// one file changed: nothing of the kind said
	if got := MirrorCommand("e3bf792a", func() string {
		os.WriteFile(filepath.Join(dir, "src/f000.cu"), []byte("branch 0\nx\n"), 0o644)
		return ""
	}); strings.Contains(got, "now hold what the branch") {
		t.Fatalf("%q", got)
	}
}
