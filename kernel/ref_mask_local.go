package kernel

import (
	"fmt"
	"sort"
	"strings"
)

// A dev conversation is not told which repository is the reference: its prompt is masked
// (maskShownPrompt) and so is what it is given of the reference's requirements. On the machine
// that runs its commands the reference sits in plain sight: the kernel's bare clone
// /tmp/ref_<owner>_<name>.git, which a "ls /tmp" names and a "git -C … show" reads. 11/18
// dev_fix_1 of uebd9b81357 (54005caf) wrote the reference's own names into its port map
// (16:06:09 "include/<ref>/artifact/dequant.hpp", "<REF>_GGUF_MAIN_DISABLED"), wrote 16:29:01
// "The reference files must be from the REF repo (<owner>/<name>)", and searched this machine for
// the reference's dequant.hpp (16:27:01).

// RoleSeesMasked: a conversation whose prompt the kernel masks — the dev conversations, the
// conversations they start, and the review — is not shown the reference on this machine either.
func RoleSeesMasked(role string) bool {
	for _, p := range []string{"dev_unit_", "dev_fix", "dev_legacy", "spawn_work", "review_conv"} {
		if strings.HasPrefix(role, p) {
			return true
		}
	}
	return false
}

// referenceRepos: the references bound now, as owner/name.
func referenceRepos() []string {
	prMu.Lock()
	defer prMu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, pr := range prTable {
		for _, r := range pr.RefRepos {
			k := r.Owner + "/" + r.Name
			if r.Owner != "" && r.Name != "" && !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ReferenceClonePlaces: where the kernel keeps each bound reference on this machine.
func ReferenceClonePlaces() []string {
	var out []string
	for _, r := range referenceRepos() {
		owner, name, _ := strings.Cut(r, "/")
		out = append(out, fmt.Sprintf("/tmp/ref_%s_%s.git", owner, name))
	}
	return out
}

// TouchesReferenceClone: the reference clone a tool call names, or "".
func TouchesReferenceClone(input string) string {
	for _, p := range ReferenceClonePlaces() {
		if strings.Contains(input, p) {
			return p
		}
	}
	return ""
}
