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
