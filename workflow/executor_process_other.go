//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)
// +build !aix,!darwin,!dragonfly,!freebsd,!linux,!netbsd,!openbsd,!solaris

package workflow

import "os/exec"

func configureTaskCommand(_ *exec.Cmd) {}

func terminateTaskCommand(cmd *exec.Cmd) error {
	return killSingleProcess(cmd)
}
