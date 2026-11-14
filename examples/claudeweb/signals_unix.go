//go:build !windows

package main

import (
	"os"
	"syscall"
)

// terminationSignals is what we react to by killing any isolated subprocess
// (see kernel.KillAllTrackedProcessGroups) and storing any plan held only in memory
// before exiting: Ctrl-C, a normal "stop this process" request, and the hangup of the
// terminal the run was started from — 11/09 16:34 the SSH session to cc-9cdb0db1 dropped
// ("client_loop: send disconnect"), and SIGHUP's default action ended the run with its
// verification subprocesses left running and nothing stored.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
