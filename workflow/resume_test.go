package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResumeDoesNotRepeatValidatedSuccess(t *testing.T) {
	repo := newTestRepository(t)
	records := t.TempDir()
	t.Setenv("WORKFLOW_RESUME_RECORDS", records)
	output := filepath.Join(t.TempDir(), "run")
	plan := &Plan{Version: 1, Name: "resume", Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "once", Run: []string{"sh", "-c", "n=0; [ ! -f \"$WORKFLOW_RESUME_RECORDS/count\" ] || n=$(cat \"$WORKFLOW_RESUME_RECORDS/count\"); n=$((n+1)); printf %s $n > \"$WORKFLOW_RESUME_RECORDS/count\"; printf done > done.txt"}, Attempts: 1,
	}}}}
	first, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: output, Jobs: 1})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	resumed, err := Resume(context.Background(), output, RunOptions{Dir: repo, Jobs: 1})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(records, "count"))); got != "1" {
		t.Fatalf("successful task ran %s times", got)
	}
	if resumed.Tasks[0].Commit != first.Tasks[0].Commit || resumed.Tasks[0].Attempts != 1 {
		t.Fatalf("resumed task = %+v, first = %+v", resumed.Tasks[0], first.Tasks[0])
	}
	if resumed.Commit != first.Commit || resumed.IntegrationWorktree != first.IntegrationWorktree {
		t.Fatalf("all-success resume rebuilt integration: resumed=%+v first=%+v", resumed, first)
	}
	if len(resumed.Runs) != 2 || !resumed.Runs[1].Resumed || resumed.Runs[1].Status != "success" {
		t.Fatalf("run history = %+v", resumed.Runs)
	}
	canonicalOutput, err := filepath.EvalSymlinks(output)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.RunDirectory != canonicalOutput {
		t.Fatalf("run directory = %q, want %q", resumed.RunDirectory, canonicalOutput)
	}
}

func TestResumeKeepsDistinctCheckpointsForSimilarTaskIDs(t *testing.T) {
	repo := newTestRepository(t)
	records := t.TempDir()
	t.Setenv("WORKFLOW_RESUME_RECORDS", records)
	output := filepath.Join(t.TempDir(), "run")
	plan := &Plan{Version: 1, Tasks: []Task{
		{TaskSpec: TaskSpec{ID: "a.b", Run: []string{"sh", "-c", "printf x >> \"$WORKFLOW_RESUME_RECORDS/dot\""}, Attempts: 1}},
		{TaskSpec: TaskSpec{ID: "a_b", Run: []string{"sh", "-c", "printf x >> \"$WORKFLOW_RESUME_RECORDS/underscore\""}, Attempts: 1}},
		{TaskSpec: TaskSpec{ID: "a", Run: []string{"sh", "-c", "printf x >> \"$WORKFLOW_RESUME_RECORDS/lower\""}, Attempts: 1}},
		{TaskSpec: TaskSpec{ID: "A", Run: []string{"sh", "-c", "printf x >> \"$WORKFLOW_RESUME_RECORDS/upper\""}, Attempts: 1}},
	}}
	result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: output, Jobs: 2})
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, task := range result.Tasks {
		key := strings.ToLower(task.Checkpoint)
		if seen[key] {
			t.Fatalf("checkpoint paths collide case-insensitively: %+v", result.Tasks)
		}
		seen[key] = true
	}
	if _, err := Resume(context.Background(), output, RunOptions{Dir: repo, Jobs: 2}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(records, "dot")) != "x" || readFile(t, filepath.Join(records, "underscore")) != "x" || readFile(t, filepath.Join(records, "lower")) != "x" || readFile(t, filepath.Join(records, "upper")) != "x" {
		t.Fatal("validated successful tasks reran")
	}
}

func TestResumeRetriesOnlyFailureWithFreshBudgetAndDependencies(t *testing.T) {
	repo := newTestRepository(t)
	records := t.TempDir()
	t.Setenv("WORKFLOW_RESUME_RECORDS", records)
	output := filepath.Join(t.TempDir(), "run")
	plan := &Plan{Version: 1, Name: "resume failure", Tasks: []Task{
		{TaskSpec: TaskSpec{ID: "base", Run: []string{"sh", "-c", "printf base > base.txt; printf x >> \"$WORKFLOW_RESUME_RECORDS/base-runs\""}, Attempts: 1}},
		{TaskSpec: TaskSpec{ID: "finish", Needs: []string{"base"}, Run: []string{"sh", "-c", "n=0; [ ! -f \"$WORKFLOW_RESUME_RECORDS/finish-count\" ] || n=$(cat \"$WORKFLOW_RESUME_RECORDS/finish-count\"); n=$((n+1)); printf %s $n > \"$WORKFLOW_RESUME_RECORDS/finish-count\"; [ $n -gt 1 ] || exit 7; test -f base.txt; printf finish > finish.txt"}, Attempts: 1}},
	}}
	first, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: output, Jobs: 1})
	if err == nil {
		t.Fatal("initial execution unexpectedly succeeded")
	}
	firstByID := taskResultsByID(first)
	failedWorktree := firstByID["finish"].Worktree
	resumed, err := Resume(context.Background(), output, RunOptions{Dir: repo, Jobs: 1})
	if err != nil {
		t.Fatalf("Resume() error = %v; result = %+v", err, resumed)
	}
	byID := taskResultsByID(resumed)
	if byID["base"].Attempts != 1 || readFile(t, filepath.Join(records, "base-runs")) != "x" {
		t.Fatalf("successful dependency reran: %+v", byID["base"])
	}
	if byID["finish"].Attempts != 2 || byID["finish"].Status != "success" {
		t.Fatalf("resumed failure = %+v", byID["finish"])
	}
	if byID["finish"].Worktree == failedWorktree || !strings.Contains(byID["finish"].Worktree, "resume-2") {
		t.Fatalf("unfinished worktree was reused: old=%s new=%s", failedWorktree, byID["finish"].Worktree)
	}
	if _, err := os.Stat(failedWorktree); err != nil {
		t.Fatalf("old partial worktree was not retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(resumed.IntegrationWorktree, "finish.txt")); err != nil {
		t.Fatalf("resumed result not integrated: %v", err)
	}
}

func TestResumeRecoversSuccessfulCheckpointMissingFromResultJournal(t *testing.T) {
	repo := newTestRepository(t)
	records := t.TempDir()
	t.Setenv("WORKFLOW_RESUME_RECORDS", records)
	output := filepath.Join(t.TempDir(), "run")
	plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "checkpoint", Run: []string{"sh", "-c", "printf x >> \"$WORKFLOW_RESUME_RECORDS/runs\"; printf complete > complete.txt"}, Attempts: 1,
	}}}}
	result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: output, Jobs: 1})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := result.Tasks[0].Checkpoint
	updateResultFile(t, output, func(value map[string]any) {
		task := value["tasks"].([]any)[0].(map[string]any)
		task["status"] = "running"
		task["commit"] = ""
		task["checkpoint"] = ""
	})
	resumed, err := Resume(context.Background(), output, RunOptions{Dir: repo, Jobs: 1})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if readFile(t, filepath.Join(records, "runs")) != "x" {
		t.Fatal("checkpointed task reran")
	}
	if resumed.Tasks[0].Status != "success" || resumed.Tasks[0].Checkpoint != checkpoint {
		t.Fatalf("checkpoint was not recovered: %+v", resumed.Tasks[0])
	}
}

func TestResumeDoesNotAcceptZeroExitWhenOneExpected(t *testing.T) {
	repo := newTestRepository(t)
	output := filepath.Join(t.TempDir(), "run")
	plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "red", Run: []string{"sh", "-c", "exit 0"}, ExpectExit: 1, Attempts: 1,
	}}}}
	if _, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: output}); err == nil {
		t.Fatal("Execute() unexpectedly accepted exit 0")
	}
	result, err := Resume(context.Background(), output, RunOptions{Dir: repo})
	if err == nil {
		t.Fatal("Resume() unexpectedly accepted exit 0")
	}
	if result.Tasks[0].Status != "failed" || result.Tasks[0].Attempts != 2 {
		t.Fatalf("resumed task = %+v", result.Tasks[0])
	}
}

func TestResumeCodexGetsPriorFailureFeedback(t *testing.T) {
	repo := newTestRepository(t)
	records := t.TempDir()
	t.Setenv("WORKFLOW_RESUME_RECORDS", records)
	codex := writeExecutable(t, `#!/bin/sh
count_file="$WORKFLOW_RESUME_RECORDS/codex-count"
count=0
[ ! -f "$count_file" ] || count=$(cat "$count_file")
count=$((count+1))
printf '%s' "$count" > "$count_file"
printf '%s\n' "$6" > "$WORKFLOW_RESUME_RECORDS/prompt-$count"
if [ "$count" -eq 1 ]; then printf 'FIRST_RESUME_FAILURE\n' >&2; exit 8; fi
printf complete > complete.txt
`)
	output := filepath.Join(t.TempDir(), "run")
	plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "agent", Prompt: "finish task", Model: "m", Attempts: 1,
	}}}}
	if _, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: output, CodexBinary: codex}); err == nil {
		t.Fatal("Execute() unexpectedly succeeded")
	}
	if _, err := Resume(context.Background(), output, RunOptions{Dir: repo, CodexBinary: codex}); err != nil {
		t.Fatal(err)
	}
	prompt := readFile(t, filepath.Join(records, "prompt-2"))
	if !strings.Contains(prompt, "finish task") || !strings.Contains(prompt, "FIRST_RESUME_FAILURE") {
		t.Fatalf("resumed prompt lacks bounded prior feedback: %q", prompt)
	}
}

func TestResumeReconstructsInterruptedIntegration(t *testing.T) {
	repo := newTestRepository(t)
	output := filepath.Join(t.TempDir(), "run")
	result, err := Execute(context.Background(), noopPlan(), RunOptions{Dir: repo, OutputDir: output})
	if err != nil {
		t.Fatal(err)
	}
	originalIntegration := result.IntegrationWorktree
	if _, err := commandOutput(context.Background(), originalIntegration, "git", "commit", "--allow-empty", "--no-gpg-sign", "-m", "test: simulate crash after integration merge"); err != nil {
		t.Fatal(err)
	}
	updateResultFile(t, output, func(value map[string]any) {
		runs := value["runs"].([]any)
		last := runs[len(runs)-1].(map[string]any)
		last["status"] = "running"
		delete(last, "finished_at")
	})
	resumed, err := Resume(context.Background(), output, RunOptions{Dir: repo})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.IntegrationWorktree == originalIntegration {
		t.Fatal("interrupted integration worktree was reused")
	}
	if resumed.Runs[0].Status != "interrupted" {
		t.Fatalf("prior run = %+v", resumed.Runs[0])
	}
}

func TestResumeRejectsChangedCompletedIntegration(t *testing.T) {
	repo := newTestRepository(t)
	output := filepath.Join(t.TempDir(), "run")
	result, err := Execute(context.Background(), noopPlan(), RunOptions{Dir: repo, OutputDir: output})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commandOutput(context.Background(), result.IntegrationWorktree, "git", "commit", "--allow-empty", "--no-gpg-sign", "-m", "test: alter completed integration"); err != nil {
		t.Fatal(err)
	}
	if _, err := Resume(context.Background(), output, RunOptions{Dir: repo}); err == nil || !strings.Contains(err.Error(), "completed integration") {
		t.Fatalf("changed completed integration error = %v", err)
	}
}

func TestResumeRejectsConcurrentExecution(t *testing.T) {
	repo := newTestRepository(t)
	records := t.TempDir()
	t.Setenv("WORKFLOW_RESUME_RECORDS", records)
	output := filepath.Join(t.TempDir(), "run")
	plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "wait", Run: []string{"sh", "-c", "touch \"$WORKFLOW_RESUME_RECORDS/started\"; while [ ! -f \"$WORKFLOW_RESUME_RECORDS/release\" ]; do sleep 0.02; done"}, Attempts: 1, Timeout: "5s",
	}}}}
	done := make(chan error, 1)
	go func() {
		_, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: output, Jobs: 1})
		done <- err
	}()
	waitForResumeFile(t, filepath.Join(records, "started"))
	if _, err := Resume(context.Background(), output, RunOptions{Dir: repo, Jobs: 1}); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("concurrent Resume() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(records, "release"), []byte("release"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestResumeRejectsAlteredDurableArtifacts(t *testing.T) {
	newRun := func(t *testing.T) (string, string, *Result) {
		repo := newTestRepository(t)
		output := filepath.Join(t.TempDir(), "run")
		result, err := Execute(context.Background(), noopPlan(), RunOptions{Dir: repo, OutputDir: output, Jobs: 1})
		if err != nil {
			t.Fatal(err)
		}
		return repo, output, result
	}
	t.Run("plan", func(t *testing.T) {
		repo, output, _ := newRun(t)
		contents := readFile(t, filepath.Join(output, "plan.json"))
		if err := os.WriteFile(filepath.Join(output, "plan.json"), []byte(strings.Replace(contents, `"name": ""`, `"name": "altered"`, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Resume(context.Background(), output, RunOptions{Dir: repo, Jobs: 1}); err == nil || !strings.Contains(err.Error(), "plan") {
			t.Fatalf("altered plan error = %v", err)
		}
	})
	t.Run("checkpoint", func(t *testing.T) {
		repo, output, result := newRun(t)
		var checkpoint map[string]any
		readJSONForResume(t, result.Tasks[0].Checkpoint, &checkpoint)
		task := checkpoint["task"].(map[string]any)
		task["commit"] = strings.Repeat("0", 40)
		writeJSONForResume(t, result.Tasks[0].Checkpoint, checkpoint)
		if _, err := Resume(context.Background(), output, RunOptions{Dir: repo, Jobs: 1}); err == nil || !strings.Contains(err.Error(), "checkpoint") {
			t.Fatalf("altered checkpoint error = %v", err)
		}
	})
	t.Run("log", func(t *testing.T) {
		repo, output, result := newRun(t)
		if err := os.WriteFile(result.Tasks[0].Log, []byte("altered"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Resume(context.Background(), output, RunOptions{Dir: repo, Jobs: 1}); err == nil || !strings.Contains(err.Error(), "log") {
			t.Fatalf("altered log error = %v", err)
		}
	})
}

func updateResultFile(t *testing.T, output string, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(output, "result.json")
	var value map[string]any
	readJSONForResume(t, path, &value)
	edit(value)
	writeJSONForResume(t, path, value)
}

func readJSONForResume(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, value); err != nil {
		t.Fatal(err)
	}
}

func writeJSONForResume(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitForResumeFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + path)
}
