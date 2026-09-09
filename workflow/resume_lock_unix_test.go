//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd
// +build darwin dragonfly freebsd linux netbsd openbsd

package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResumeWaitsForChildLeftByCrashedRunner(t *testing.T) {
	if os.Getenv("WORKFLOW_CRASH_HELPER") == "1" {
		plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
			ID: "orphan", Run: []string{"sh", "-c", "touch \"$WORKFLOW_CRASH_RECORDS/started\"; sleep 1; touch \"$WORKFLOW_CRASH_RECORDS/finished\""}, Attempts: 1, Timeout: "5s",
		}}}}
		_, _ = Execute(context.Background(), plan, RunOptions{Dir: os.Getenv("WORKFLOW_CRASH_REPO"), OutputDir: os.Getenv("WORKFLOW_CRASH_OUTPUT")})
		os.Exit(0)
	}

	repo := newTestRepository(t)
	records := t.TempDir()
	t.Setenv("WORKFLOW_CRASH_RECORDS", records)
	output := filepath.Join(t.TempDir(), "run")
	cmd := exec.Command(os.Args[0], "-test.run=^TestResumeWaitsForChildLeftByCrashedRunner$")
	cmd.Env = append(os.Environ(),
		"WORKFLOW_CRASH_HELPER=1",
		"WORKFLOW_CRASH_REPO="+repo,
		"WORKFLOW_CRASH_OUTPUT="+output,
		"WORKFLOW_CRASH_RECORDS="+records,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForResumeFile(t, filepath.Join(records, "started"))
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	if _, err := Resume(context.Background(), output, RunOptions{Dir: repo}); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("Resume() while crash-left child is alive error = %v", err)
	}
	waitForResumeFile(t, filepath.Join(records, "finished"))
	var result *Result
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		result, err = Resume(context.Background(), output, RunOptions{Dir: repo})
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "locked") {
			t.Fatalf("Resume() after crash-left child exited error = %v; result = %+v", err, result)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Resume() remained locked after crash-left child exited: %v", err)
	}
}
