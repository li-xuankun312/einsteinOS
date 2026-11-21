package kernel

// machine_changes.go — what a verification script changed on this machine outside its
// workspace.
//
// 11/21 03:01 the integration's e2e.sh (cbb5dc6), which the kernel runs on this machine, ran
// sed -i on corex's vllm model_executor/models/registry.py (both paths), rewrote corex's
// transformers models/auto/configuration_auto.py, and ran rm -rf on
// /root/.local/lib/python3.10/site-packages/transformers*; 11/20 20:44 a pip install --user
// put tokenizers 0.23.3 and nine more packages into /root/.local, and 11/21 06:54 transformers
// 5.20.2 replaced 4.51.3 in /usr/local/lib/python3.10/site-packages. The places a Python
// environment keeps its packages are walked after the script for files and directories
// changed since it started; a compiled-bytecode cache (__pycache__, .pyc), which an import
// writes, is not a change.

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// machineClockSlack: how far before a run's start a change still counts as the run's.
const machineClockSlack = time.Second

// machinePackageDirs: where this machine keeps Python packages, resolved and without
// repeats (/usr/local/corex is a link to /usr/local/corex-3.2.3).
var machinePackageDirs = func() []string {
	home, _ := os.UserHomeDir()
	var pats = []string{
		"/usr/local/lib/python3*/site-packages", "/usr/local/lib/python3*/dist-packages",
		"/usr/lib/python3*/dist-packages", "/usr/lib/python3/dist-packages",
		"/usr/local/corex*/lib64/python3/dist-packages",
	}
	if home != "" {
		pats = append(pats, filepath.Join(home, ".local/lib/python3*/site-packages"), filepath.Join(home, ".local/bin"))
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range pats {
		ms, _ := filepath.Glob(p)
		for _, m := range ms {
			r, err := filepath.EvalSymlinks(m)
			if err != nil || seen[r] {
				continue
			}
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

// machineChangedSince: the files and directories under the package directories whose
// modification time is after start (a directory's changes when an entry is added or removed).
func machineChangedSince(start time.Time) []string {
	// a file's time comes from the system's coarse clock and can read a few ms before a
	// time.Now() taken earlier: compared from a second before
	start = start.Add(-machineClockSlack)
	var out []string
	for _, root := range machinePackageDirs() {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if d.IsDir() && name == "__pycache__" {
				return filepath.SkipDir
			}
			if strings.HasSuffix(name, ".pyc") {
				return nil
			}
			info, err := d.Info()
			if err != nil || !info.ModTime().After(start) {
				return nil
			}
			// a directory changed only by an import making its __pycache__ is not a change
			if d.IsDir() {
				if pc, err := os.Stat(filepath.Join(path, "__pycache__")); err == nil && pc.ModTime().After(start) && !changedEntryIn(path, start) {
					return nil
				}
			}
			out = append(out, path)
			return nil
		})
	}
	return out
}

// changedEntryIn: a direct entry of dir other than __pycache__ and .pyc changed after start.
func changedEntryIn(dir string, start time.Time) bool {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() == "__pycache__" || strings.HasSuffix(e.Name(), ".pyc") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(start) {
			return true
		}
	}
	return false
}

// shownPaths: at most n paths, then how many more.
func shownPaths(ps []string, n int) string {
	if len(ps) <= n {
		return strings.Join(ps, " ")
	}
	return strings.Join(ps[:n], " ") + " … " + strconv.Itoa(len(ps)-n) + " more"
}
