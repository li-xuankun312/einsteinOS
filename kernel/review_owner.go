package kernel

// review_owner.go — which unit the files a review named belong to.
//
// A reviewer reads the whole branch, so what it finds is not always the unit under review's
// to answer for. 11/08 unit pf7b804cf95 (three prefill headers) was failed three times for
// src/prefill/kernels.cu and src/kernels/gpu/gr_core.cu — the ported files of units
// u41e2bf114f and u4decb4e657, which had already passed — and then had no modules left to
// split, so the run stopped. The kernel measures the owner instead of assuming it is the unit
// that happens to be under review: a path in the verdict is matched against the port maps,
// which say which reference source each written file came from, and against each unit's own
// measured sources.

import (
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
)

// reviewPathRe: a repository-relative path with a code extension, as a verdict names it.
var reviewPathRe = regexp.MustCompile(`(?:^|[\s(\[])([A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+\.(?:cu|cuh|cpp|cc|c|hpp|hh|h|inc|py))`)

// reviewPaths: the distinct files a verdict names, in the order it names them.
func reviewPaths(verdict string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range reviewPathRe.FindAllStringSubmatch(verdict, -1) {
		if p := m[1]; !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// portMapOwners reads the port maps of branch in the dev repository's clone: written file ->
// the units whose map claims it. They are read from the branch itself (git show), not from
// the clone's working tree, which between units is checked out on whatever the last git step
// left there — 11/08 the attribution found no port map at all and faulted nobody.
func portMapOwners(dir, branch string) map[string][]string {
	owners := map[string][]string{}
	mapDir := path.Dir(PortMapPath("x"))
	out, err := exec.Command("git", "-C", dir, "ls-tree", "-r", "--name-only", branch, "--", mapDir).Output()
	if err != nil {
		return owners
	}
	for _, f := range strings.Split(string(out), "\n") {
		f = strings.TrimSpace(f)
		if !strings.HasSuffix(f, ".tsv") {
			continue
		}
		unit := strings.TrimSuffix(path.Base(f), ".tsv")
		b, err := exec.Command("git", "-C", dir, "show", branch+":"+f).Output()
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(string(b), "\n") {
			fs := strings.Split(line, "\t")
			if len(fs) < 3 || fs[2] == "" || seen[fs[2]] {
				continue
			}
			seen[fs[2]] = true
			owners[fs[2]] = append(owners[fs[2]], unit)
		}
	}
	return owners
}

// reviewOwners: for each file the verdict names, the units that own it — by the port maps
// (the file they wrote) or, for a path of the reference, by a unit's measured sources.
// A file no unit claims has no owner.
func reviewOwners(verdict string, written map[string][]string, sources map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, p := range reviewPaths(verdict) {
		var units []string
		units = append(units, written[p]...)
		for unit, srcs := range sources {
			for _, s := range srcs {
				if s == p {
					units = append(units, unit)
				}
			}
		}
		sort.Strings(units)
		units = dedupStrings(units)
		if len(units) > 0 {
			out[p] = units
		}
	}
	return out
}

func dedupStrings(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || in[i-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// reviewBlames: the units other than the one under review that own files the verdict named.
func reviewBlames(unitID string, owners map[string][]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, units := range owners {
		for _, u := range units {
			if u != unitID && !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	sort.Strings(out)
	return out
}
