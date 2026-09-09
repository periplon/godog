package workflow

import (
	"context"
	"fmt"
	"os/exec"
)

func runTaskCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	attachExecutionLock(ctx, cmd)
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
