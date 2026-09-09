//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package workflow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProbeLogWriteFailure(t *testing.T) {
	for _, mode := range []string{"header", "expected-exit-output"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRunAttemptLogWriteFailureSubprocess$")
			cmd.Env = append(os.Environ(), "WORKFLOW_LOG_FAILURE_MODE="+mode)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("log failure probe failed: %v\n%s", err, output)
			}
		})
	}
}

func TestRunAttemptLogWriteFailureSubprocess(t *testing.T) {
	mode := os.Getenv("WORKFLOW_LOG_FAILURE_MODE")
	if mode == "" {
		t.Skip("subprocess helper")
	}

	logPath := filepath.Join(t.TempDir(), "task.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	header := "\n=== attempt 1 ===\n"
	limit := uint64(0)
	task := Task{TaskSpec: TaskSpec{Run: []string{"/usr/bin/true"}}}
	if mode == "expected-exit-output" {
		limit = uint64(len(header))
		task.Run = []string{"/bin/sh", "-c", "printf child-output; exit 7"}
		task.ExpectExit = 7
	}

	var current syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &current); err != nil {
		t.Fatal(err)
	}
	if limit > current.Max {
		t.Fatalf("requested file size limit %d exceeds maximum %d", limit, current.Max)
	}
	current.Cur = limit
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &current); err != nil {
		t.Fatal(err)
	}

	_, exitCode, err := runAttempt(context.Background(), t.TempDir(), logPath, task, "", "", 1)
	if err == nil {
		t.Fatalf("runAttempt() succeeded with RLIMIT_FSIZE=%d and exit code %d", limit, exitCode)
	}
	if !strings.Contains(err.Error(), "task log") {
		t.Fatalf("runAttempt() error = %v, want task log write failure", err)
	}
	if mode == "expected-exit-output" && exitCode != task.ExpectExit {
		t.Fatalf("runAttempt() exit code = %d, want %d; error = %v", exitCode, task.ExpectExit, err)
	}
	if attemptExitMatches(task.ExpectExit, exitCode, err) {
		t.Fatalf("I/O error was accepted as expected exit %d: %v", task.ExpectExit, err)
	}
}

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
