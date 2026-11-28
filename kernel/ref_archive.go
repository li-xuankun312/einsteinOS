package kernel

// ref_archive.go — the blobs a blobless reference clone lacks, taken from the commit's archive.
//
// 11/16 on a fresh machine and 11/19 14:51 (and again on cc-1c97a71e) prefetchCommitBlobs'
// one request for the 219 blobs of the commit ended with
//
//	fatal: bad revision '11c0baef…'
//	error: https://github.com/Niko1221/Strata.git did not send all necessary objects
//
// on both routes, and every file was then fetched one at a time (fileSurvey took the 40
// minutes of 14:51–15:33). Reproduced against GitHub: object ids given to `git fetch` as
// arguments get nothing back, while the same ids on stdin (what git's own lazy fetch does)
// get all of them in 0.3 s — but --stdin needs git 2.29 and Ubuntu 20.04 has 2.25. A commit's
// source archive is one GET (1 MB for the 219 files) that needs no particular git: each file
// in it is hashed as a git blob and the ones the clone lacks are written into it.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// refArchiveURL is where a commit's source archive is; a variable for tests.
var refArchiveURL = func(owner, name, sha string) string {
	return fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/%s", owner, name, sha)
}

const (
	refArchiveTimeout  = 2 * time.Minute
	refArchiveMaxBytes = 256 << 20 // a larger archive is not taken: the files go one at a time
)

var refOriginRe = regexp.MustCompile(`github\.com[/:]([^/\s]+)/([^/\s]+?)(?:\.git)?/?$`)

// refOriginRepo is the owner and name of the GitHub repository the clone's origin is.
func refOriginRepo(cloneDir string) (owner, name string, ok bool) {
	out, err := exec.Command("git", "--git-dir", cloneDir, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return "", "", false
	}
	m := refOriginRe.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// gitBlobID is the id git gives content as a blob.
func gitBlobID(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// importArchiveBlobs reads a gzipped tar and writes into the repository at cloneDir the
// blobs of its files whose ids are in want, removing them from want as it does. Returns how
// many it wrote.
func importArchiveBlobs(cloneDir string, archive io.Reader, want map[string]bool) (int, error) {
	zr, err := gzip.NewReader(archive)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	n := 0
	for len(want) > 0 {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, 64<<20))
		if err != nil {
			return n, err
		}
		id := gitBlobID(data)
		if !want[id] && bytes.Contains(data, []byte("\r\n")) {
			// git archive writes a file as the repository's attributes say (a .bat with
			// eol=crlf comes out with CRLF), and the blob holds it with LF: 11/19 the one file
			// of the 219 that was not taken (START-HERE.bat). What is written is checked
			// against the id wanted below, so a wrong guess writes nothing.
			if lf := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")); want[gitBlobID(lf)] {
				data, id = lf, gitBlobID(lf)
			}
		}
		if !want[id] {
			continue
		}
		c := exec.Command("git", "--git-dir", cloneDir, "hash-object", "-w", "-t", "blob", "--stdin")
		c.Stdin = strings.NewReader(string(data))
		out, err := c.Output()
		if err != nil {
			return n, fmt.Errorf("git hash-object: %v", err)
		}
		if got := strings.TrimSpace(string(out)); got != id {
			return n, fmt.Errorf("git wrote blob %s for the content of %s, expected %s", got, hdr.Name, id)
		}
		delete(want, id)
		n++
	}
	return n, nil
}

// prefetchViaArchive takes the blobs oids of commit sha, which the clone lacks, from the
// commit's source archive: directly first, then through the machine's proxy (ref_proxy.go).
// An error says how many were not got — the files that are not in the archive (a submodule,
// a file too large) — and those go one at a time as before.
func prefetchViaArchive(cloneDir, sha string, oids []string) error {
	owner, name, ok := refOriginRepo(cloneDir)
	if !ok {
		return fmt.Errorf("the clone's origin is not a GitHub repository")
	}
	want := make(map[string]bool, len(oids))
	for _, o := range oids {
		want[o] = true
	}
	// the archive ref_tarball.go fetched for the commit's files serves first: 11/24 14:15:06 it
	// had come in 1m14s and the same archive was then downloaded again for the blobs
	if p := refTarballArchive(owner, name, sha); p != "" {
		if f, err := os.Open(p); err == nil {
			_, ierr := importArchiveBlobs(cloneDir, f, want)
			f.Close()
			if ierr == nil && len(want) == 0 {
				return nil
			}
		}
	}
	var lastErr error
	for _, proxy := range refFetchRoutes() {
		tr := &http.Transport{}
		if proxy != "" {
			if u, err := url.Parse(proxy); err == nil {
				tr.Proxy = http.ProxyURL(u)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), refArchiveTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, refArchiveURL(owner, name, sha), nil)
		if err != nil {
			cancel()
			return err
		}
		resp, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("%s: %v", refRouteName(proxy), err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			cancel()
			lastErr = fmt.Errorf("%s: archive of %s/%s %s: HTTP %d", refRouteName(proxy), owner, name, short_sha(sha), resp.StatusCode)
			continue
		}
		_, ierr := importArchiveBlobs(cloneDir, io.LimitReader(resp.Body, refArchiveMaxBytes), want)
		resp.Body.Close()
		cancel()
		if ierr != nil && len(want) > 0 {
			lastErr = fmt.Errorf("%s: %v", refRouteName(proxy), ierr)
			continue
		}
		if len(want) > 0 {
			return fmt.Errorf("%d of %d blob(s) are not in the commit's archive", len(want), len(oids))
		}
		return nil
	}
	return lastErr
}
