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
	"regexp"
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

// machinePackagesNamedRe: a line that names where a Python environment keeps its packages.
var machinePackagesNamedRe = regexp.MustCompile(`(site|dist)-packages|getsitepackages|getusersitepackages|get_paths\(\)\[["']purelib`)

// scriptFileRe: a file a script runs or reads, by its path in the workspace.
var scriptFileRe = regexp.MustCompile(`[A-Za-z0-9_./${}"-]*[A-Za-z0-9_-]+\.(py|sh|pl|rb)\b`)

// scriptsNamingMachinePackages: the lines, outside comments, of the unit's build.sh, test.sh and
// e2e.sh and of the workspace's files they name that name this machine's Python package
// directories ("path:line: text") — read before any of them runs. 11/21 03:01 and 11/22 23:47
// the integration's scripts rewrote corex's vllm registry and transformers' import_utils.py,
// and the change was measured after the fact; 11/23 the e2e.sh on the wip branch still ran
// pytools/patch_import_utils.py, whose SEARCH_BASES lists /usr/local/lib/python3.10/site-packages.
func scriptsNamingMachinePackages(ws, unitID string) []string {
	build, test := devVerifyScripts(unitID)
	queue := []string{build, test, devE2EScript(unitID)}
	seen := map[string]bool{}
	var out []string
	for len(queue) > 0 {
		rel := filepath.ToSlash(queue[0])
		queue = queue[1:]
		if seen[rel] {
			continue
		}
		seen[rel] = true
		raw, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		for i, l := range strings.Split(string(raw), "\n") {
			t := strings.TrimSpace(l)
			if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
				continue
			}
			if machinePackagesNamedRe.MatchString(t) {
				out = append(out, rel+":"+strconv.Itoa(i+1)+": "+truncStr(t, 160))
			}
			for _, m := range scriptFileRe.FindAllString(t, -1) {
				p := strings.Trim(m, `"'`)
				for _, pre := range []string{"$ROOT/", "${ROOT}/", "$HERE/", "./"} {
					p = strings.TrimPrefix(p, pre)
				}
				if strings.ContainsAny(p, "${}\"") || strings.HasPrefix(p, "/") {
					continue
				}
				if st, err := os.Stat(filepath.Join(ws, filepath.FromSlash(p))); err == nil && st.Mode().IsRegular() {
					queue = append(queue, p)
				}
			}
		}
	}
	return out
}
