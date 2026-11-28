package claudeweb

// git_path_shim.go — a script's own git calls on this machine.
//
// The mirror a conversation's commands run against is the branch's tree without .git
// (kernel/dev_verify.go primeMirror: a .git there would be taken for the kernel's clone), and
// the kernel commits the tree itself. A shell's git is a function here (gitShim); a script's
// git is the program on PATH, and on the mirror "git add" fails — the script stops there while
// the sandbox's copy runs on. 11/17 13:57:17 dev_fix_1 (8941cbd1) ran a Python script that
// appended a port line, then called git add: "fatal: not a git repository", the script ended,
// and the mirror kept a 77th line the sandbox's run had not left — the kernel's verification
// found port_extra (16:32:59) for a line the conversation's tree did not have; 16:38:22 its
// fix stopped at git add again.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

var (
	gitPathShimOnce sync.Once
	gitPathShimDir  string
)

// gitPathShim is a directory holding a "git" program for the commands run here: add and commit
// record history only and leave the working tree as it is, so on the mirror they succeed and
// do nothing; every other git command is the real git's.
func gitPathShim() string {
	gitPathShimOnce.Do(func() {
		real, err := exec.LookPath("git")
		if err != nil {
			return
		}
		dir := filepath.Join(os.TempDir(), "einsteinos_gitshim")
		if os.MkdirAll(dir, 0o755) != nil {
			return
		}
		script := fmt.Sprintf(`#!/bin/bash
a=("$@"); i=0
while [ $i -lt ${#a[@]} ]; do case "${a[$i]}" in -C|-c) i=$((i+2));; -*) i=$((i+1));; *) break;; esac; done
case "${a[$i]}" in add|commit) exit 0;; esac
exec %q "$@"
`, real)
		if os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755) != nil {
			return
		}
		gitPathShimDir = dir
	})
	return gitPathShimDir
}

// withGitPathShim puts the git program of gitPathShim first on the command's PATH.
func withGitPathShim(command string) string {
	if d := gitPathShim(); d != "" {
		return "export PATH=" + d + ":\"$PATH\"; " + command
	}
	return command
}
