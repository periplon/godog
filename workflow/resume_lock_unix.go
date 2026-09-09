//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd
// +build darwin dragonfly freebsd linux netbsd openbsd

package workflow

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func acquireExecutionLock(path string) (*executionLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("workflow: open execution lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.New("workflow: run is locked by another execution")
		}
		return nil, fmt.Errorf("workflow: acquire execution lock: %w", err)
	}
	return &executionLock{file: file, path: path}, nil
}

func (lock *executionLock) release() {
	// Do not explicitly unlock: children inherit this open file description and
	// keep the advisory lock until their process tree exits after a runner crash.
	_ = lock.file.Close()
}

func attachExecutionLockFile(cmd *exec.Cmd, file *os.File) {
	cmd.ExtraFiles = append(cmd.ExtraFiles, file)
}
