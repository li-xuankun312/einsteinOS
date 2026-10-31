//go:build !windows

package claudeweb

import (
	"os/exec"
	"syscall"
	"time"
)

// hardKillOnCancel makes a context deadline actually end the command.
//
// exec.CommandContext only kills the direct child (bash). A curl started by
// that bash survives, keeps the stdout pipe open, and cmd.Output() blocks until
// curl itself gives up — which is why a "15s" guard logged "took 2m7s",
// "took 10m7s" in the Strata run. Putting the command in its own process group
// and killing the whole group fixes the root cause; WaitDelay is the backstop
// if anything still holds the pipe.
func hardKillOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
}
