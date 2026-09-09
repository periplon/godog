//go:build windows
// +build windows

package workflow

import (
	"fmt"
	"os/exec"
	"strconv"
)

func configureTaskCommand(_ *exec.Cmd) {}

func terminateTaskCommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if output, err := newTaskkillCommand(cmd.Process.Pid).CombinedOutput(); err != nil {
		if killErr := killSingleProcess(cmd); killErr != nil {
			return fmt.Errorf("taskkill process tree: %w: %s (fallback kill: %v)", err, output, killErr)
		}
		return fmt.Errorf("taskkill process tree: %w: %s", err, output)
	}
	return nil
}

func newTaskkillCommand(pid int) *exec.Cmd {
	return exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(pid))
}
