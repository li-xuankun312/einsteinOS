package kernel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 11/05 03:17:40 a unit cloned the reference after reading its name in the dev branch's
// own files. The branch is masked; paths with the name are renamed consistently.
func TestTheDevBranchIsMaskedFilesAndPaths(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if o, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, o)
		}
	}
	put := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	run("init", "-q")
	put("STRATA_FEATURE_TABLE.md", "features of github.com/Niko1221/Strata\n")
	put("include/baloon/strata_pack_builder.hpp", "namespace strata { int STRATA_MAX = 1; }\n")
	put("src/use.cpp", "#include \"baloon/strata_pack_builder.hpp\"\nint x = strata::STRATA_MAX;\n")
	put("blob.bin", "Strata\x00\x01")
	put("clean.txt", "nothing here\n")
	put(".einsteinOS/plan.json", `{"ref":"Niko1221/Strata"}`)
	run("add", "-A")
	run("commit", "-qm", "init")

	changed, renamed, err := devMaskTree(dir, newRefMask(refMaskIdent, "Niko1221/Strata"))
	if err != nil || changed != 3 || renamed != 2 {
		t.Fatalf("changed=%d renamed=%d err=%v", changed, renamed, err)
	}
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(dir, p)); return string(b) }
	if got := read("include/baloon/ref_pack_builder.hpp"); got != "namespace ref { int REF_MAX = 1; }\n" {
		t.Fatalf("%q", got)
	}
	if got := read("src/use.cpp"); !strings.Contains(got, "\"baloon/ref_pack_builder.hpp\"") || !strings.Contains(got, "ref::REF_MAX") {
		t.Fatalf("references follow the rename: %q", got)
	}
	if read("REF_FEATURE_TABLE.md") != "features of reference\n" {
		t.Fatalf("%q", read("REF_FEATURE_TABLE.md"))
	}
	if read(".einsteinOS/plan.json") != `{"ref":"Niko1221/Strata"}` {
		t.Fatal("the kernel's plan keeps its identity")
	}
	if read("blob.bin") != "Strata\x00\x01" {
		t.Fatal("binary files are left alone")
	}
	if c, r, _ := devMaskTree(dir, newRefMask(refMaskIdent, "Niko1221/Strata")); c+r != 0 {
		t.Fatal("a second sweep finds nothing")
	}
}

// 11/05 08:07: 42 *.bc build outputs were committed with a unit
func TestBuildOutputsStayOutOfTheDeltaAndAreRemovedFromTheBranch(t *testing.T) {
	m := t.TempDir()
	os.WriteFile(filepath.Join(m, "a.cu"), []byte("__global__ void k(){}\n"), 0o644)
	os.WriteFile(filepath.Join(m, "a.bc"), []byte("BC\xc0\xde\x00\x01"), 0o644)
	snap, err := snapshotTree(m)
	if err != nil || snap["a.cu"] == "" || snap["a.bc"] != "" {
		t.Fatalf("binary files are not part of a unit's tree: %v %v", snap, err)
	}

	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if o, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, o)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "logo.png"), []byte("PNG\x00"), 0o644)
	run("add", "-A")
	run("commit", "-qm", "main")
	run("checkout", "-q", "-b", "dev")
	os.WriteFile(filepath.Join(dir, "k.bc"), []byte("BC\x00"), 0o644)
	os.WriteFile(filepath.Join(dir, "k.cu"), []byte("x"), 0o644)
	run("add", "-A")
	run("commit", "-qm", "unit")
	if n := devRemoveUnitBinaries(dir, "main"); n != 1 {
		t.Fatalf("only the unit's binary: %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "logo.png")); err != nil {
		t.Fatal("the repository's own binary stays")
	}
}
