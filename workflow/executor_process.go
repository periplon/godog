package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func runTaskCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	configureTaskCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	select {
	case err := <-wait:
		return err
	case <-ctx.Done():
		if err := terminateTaskCommand(cmd); err != nil {
			<-wait
			return fmt.Errorf("%w (terminate process: %v)", ctx.Err(), err)
		}
		<-wait
		return ctx.Err()
	}
}

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
