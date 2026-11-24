package kernel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func tarGz(t *testing.T, files map[string]string) []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for p, s := range files {
		tw.WriteHeader(&tar.Header{Name: "Strata-abc/" + p, Mode: 0o644, Size: int64(len(s)), Typeflag: tar.TypeReg})
		tw.Write([]byte(s))
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

// 11/24 08:18:57 a rebuilt machine's ref_bind stood still reading the commit's 211 files one blob
// at a time: the files are read from one archive of the commit, fetched once
func TestTheReferencesFilesAreReadFromOneArchive(t *testing.T) {
	var hits int32
	body := tarGz(t, map[string]string{"src/core/layer.cpp": "int f() { return 1; }\n", "README.md": "x"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write(body)
	}))
	defer srv.Close()
	oldU, oldD := refTarballURLFmt, refTarballDirFmt
	refTarballURLFmt = srv.URL + "/%[1]s/%[2]s/tar.gz/%[3]s"
	refTarballDirFmt = filepath.Join(t.TempDir(), "ref_tar_%[1]s_%[2]s_%[3]s")
	defer func() { refTarballURLFmt, refTarballDirFmt = oldU, oldD }()
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	sha := "01529aaa82b9000000000000000000000000abcd"
	refMu.Lock()
	refRepos["Niko1221/Strata"] = &RefRepoState{Owner: "Niko1221", Name: "Strata", CloneDir: "/nonexistent.git"}
	refMu.Unlock()
	defer func() { refMu.Lock(); delete(refRepos, "Niko1221/Strata"); refMu.Unlock() }()
	for i := 0; i < 3; i++ {
		got, err := RefFileAt("Niko1221", "Strata", sha, "src/core/layer.cpp")
		if err != nil || got != "int f() { return 1; }\n" {
			t.Fatalf("%q %v", got, err)
		}
	}
	if missing := refUnreadable("Niko1221", "Strata", sha, []string{"src/core/layer.cpp"}, 1); len(missing) != 0 {
		t.Fatalf("%v", missing)
	}
	if hits != 1 {
		t.Fatalf("%d archive request(s) for one commit", hits)
	}
}
