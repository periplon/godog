//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package workflow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRunTaskCommandCancelsNestedProcessTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-finished")
	ready := filepath.Join(t.TempDir(), "descendant-ready")
	t.Setenv("WORKFLOW_DESCENDANT_MARKER", marker)
	t.Setenv("WORKFLOW_DESCENDANT_READY", ready)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.Command("sh", "-c", `sh -c '(sleep 0.5; touch "$WORKFLOW_DESCENDANT_MARKER") & touch "$WORKFLOW_DESCENDANT_READY"; wait'`)
	done := make(chan error, 1)
	go func() { done <- runTaskCommand(ctx, cmd) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("nested descendant did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("runTaskCommand() error = %v, want context canceled", err)
	}

	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("nested descendant survived cancellation and created %s", marker)
	}
}
