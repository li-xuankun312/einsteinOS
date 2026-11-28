package kernel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func archiveOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: "Strata-abc/", Typeflag: tar.TypeDir, Mode: 0o755})
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "Strata-abc/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func bareRepo(t *testing.T, origin string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	diagGitOut(t, dir, "init", "-q", "--bare", ".")
	if origin != "" {
		diagGitOut(t, dir, "config", "remote.origin.url", origin)
	}
	return dir
}

func hasObject(dir, id string) bool {
	return exec.Command("git", "--git-dir", dir, "cat-file", "-e", id).Run() == nil
}

func TestGitBlobIDIsWhatGitGives(t *testing.T) {
	dir := bareRepo(t, "")
	for _, content := range []string{"", "alpha", "two\nlines\n", "bin\x00ary\xff"} {
		c := exec.Command("git", "--git-dir", dir, "hash-object", "--stdin")
		c.Stdin = strings.NewReader(content)
		out, err := c.Output()
		if err != nil || strings.TrimSpace(string(out)) != gitBlobID([]byte(content)) {
			t.Fatalf("%q: git %q, ours %s (%v)", content, out, gitBlobID([]byte(content)), err)
		}
	}
}

func TestImportArchiveBlobsWritesOnlyWhatIsWanted(t *testing.T) {
	dir := bareRepo(t, "")
	files := map[string]string{"a.txt": "alpha", "src/b.cpp": "beta\nbeta\n", "c.bin": "bin\x00ary"}
	want := map[string]bool{gitBlobID([]byte("alpha")): true, gitBlobID([]byte("beta\nbeta\n")): true}
	n, err := importArchiveBlobs(dir, bytes.NewReader(archiveOf(t, files)), want)
	if err != nil || n != 2 || len(want) != 0 {
		t.Fatalf("n=%d err=%v want left %v", n, err, want)
	}
	for _, c := range []string{"alpha", "beta\nbeta\n"} {
		if !hasObject(dir, gitBlobID([]byte(c))) {
			t.Fatalf("blob of %q is not in the repository", c)
		}
	}
	if hasObject(dir, gitBlobID([]byte("bin\x00ary"))) {
		t.Fatal("a blob that was not wanted was written")
	}
}

func TestRefOriginRepoParsesTheShapesOfAnOrigin(t *testing.T) {
	for _, c := range []struct{ url, owner, name string }{
		{"https://github.com/Niko1221/Strata.git", "Niko1221", "Strata"},
		{"https://github.com/Niko1221/Strata", "Niko1221", "Strata"},
		{"git@github.com:Niko1221/Strata.git", "Niko1221", "Strata"},
		{"https://x-access-token:tok@github.com/o/n.git", "o", "n"},
	} {
		dir := bareRepo(t, c.url)
		o, n, ok := refOriginRepo(dir)
		if !ok || o != c.owner || n != c.name {
			t.Fatalf("%s: %q %q %v", c.url, o, n, ok)
		}
	}
	if _, _, ok := refOriginRepo(bareRepo(t, "https://example.com/o/n.git")); ok {
		t.Fatal("a repository that is not GitHub's was taken for one")
	}
}

func withArchiveServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	srv := httptest.NewServer(h)
	old := refArchiveURL
	refArchiveURL = func(owner, name, sha string) string { return srv.URL + "/" + owner + "/" + name + "/tar.gz/" + sha }
	t.Cleanup(func() { refArchiveURL = old; srv.Close() })
}

func TestPrefetchViaArchiveTakesTheBlobsFromTheArchive(t *testing.T) {
	files := map[string]string{"a.txt": "alpha", "b.txt": "beta"}
	var gotPath string
	withArchiveServer(t, func(w http.ResponseWriter, r *http.Request) { gotPath = r.URL.Path; w.Write(archiveOf(t, files)) })
	dir := bareRepo(t, "https://github.com/Niko1221/Strata.git")
	oids := []string{gitBlobID([]byte("alpha")), gitBlobID([]byte("beta"))}
	if err := prefetchViaArchive(dir, "01529aaa82b9", oids); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/Niko1221/Strata/tar.gz/01529aaa82b9" {
		t.Fatalf("asked for %q", gotPath)
	}
	for _, id := range oids {
		if !hasObject(dir, id) {
			t.Fatalf("%s is not in the repository", id)
		}
	}
}

func TestPrefetchViaArchiveSaysHowManyAreNotInTheArchive(t *testing.T) {
	withArchiveServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(archiveOf(t, map[string]string{"a.txt": "alpha"}))
	})
	dir := bareRepo(t, "https://github.com/o/n.git")
	got, missing := gitBlobID([]byte("alpha")), gitBlobID([]byte("a submodule's file"))
	err := prefetchViaArchive(dir, "abc", []string{got, missing})
	if err == nil || !strings.Contains(err.Error(), "1 of 2 blob(s) are not in the commit's archive") {
		t.Fatalf("%v", err)
	}
	if !hasObject(dir, got) {
		t.Fatal("the blob the archive had was not written")
	}
}

func TestPrefetchViaArchiveReportsAnArchiveThatIsNotThere(t *testing.T) {
	withArchiveServer(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	err := prefetchViaArchive(bareRepo(t, "https://github.com/o/n.git"), "abc", []string{gitBlobID([]byte("x"))})
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("%v", err)
	}
}

// the archive holds START-HERE.bat with CRLF (the repository's eol attribute), the blob holds it with LF
func TestImportArchiveBlobsFindsAFileTheArchiveWroteWithCRLF(t *testing.T) {
	dir := bareRepo(t, "")
	lf := "echo off\nstart chat.py\n"
	want := map[string]bool{gitBlobID([]byte(lf)): true}
	n, err := importArchiveBlobs(dir, bytes.NewReader(archiveOf(t, map[string]string{"START-HERE.bat": strings.ReplaceAll(lf, "\n", "\r\n")})), want)
	if err != nil || n != 1 || len(want) != 0 {
		t.Fatalf("n=%d err=%v want left %v", n, err, want)
	}
	out, err := exec.Command("git", "--git-dir", dir, "cat-file", "blob", gitBlobID([]byte(lf))).Output()
	if err != nil || string(out) != lf {
		t.Fatalf("the blob holds %q (%v), want %q", out, err, lf)
	}
}

// a file whose CRLF form is not what is wanted is left alone
func TestImportArchiveBlobsDoesNotGuessAWrongFile(t *testing.T) {
	dir := bareRepo(t, "")
	want := map[string]bool{gitBlobID([]byte("something else\n")): true}
	n, _ := importArchiveBlobs(dir, bytes.NewReader(archiveOf(t, map[string]string{"x.bat": "a\r\nb\r\n"})), want)
	if n != 0 || len(want) != 1 {
		t.Fatalf("n=%d want left %v", n, want)
	}
}
