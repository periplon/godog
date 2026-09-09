package workflow

import (
	"context"
	"os"
	"os/exec"
)

type executionLock struct {
	file *os.File
	path string
}

type executionLockContextKey struct{}

func contextWithExecutionLock(ctx context.Context, lock *executionLock) context.Context {
	return context.WithValue(ctx, executionLockContextKey{}, lock)
}

func attachExecutionLock(ctx context.Context, cmd *exec.Cmd) {
	lock, _ := ctx.Value(executionLockContextKey{}).(*executionLock)
	if lock != nil {
		attachExecutionLockFile(cmd, lock.file)
	}
}
