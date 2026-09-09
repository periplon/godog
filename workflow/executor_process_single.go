//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)
// +build !aix,!darwin,!dragonfly,!freebsd,!linux,!netbsd,!openbsd,!solaris

package workflow

import (
	"errors"
	"os"
	"os/exec"
)

func killSingleProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
