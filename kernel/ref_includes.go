package kernel

import (
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// refIncludeRe: an #include of a file by name ("…"), not of a system header (<…>).
var refIncludeRe = regexp.MustCompile(`(?m)^[ \t]*#[ \t]*include[ \t]*"([^"]+)"`)

// refIncludedFiles: the files of the reference tree that file's text includes — each
// #include "name" resolved as the compiler would find it here: beside file, or at a tree path
// that ends in /name (an include directory, include/ + strata/platform/direct_file.hpp).
func refIncludedFiles(file, text string, tree []string) []string {
	in := map[string]bool{}
	for _, f := range tree {
		in[f] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range refIncludeRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		var found []string
		if p := path.Clean(path.Join(path.Dir(file), name)); in[p] {
			found = append(found, p)
		} else {
			for _, f := range tree {
				if f == name || strings.HasSuffix(f, "/"+name) {
					found = append(found, f)
				}
			}
		}
		for _, f := range found {
			if f != file && !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}

var (
	refTreeMu    sync.Mutex
	refTreeCache = map[string][]string{}
)

// refTreeFiles: every file of the reference at sha, read once per bare clone and commit.
func refTreeFiles(bare, sha string) []string {
	key := bare + "@" + sha
	refTreeMu.Lock()
	defer refTreeMu.Unlock()
	if t, ok := refTreeCache[key]; ok {
		return t
	}
	ls, err := refGitFn(bare, "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return nil
	}
	t := strings.Fields(ls)
	refTreeCache[key] = t
	return t
}
