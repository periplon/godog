//go:build windows
// +build windows

package workflow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestWindowsTaskkillCommandTargetsProcessTree(t *testing.T) {
	cmd := newTaskkillCommand(1234)
	want := []string{"taskkill.exe", "/T", "/F", "/PID", "1234"}
	got := append([]string{filepath.Base(cmd.Path)}, cmd.Args[1:]...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("taskkill command = %#v, want %#v", got, want)
	}
}

func TestRunTaskCommandCancelsNestedProcessTreeWindows(t *testing.T) {
	if role := os.Getenv("WORKFLOW_PROCESS_HELPER"); role != "" {
		runWindowsProcessHelper(t, role)
		return
	}

	marker := filepath.Join(t.TempDir(), "descendant-finished")
	ready := filepath.Join(t.TempDir(), "descendant-ready")
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunTaskCommandCancelsNestedProcessTreeWindows$")
	cmd.Env = append(os.Environ(),
		"WORKFLOW_PROCESS_HELPER=parent",
		"WORKFLOW_DESCENDANT_MARKER="+marker,
		"WORKFLOW_DESCENDANT_READY="+ready,
	)
	done := make(chan error, 1)
	go func() { done <- runTaskCommand(ctx, cmd) }()
	waitForFile(t, ready)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("runTaskCommand() error = %v, want context canceled", err)
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("nested descendant survived cancellation and created %s", marker)
	}
}

func runWindowsProcessHelper(t *testing.T, role string) {
	t.Helper()
	switch role {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestRunTaskCommandCancelsNestedProcessTreeWindows$")
		child.Env = append(os.Environ(), "WORKFLOW_PROCESS_HELPER=child")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("WORKFLOW_DESCENDANT_READY"), []byte("ready"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := child.Wait(); err != nil {
			t.Fatal(err)
		}
	case "child":
		time.Sleep(500 * time.Millisecond)
		if err := os.WriteFile(os.Getenv("WORKFLOW_DESCENDANT_MARKER"), []byte("finished"), 0o644); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper role %q", role)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
