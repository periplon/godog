//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)
// +build !darwin,!dragonfly,!freebsd,!linux,!netbsd,!openbsd,!windows

package workflow

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func acquireExecutionLock(path string) (*executionLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errors.New("workflow: run is locked; stale locks require explicit removal on this platform")
		}
		return nil, fmt.Errorf("workflow: acquire execution lock: %w", err)
	}
	return &executionLock{file: file, path: path}, nil
}

func (lock *executionLock) release() {
	_ = lock.file.Close()
	_ = os.Remove(lock.path)
}

func attachExecutionLockFile(_ *exec.Cmd, _ *os.File) {}
