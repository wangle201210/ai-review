//go:build !darwin && !linux

package codex

import (
	"os/exec"
	"time"
)

func prepareCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
