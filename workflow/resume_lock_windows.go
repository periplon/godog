//go:build windows
// +build windows

package workflow

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

const windowsSharingViolation syscall.Errno = 32

func acquireExecutionLock(path string) (*executionLock, error) {
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("workflow: encode execution lock path: %w", err)
	}
	attributes := syscall.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(syscall.SecurityAttributes{})),
		InheritHandle: 1,
	}
	handle, err := syscall.CreateFile(pathPointer, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, &attributes, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, windowsSharingViolation) {
			return nil, errors.New("workflow: run is locked by another execution")
		}
		return nil, fmt.Errorf("workflow: acquire execution lock: %w", err)
	}
	return &executionLock{file: os.NewFile(uintptr(handle), path), path: path}, nil
}

func (lock *executionLock) release() {
	_ = lock.file.Close()
}

func attachExecutionLockFile(cmd *exec.Cmd, file *os.File) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.AdditionalInheritedHandles = append(cmd.SysProcAttr.AdditionalInheritedHandles, syscall.Handle(file.Fd()))
}
