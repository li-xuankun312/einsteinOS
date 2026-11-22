package kernel

// verify_build_keep.go — a unit's verification builds on the build directory its last one left.
//
// 11/22 14:33-14:40 and 14:55-15:02 the integration unit's verifications changed one python
// file (pytools/e2e_infer.py) and build.sh compiled the whole C++/CUDA project again in a new
// directory, 6.5 minutes of each 11. The workspace is still assembled fresh from the branch and
// the delta (assembleWorkspace), at a path of the unit's own, so the build directory of the last
// verification (its CMake cache holds that path) is put back into it, and every file whose
// content is what it was then gets back the time it had then: make rebuilds what changed. A
// verification that passes on such a build is verified again from nothing before it counts
// (devVerifyOnly): an executable the last build left, which this one no longer makes, does not
// pass a test. EINSTEINOS_FRESH_BUILD=1 assembles every workspace fresh, as before.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// verifyBuildDir: the build directory build.sh writes (EINSTEINOS_BUILD_DIR's default)
const verifyBuildDir = "build"

// keptFile: a file of the tree a verification built from.
type keptFile struct {
	SHA   string    `json:"sha"`
	MTime time.Time `json:"mtime"`
}

// verifyKeepRoot: where a unit's last build directory and file times are kept (a variable for
// tests).
var verifyKeepRoot = func() string { return filepath.Join(os.TempDir(), "einsteinos_verify_keep") }

// freshBuild: every workspace is assembled fresh (EINSTEINOS_FRESH_BUILD=1).
func freshBuild() bool { return os.Getenv("EINSTEINOS_FRESH_BUILD") != "" }

func fileSHA(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// treeFiles: the regular files of ws outside its build directory, by slash path.
func treeFiles(ws string) map[string]string {
	out := map[string]string{}
	filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == verifyBuildDir {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			out[rel] = p
		}
		return nil
	})
	return out
}

// assembleWorkspaceKept is assembleWorkspace at the unit's own path with its last build put back
// (see the file's comment); seeded says whether a build directory was. done keeps the build
// directory and the files' times for the next verification and removes the workspace.
func assembleWorkspaceKept(unitID, tipDir, mirrorDir string, changed, removed []string) (ws string, seeded bool, done func(), err error) {
	if freshBuild() || unitID == "" {
		ws, err = assembleWorkspace(tipDir, mirrorDir, changed, removed)
		return ws, false, func() { os.RemoveAll(ws) }, err
	}
	keep := filepath.Join(verifyKeepRoot(), unitID)
	ws = filepath.Join(os.TempDir(), "einsteinos_verify_"+unitID)
	os.RemoveAll(ws)
	if err = os.MkdirAll(ws, 0o755); err != nil {
		return "", false, func() {}, err
	}
	if err = copyTree(tipDir, ws); err == nil {
		err = applyDelta(mirrorDir, ws, changed, removed)
	}
	if err != nil {
		os.RemoveAll(ws)
		return "", false, func() {}, err
	}
	var last map[string]keptFile
	if b, rerr := os.ReadFile(filepath.Join(keep, "files.json")); rerr == nil {
		json.Unmarshal(b, &last)
	}
	if _, serr := os.Stat(filepath.Join(keep, verifyBuildDir)); serr == nil && last != nil {
		if os.Rename(filepath.Join(keep, verifyBuildDir), filepath.Join(ws, verifyBuildDir)) == nil {
			seeded = true
			for rel, p := range treeFiles(ws) {
				if k, ok := last[rel]; ok && k.SHA == fileSHA(p) {
					os.Chtimes(p, k.MTime, k.MTime)
				}
			}
		}
	}
	done = func() {
		files := map[string]keptFile{}
		for rel, p := range treeFiles(ws) {
			if st, serr := os.Stat(p); serr == nil {
				files[rel] = keptFile{SHA: fileSHA(p), MTime: st.ModTime()}
			}
		}
		os.RemoveAll(keep)
		if os.MkdirAll(keep, 0o755) == nil {
			if _, serr := os.Stat(filepath.Join(ws, verifyBuildDir)); serr == nil && os.Rename(filepath.Join(ws, verifyBuildDir), filepath.Join(keep, verifyBuildDir)) == nil {
				b, _ := json.Marshal(files)
				os.WriteFile(filepath.Join(keep, "files.json"), b, 0o644)
			}
		}
		os.RemoveAll(ws)
	}
	return ws, seeded, done, nil
}
