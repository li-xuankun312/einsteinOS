package kernel

// ref_tarball.go — a reference commit's files read from one archive of the commit, fetched in one
// request, before any blob is fetched one at a time.
//
// 11/24 08:18:57 a rebuilt machine's ref_bind stood still after naming the commit: its clone is
// blobless, and each of the commit's 211 source files was read with its own `git show`, each
// fetching its own blob from GitHub, which this machine reached neither directly (every request
// timed out) nor steadily through its proxy — a file could wait 60 s on each route. One archive of
// the commit (codeload's tar.gz) holds every file; it is fetched once, directly or through the
// proxy, and the files are read from it.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// refTarballURLFmt: %[1]s owner, %[2]s name, %[3]s commit
	refTarballURLFmt = "https://codeload.github.com/%[1]s/%[2]s/tar.gz/%[3]s"
	// refTarballDirFmt: %[1]s owner, %[2]s name, %[3]s commit
	refTarballDirFmt = "/tmp/ref_tar_%[1]s_%[2]s_%[3]s"
	refTarballMu     sync.Mutex
	refTarballFailed = map[string]time.Time{} // key -> when its archive last could not be fetched
)

const (
	refTarballTimeout = 10 * time.Minute
	// refTarballRetry: after a failed fetch, how long before the archive is asked for again
	refTarballRetry = 10 * time.Minute
)

// refTarballTree: the directory the commit's archive is unpacked in, fetched on first need ("" when
// it could not be fetched).
func refTarballTree(owner, name, sha string) string {
	dir := fmt.Sprintf(refTarballDirFmt, owner, name, sha)
	key := owner + "/" + name + "@" + sha
	refTarballMu.Lock()
	defer refTarballMu.Unlock()
	if st, err := os.Stat(filepath.Join(dir, ".complete")); err == nil && !st.IsDir() {
		return dir
	}
	if t, ok := refTarballFailed[key]; ok && time.Since(t) < refTarballRetry {
		return ""
	}
	start := time.Now()
	var errs []string
	for _, proxy := range refFetchRoutes() {
		n, err := fetchTarball(fmt.Sprintf(refTarballURLFmt, owner, name, sha), proxy, dir)
		if err == nil {
			log.Printf("ref_tarball: %s/%s %s: %d file(s) from one archive %s in %s", owner, name, short_sha(sha), n, refRouteName(proxy), time.Since(start).Round(time.Second))
			delete(refTarballFailed, key)
			return dir
		}
		errs = append(errs, refRouteName(proxy)+": "+err.Error())
	}
	refTarballFailed[key] = time.Now()
	log.Printf("ref_tarball: %s/%s %s: the archive could not be fetched (%s) — its files are read blob by blob", owner, name, short_sha(sha), strings.Join(errs, "; "))
	return ""
}

// fetchTarball downloads one tar.gz and unpacks it into dir, its top directory left out.
func fetchTarball(u, proxy, dir string) (int, error) {
	tr := &http.Transport{Proxy: nil}
	if proxy != "" {
		pu, err := url.Parse(proxy)
		if err != nil {
			return 0, err
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	ctx, cancel := context.WithTimeout(context.Background(), refTarballTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	if tok := GetGHToken(); tok != "" {
		req.Header.Set("Authorization", "token "+tok)
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// the archive is kept beside its unpacked files: the blobless clone's blobs are taken from
	// it too (ref_archive.go), not fetched in a second download of the same archive
	part := dir + ".tar.gz.part"
	f, err := os.Create(part)
	if err != nil {
		return unpackTarGz(resp.Body, dir)
	}
	n, err := unpackTarGz(io.TeeReader(resp.Body, f), dir)
	io.Copy(f, resp.Body) // what the unpacking did not read
	f.Close()
	if err == nil {
		os.Rename(part, dir+".tar.gz")
	} else {
		os.Remove(part)
	}
	return n, err
}

// refTarballArchive: the commit's archive as fetched for its files, when it was ("" when not).
func refTarballArchive(owner, name, sha string) string {
	p := fmt.Sprintf(refTarballDirFmt, owner, name, sha) + ".tar.gz"
	if st, err := os.Stat(p); err == nil && st.Size() > 0 {
		return p
	}
	return ""
}

// unpackTarGz writes a tar.gz's regular files under dir, the archive's top directory left out,
// and marks dir complete; a partial unpack leaves no mark.
func unpackTarGz(r io.Reader, dir string) (int, error) {
	os.RemoveAll(dir)
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, err
	}
	t := tar.NewReader(gz)
	n := 0
	for {
		h, err := t.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		_, rel, ok := strings.Cut(h.Name, "/")
		if !ok || rel == "" || strings.Contains(rel, "..") {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return n, err
		}
		f, err := os.Create(p)
		if err != nil {
			return n, err
		}
		if _, err := io.Copy(f, t); err != nil {
			f.Close()
			return n, err
		}
		f.Close()
		n++
	}
	return n, os.WriteFile(filepath.Join(dir, ".complete"), nil, 0o644)
}

// refTarballFile: a file of the commit from its archive (false when the archive is not there or
// the commit has no such file).
func refTarballFile(owner, name, sha, path string) (string, bool) {
	dir := refTarballTree(owner, name, sha)
	if dir == "" {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		return "", false
	}
	return string(b), true
}
