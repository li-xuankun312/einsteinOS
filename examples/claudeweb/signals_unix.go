//go:build !windows

package main

import (
	"os"
	"syscall"
)

// terminationSignals is what we react to by killing any isolated subprocess
// (see kernel.KillAllTrackedProcessGroups) before exiting: Ctrl-C and a normal
// "stop this process" request.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
