//go:build !windows

package kernel

import "syscall"

// killPgid sends SIGKILL to every process in the group led by pgid. The leading
// "-" on the pid is what makes a POSIX kill() target the whole group instead of
// just that one process.
func killPgid(pgid int) {
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
