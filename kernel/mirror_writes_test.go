package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
	want := "src/kernels/gpu/gdn_new.cu|0ea8884d|written src/kernels/gpu/gdn_qwen.cu|ffdec91c|removed src/kernels/gpu/qsa_layer.cu|ffdec91c|written"
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
	for _, w := range []string{"changed or removed 2 file(s)", "src/kernels/gpu/gdn_layer.cu removed (written by gdn, conversation 0ea8884d)", "include/baloon/rt_native.hpp changed (the planner gave it to gdn)"} {
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
	putTree(t, dir, map[string]string{"src/kernels/gpu/gdn_qwen.cu": "a"})
	noteMirrorConv("uint", dir, "92e537ed", "")
	noteMirrorConv("uint", dir, "0ea8884d", "gdn: runs one GDN sub-layer")
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
