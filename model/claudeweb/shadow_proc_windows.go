//go:build windows

package claudeweb

import (
	"os/exec"
	"time"
)

// hardKillOnCancel: no process groups here; WaitDelay at least bounds Output().
func hardKillOnCancel(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
}
