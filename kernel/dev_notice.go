package kernel

// dev_notice.go — what a dev conversation is told when it reaches for git.
//
// A dev conversation's own sandbox cannot clone the dev repository (private), and the
// kernel — not the conversation — verifies and pushes. Yet the 2026/11/02 dev units
// that wrote nothing (unit 2 twice, unit 3 at 15:23) each spent their first rounds on
// git commands: every one of those rounds had exactly one tool, followed by
// "[shadow] skip git (remote has full output)". The shadow lets the sandbox's own result
// stand for git, so the conversation read its sandbox's failure and nothing corrected
// it. For a dev role the answer to a git command is now the truth, from this machine.

import (
	"fmt"
	"strings"
)

// IsDevRole: conversations that write a unit's code (unit and fix attempts).
func IsDevRole(role string) bool {
	return strings.HasPrefix(role, "dev_unit") || strings.HasPrefix(role, "dev_fix")
}

// ConvRoleOf returns the role of the conversation with this id: from the process that
// runs it (known from the moment it starts), else from the registry.
func ConvRoleOf(convID string) string {
	if pid := FindPidByConvID(convID); pid > 0 {
		if m := getMeta(pid); m != nil && m.Role != "" {
			return m.Role
		}
	}
	convRegistryMu.Lock()
	defer convRegistryMu.Unlock()
	if rec, ok := mergedConvLocked(convID); ok && rec.Role != "child" {
		return rec.Role
	}
	return ""
}

// DevGitNotice is the local result of a git command issued by a dev conversation.
func DevGitNotice() string {
	dirs := "the directory under [DEV REPO]"
	if drs := GetDevRepos(); len(drs) > 0 {
		var ds []string
		for _, d := range drs {
			ds = append(ds, devMirrorDir(d))
		}
		dirs = strings.Join(ds, ", ")
	}
	return fmt.Sprintf("[kernel] git is not used in this phase. the repository is already on this machine at %s "+
		"(this machine runs your commands; ls it, and read .einsteinOS/theory.md there). do not clone, fetch, pull or push: "+
		"your own sandbox cannot reach this private repository, and the kernel pushes your files after it has built and tested them. "+
		"write the files and the two verify scripts.", dirs)
}
