package kernel

// verify_build_memo.go — a build whose inputs did not change is not run again.
//
// 11/22 the integration unit's build.sh starts with rm -rf build/ and compiles every source
// with clang in a loop, and every verification spent 6m26s in it — the ones that changed only
// pytools/e2e_infer.py, which it does not read, as much as the others; the build directory of
// the last verification put back (verify_build_keep.go) was the first thing it removed. So the
// build runs under the launch probe with EINSTEINOS_PROBE_FILES=1, which records every file its
// processes open to read or to write and every directory they list. The next verification of
// the unit whose build.sh, environment, files read and directories listed are what they were
// gets the files that build wrote put back and its record (exit, output, process trace) instead
// of a run; a pass on it is verified again from nothing before it counts (devVerifyOnly).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// buildMemo: what a build of the unit read and wrote, and what it gave.
type buildMemo struct {
	Key     string            `json:"key"`     // build.sh's content and the environment
	Inputs  map[string]string `json:"inputs"`  // workspace file -> sha256
	Dirs    map[string]string `json:"dirs"`    // workspace directory -> its entries' names, hashed
	Outputs []string          `json:"outputs"` // files it wrote that were there after it: "ws:" + relative path, or an absolute path
	Exit    int               `json:"exit"`
	Out     string            `json:"out"`
	Trace   ProcTrace         `json:"trace"`
	NoTool  NoToolchainBuild  `json:"no_toolchain"`
}

func buildMemoDir(unitID string) string { return filepath.Join(verifyKeepRoot(), unitID, "memo") }

// buildMemoKey: what a build depends on besides the files it reads.
func buildMemoKey(ws, build, env string) string {
	h := sha256.New()
	b, _ := os.ReadFile(filepath.Join(ws, filepath.FromSlash(build)))
	h.Write(b)
	h.Write([]byte("\x00" + env))
	return hex.EncodeToString(h.Sum(nil))
}

// dirListing: the names in dir, hashed.
func dirListing(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	h := sha256.Sum256([]byte(strings.Join(names, "\x00")))
	return hex.EncodeToString(h[:])
}

// inWS: p relative to ws, when it is in it.
func inWS(ws, p string) (string, bool) {
	root, _ := filepath.Abs(ws)
	if p == root {
		return ".", true
	}
	if strings.HasPrefix(p, root+"/") {
		return filepath.ToSlash(strings.TrimPrefix(p, root+"/")), true
	}
	return "", false
}

// memoFromBuild records what the build that just ran in ws read and wrote (pl) and gave.
func memoFromBuild(ws, key string, pl probeLog, exit int, out string, trace ProcTrace, nt NoToolchainBuild) buildMemo {
	m := buildMemo{Key: key, Inputs: map[string]string{}, Dirs: map[string]string{}, Exit: exit, Out: out, Trace: trace, NoTool: nt}
	wrote := map[string]bool{}
	for p := range pl.Wrote {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && !strings.HasPrefix(p, "/dev/") && !strings.HasPrefix(p, "/proc/") {
			wrote[p] = true
			if rel, ok := inWS(ws, p); ok {
				m.Outputs = append(m.Outputs, "ws:"+rel)
			} else {
				m.Outputs = append(m.Outputs, p)
			}
		}
	}
	sort.Strings(m.Outputs)
	for p := range pl.Read {
		rel, ok := inWS(ws, p)
		if !ok || wrote[p] {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			m.Inputs[rel] = fileSHA(p)
		}
	}
	for p := range pl.Listed {
		if rel, ok := inWS(ws, filepath.Clean(p)); ok {
			m.Dirs[rel] = dirListing(p)
		}
	}
	return m
}

// memoMatches: ws, as the build would see it, is what the memo's build saw.
func (m buildMemo) memoMatches(ws, key string) bool {
	if m.Key != key || len(m.Inputs) == 0 {
		return false
	}
	for rel, sha := range m.Inputs {
		if fileSHA(filepath.Join(ws, filepath.FromSlash(rel))) != sha {
			return false
		}
	}
	for rel, l := range m.Dirs {
		if dirListing(filepath.Join(ws, filepath.FromSlash(rel))) != l {
			return false
		}
	}
	return true
}

func memoCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, _ := in.Stat()
	os.MkdirAll(filepath.Dir(dst), 0o755)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// outputPath: where output o of the memo is, for a build in ws.
func outputPath(ws, o string) string {
	if rel, ok := strings.CutPrefix(o, "ws:"); ok {
		return filepath.Join(ws, filepath.FromSlash(rel))
	}
	return o
}

// storedOutput: where output o is kept.
func storedOutput(unitID, o string) string {
	if rel, ok := strings.CutPrefix(o, "ws:"); ok {
		return filepath.Join(buildMemoDir(unitID), "ws", filepath.FromSlash(rel))
	}
	return filepath.Join(buildMemoDir(unitID), "abs", o)
}

// saveBuildMemo keeps m and copies of the files the build wrote.
func saveBuildMemo(unitID, ws string, m buildMemo) error {
	dir := buildMemoDir(unitID)
	os.RemoveAll(dir)
	for _, o := range m.Outputs {
		if err := memoCopyFile(outputPath(ws, o), storedOutput(unitID, o)); err != nil {
			os.RemoveAll(dir)
			return err
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	os.MkdirAll(dir, 0o755)
	return os.WriteFile(filepath.Join(dir, "memo.json"), b, 0o644)
}

// reuseBuildMemo: when the unit's last build saw what a build in ws would, its outputs are put
// back and its record returned.
func reuseBuildMemo(unitID, ws, key string) (buildMemo, bool) {
	var m buildMemo
	b, err := os.ReadFile(filepath.Join(buildMemoDir(unitID), "memo.json"))
	if err != nil || json.Unmarshal(b, &m) != nil || !m.memoMatches(ws, key) {
		return buildMemo{}, false
	}
	for _, o := range m.Outputs {
		if err := memoCopyFile(storedOutput(unitID, o), outputPath(ws, o)); err != nil {
			return buildMemo{}, false
		}
	}
	return m, true
}
