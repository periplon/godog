package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecuteCodexUsesExactArgvAndPreservesCaller(t *testing.T) {
	repo := newTestRepository(t)
	baseline := gitOutput(t, repo, "rev-parse", "HEAD")
	records := t.TempDir()
	t.Setenv("FAKE_CODEX_RECORDS", records)
	codex := writeExecutable(t, `#!/bin/sh
printf '%s\n' "$PWD" > "$FAKE_CODEX_RECORDS/cwd"
printf '%s\n' "$@" > "$FAKE_CODEX_RECORDS/argv"
[ "$5" = "--" ] || { printf 'missing option terminator\n' >&2; exit 2; }
printf 'task output\n' > task-output.txt
`)
	output := filepath.Join(t.TempDir(), "run")
	prompt := "--literal $(touch escaped) ' quoted"
	plan := &Plan{Version: 1, Name: "codex", Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "agent", Prompt: prompt, Model: "test-model", Attempts: 1,
	}}}}

	result, err := Execute(context.Background(), plan, RunOptions{
		Dir: repo, OutputDir: output, Jobs: 1, CodexBinary: codex,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Commit == "" || result.Tasks[0].Status != "success" || result.Tasks[0].Attempts != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	argv := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(records, "argv"))), "\n")
	wantArgv := []string{"exec", "-m", "test-model", "--dangerously-bypass-approvals-and-sandbox", "--", prompt}
	if strings.Join(argv, "\x00") != strings.Join(wantArgv, "\x00") {
		t.Fatalf("codex argv = %#v, want %#v", argv, wantArgv)
	}
	cwd := strings.TrimSpace(readFile(t, filepath.Join(records, "cwd")))
	if cwd != result.Tasks[0].Worktree {
		t.Fatalf("codex cwd = %q, want %q", cwd, result.Tasks[0].Worktree)
	}
	if got := readFile(t, filepath.Join(result.IntegrationWorktree, "task-output.txt")); got != "task output\n" {
		t.Fatalf("integrated file = %q", got)
	}
	if got := gitOutput(t, repo, "rev-parse", "HEAD"); got != baseline {
		t.Fatalf("caller HEAD changed from %s to %s", baseline, got)
	}
	if got := gitOutput(t, repo, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("caller became dirty: %s", got)
	}
	assertJSONFile(t, filepath.Join(output, "plan.json"))
	assertJSONFile(t, filepath.Join(output, "result.json"))
}

func TestExecuteKeepsAgentCommitsAndCommitsRemainingChanges(t *testing.T) {
	repo := newTestRepository(t)
	records := t.TempDir()
	t.Setenv("FAKE_CODEX_RECORDS", records)
	codex := writeExecutable(t, `#!/bin/sh
printf 'agent commit\n' > agent.txt
git add agent.txt
git commit -q -m 'feat: agent commit'
git rev-parse HEAD > "$FAKE_CODEX_RECORDS/agent-head"
printf 'runner commit\n' > remaining.txt
`)
	plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "commits", Prompt: "commit some work", Model: "m", Attempts: 1,
	}}}}
	result, err := Execute(context.Background(), plan, RunOptions{
		Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), Jobs: 1, CodexBinary: codex,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	agentHead := strings.TrimSpace(readFile(t, filepath.Join(records, "agent-head")))
	gitRun(t, result.Tasks[0].Worktree, "merge-base", "--is-ancestor", agentHead, result.Tasks[0].Commit)
	gitRun(t, result.IntegrationWorktree, "merge-base", "--is-ancestor", result.Tasks[0].Commit, result.Commit)
	for _, name := range []string{"agent.txt", "remaining.txt"} {
		if _, err := os.Stat(filepath.Join(result.IntegrationWorktree, name)); err != nil {
			t.Fatalf("integrated %s: %v", name, err)
		}
	}
}

func TestExecuteRunsReadyTasksConcurrentlyAndMergesDependencies(t *testing.T) {
	repo := newTestRepository(t)
	syncDir := t.TempDir()
	t.Setenv("WORKFLOW_SYNC_DIR", syncDir)
	waitForPeer := func(self, peer, output string) []string {
		return []string{"sh", "-c", "touch \"$WORKFLOW_SYNC_DIR/" + self + "\"; i=0; while [ ! -f \"$WORKFLOW_SYNC_DIR/" + peer + "\" ]; do i=$((i+1)); [ $i -lt 100 ] || exit 41; sleep 0.02; done; printf '" + self + "\\n' > " + output}
	}
	plan := &Plan{Version: 1, Name: "dag", Tasks: []Task{
		{TaskSpec: TaskSpec{ID: "a", Run: waitForPeer("a", "b", "a.txt"), Attempts: 1, Timeout: "4s"}},
		{TaskSpec: TaskSpec{ID: "b", Run: waitForPeer("b", "a", "b.txt"), Attempts: 1, Timeout: "4s"}},
		{TaskSpec: TaskSpec{ID: "c", Needs: []string{"b", "a"}, Run: []string{"sh", "-c", "test -f a.txt && test -f b.txt && printf 'c\\n' > c.txt"}, Attempts: 1}},
	}}

	result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), Jobs: 2})
	if err != nil {
		t.Fatalf("Execute() error = %v; result = %+v", err, result)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(result.IntegrationWorktree, name)); err != nil {
			t.Fatalf("integrated %s: %v", name, err)
		}
	}
	for _, task := range result.Tasks {
		if task.Status != "success" {
			t.Fatalf("task %s status = %s", task.ID, task.Status)
		}
		gitRun(t, result.IntegrationWorktree, "merge-base", "--is-ancestor", task.Commit, result.Commit)
	}
	byID := taskResultsByID(result)
	aStart := parseTime(t, byID["a"].StartedAt)
	aFinish := parseTime(t, byID["a"].FinishedAt)
	bStart := parseTime(t, byID["b"].StartedAt)
	bFinish := parseTime(t, byID["b"].FinishedAt)
	if !aStart.Before(bFinish) || !bStart.Before(aFinish) {
		t.Fatalf("independent tasks did not overlap: a=%s..%s b=%s..%s", aStart, aFinish, bStart, bFinish)
	}
}

func TestExecuteRetriesWithBoundedFeedbackAndBlocksDescendants(t *testing.T) {
	repo := newTestRepository(t)
	records := t.TempDir()
	t.Setenv("FAKE_CODEX_RECORDS", records)
	codex := writeExecutable(t, `#!/bin/sh
count_file="$FAKE_CODEX_RECORDS/count"
count=0
[ ! -f "$count_file" ] || count=$(cat "$count_file")
count=$((count+1))
printf '%s' "$count" > "$count_file"
printf '%s\n' "$6" > "$FAKE_CODEX_RECORDS/prompt-$count"
if [ "$count" -eq 1 ]; then printf 'FIRST_FAILURE\n' >&2; exit 7; fi
printf 'recovered\n' > recovered.txt
`)
	plan := &Plan{Version: 1, Name: "failures", Tasks: []Task{
		{TaskSpec: TaskSpec{ID: "retry", Prompt: "do work", Model: "m", Attempts: 2}},
		{TaskSpec: TaskSpec{ID: "fail", Run: []string{"sh", "-c", "i=0; while [ $i -lt 50 ]; do printf 'out\\n'; printf 'err\\n' >&2; i=$((i+1)); done; exit 9"}, Attempts: 2}},
		{TaskSpec: TaskSpec{ID: "blocked", Needs: []string{"fail"}, Run: []string{"sh", "-c", "touch should-not-run"}, Attempts: 1}},
	}}

	result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), Jobs: 2, CodexBinary: codex})
	if err == nil {
		t.Fatal("Execute() succeeded with a failed task")
	}
	byID := taskResultsByID(result)
	if byID["retry"].Status != "success" || byID["retry"].Attempts != 2 {
		t.Fatalf("retry result = %+v", byID["retry"])
	}
	if byID["fail"].Status != "failed" || byID["fail"].Attempts != 2 {
		t.Fatalf("fail result = %+v", byID["fail"])
	}
	if byID["blocked"].Status != "blocked" || byID["blocked"].Attempts != 0 {
		t.Fatalf("blocked result = %+v", byID["blocked"])
	}
	retryPrompt := readFile(t, filepath.Join(records, "prompt-2"))
	if !strings.Contains(retryPrompt, "FIRST_FAILURE") || !strings.Contains(retryPrompt, "do work") {
		t.Fatalf("retry prompt lacks task and failure feedback: %q", retryPrompt)
	}
	assertJSONFile(t, filepath.Join(filepath.Dir(byID["retry"].Worktree), "..", "result.json"))
}

func TestExecutePropagatesDependencyFailureTransitively(t *testing.T) {
	repo := newTestRepository(t)
	markerDir := t.TempDir()
	t.Setenv("WORKFLOW_MARKER_DIR", markerDir)
	plan := &Plan{Version: 1, Name: "dependency failure", Tasks: []Task{
		{TaskSpec: TaskSpec{ID: "fail", Run: []string{"sh", "-c", "printf 'failure output\\n'; exit 23"}, Attempts: 1}},
		{TaskSpec: TaskSpec{ID: "direct", Needs: []string{"fail"}, Run: []string{"sh", "-c", "touch \"$WORKFLOW_MARKER_DIR/direct\""}, Attempts: 1}},
		{TaskSpec: TaskSpec{ID: "transitive", Needs: []string{"direct"}, Run: []string{"sh", "-c", "touch \"$WORKFLOW_MARKER_DIR/transitive\""}, Attempts: 1}},
		{TaskSpec: TaskSpec{ID: "independent", Run: []string{"sh", "-c", "printf 'ok\\n' > independent.txt"}, Attempts: 1}},
	}}

	result, err := Execute(context.Background(), plan, RunOptions{
		Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), Jobs: 2,
	})
	if err == nil {
		t.Fatal("Execute() succeeded with a failed dependency")
	}
	byID := taskResultsByID(result)
	if got := byID["fail"]; got.Status != "failed" || got.Attempts != 1 || got.Worktree == "" || got.Log == "" {
		t.Fatalf("failed task result = %+v", got)
	}
	for _, id := range []string{"direct", "transitive"} {
		got := byID[id]
		if got.Status != "blocked" || got.Attempts != 0 || got.Commit != "" {
			t.Fatalf("dependent task %s result = %+v", id, got)
		}
		if _, statErr := os.Stat(filepath.Join(markerDir, id)); !os.IsNotExist(statErr) {
			t.Fatalf("blocked task %s executed: %v", id, statErr)
		}
	}
	if got := byID["independent"]; got.Status != "success" || got.Commit == "" {
		t.Fatalf("independent task result = %+v", got)
	}
	if _, statErr := os.Stat(byID["fail"].Worktree); statErr != nil {
		t.Fatalf("failed task worktree missing: %v", statErr)
	}
	if log := readFile(t, byID["fail"].Log); !strings.Contains(log, "failure output") {
		t.Fatalf("failed task log lacks command output: %q", log)
	}
}

func TestExecuteHonorsExpectedExitAndCancellation(t *testing.T) {
	t.Run("expected nonzero exit succeeds", func(t *testing.T) {
		repo := newTestRepository(t)
		plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
			ID: "expected", Run: []string{"sh", "-c", "printf ok > expected.txt; exit 7"}, ExpectExit: 7, Attempts: 1,
		}}}}
		result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), Jobs: 1})
		if err != nil || result.Tasks[0].Status != "success" {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
	})

	t.Run("unexpected zero exit fails", func(t *testing.T) {
		repo := newTestRepository(t)
		plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
			ID: "unexpected", Run: []string{"sh", "-c", "printf wrong > wrong.txt"}, ExpectExit: 1, Attempts: 1,
		}}}}
		result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), Jobs: 1})
		if err == nil || result.Tasks[0].Status != "failed" {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
		if result.Tasks[0].Commit != "" {
			t.Fatalf("unexpected exit was committed: %+v", result.Tasks[0])
		}
	})

	t.Run("cancellation cannot be expected", func(t *testing.T) {
		repo := newTestRepository(t)
		marker := filepath.Join(t.TempDir(), "orphan-marker")
		t.Setenv("WORKFLOW_ORPHAN_MARKER", marker)
		plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
			ID: "cancel", Run: []string{"sh", "-c", "(sleep 0.5; touch \"$WORKFLOW_ORPHAN_MARKER\") & wait"}, ExpectExit: 137, Attempts: 1,
		}}}}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		started := time.Now()
		result, err := Execute(ctx, plan, RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), Jobs: 1})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded; result = %+v", err, result)
		}
		if time.Since(started) > 3*time.Second {
			t.Fatalf("cancellation took %v", time.Since(started))
		}
		if result.Tasks[0].Status == "success" {
			t.Fatalf("cancelled task reported success: %+v", result.Tasks[0])
		}
		time.Sleep(600 * time.Millisecond)
		if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
			t.Fatalf("child process survived cancellation and created %s", marker)
		}
	})
}

func TestExecuteTaskTimeoutIsFailureNotCallerCancellation(t *testing.T) {
	repo := newTestRepository(t)
	plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "timeout", Run: []string{"sh", "-c", "sleep 10"}, Attempts: 1, Timeout: "100ms",
	}}}}
	result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), Jobs: 1})
	if err == nil {
		t.Fatal("Execute() succeeded after task timeout")
	}
	if result.Tasks[0].Status != "failed" || !strings.Contains(result.Tasks[0].Error, "deadline exceeded") {
		t.Fatalf("timeout result = %+v", result.Tasks[0])
	}
}

func TestExecuteWorktreeSetupFailureReportsOnlyExistingArtifacts(t *testing.T) {
	repo := newTestRepository(t)
	output := filepath.Join(t.TempDir(), "run")
	plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "setup-timeout", Run: []string{"true"}, Attempts: 1, Timeout: "1ns",
	}}}}

	result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: output, Jobs: 1})
	if err == nil {
		t.Fatal("Execute() succeeded after worktree setup timeout")
	}
	task := result.Tasks[0]
	if task.Status != "failed" {
		t.Fatalf("task status = %q, want failed", task.Status)
	}
	if task.Worktree != "" {
		t.Fatalf("failed setup reported nonexistent worktree %q", task.Worktree)
	}
	if task.Log == "" {
		t.Fatal("failed setup did not report a log")
	}
	if _, statErr := os.Stat(task.Log); statErr != nil {
		t.Fatalf("failed setup log missing: %v", statErr)
	}
	if log := readFile(t, task.Log); !strings.Contains(log, "create worktree") {
		t.Fatalf("failed setup log lacks failure context: %q", log)
	}
}

func TestExecutePreservesIntegrationConflict(t *testing.T) {
	repo := newTestRepository(t)
	plan := &Plan{Version: 1, Tasks: []Task{
		{TaskSpec: TaskSpec{ID: "a", Run: []string{"sh", "-c", "printf a > seed.txt"}, Attempts: 1}},
		{TaskSpec: TaskSpec{ID: "b", Run: []string{"sh", "-c", "printf b > seed.txt"}, Attempts: 1}},
	}}
	result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), Jobs: 2})
	if err == nil || !strings.Contains(err.Error(), "integration conflict") {
		t.Fatalf("error = %v, want integration conflict", err)
	}
	if result.IntegrationWorktree == "" {
		t.Fatal("integration worktree was not preserved")
	}
	if _, statErr := os.Stat(result.IntegrationWorktree); statErr != nil {
		t.Fatalf("integration worktree missing: %v", statErr)
	}
}

func TestExecuteReportsIntegrationHookFailureWithoutCallingItAConflict(t *testing.T) {
	repo := newTestRepository(t)
	hooks := t.TempDir()
	hook := filepath.Join(hooks, "pre-merge-commit")
	if err := os.WriteFile(hook, []byte(`#!/bin/sh
case "$PWD" in
  */integration) exit 42 ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "config", "core.hooksPath", hooks)
	plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "change", Run: []string{"sh", "-c", "printf change > change.txt"}, Attempts: 1,
	}}}}

	result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), Jobs: 1})
	if err == nil || !strings.Contains(err.Error(), "integration merge failed") {
		t.Fatalf("error = %v, want integration merge failure; result = %+v", err, result)
	}
	if strings.Contains(err.Error(), "conflict") {
		t.Fatalf("hook failure misreported as conflict: %v", err)
	}
}

func TestExecuteReportsIntegrationCancellationWithoutCallingItAConflict(t *testing.T) {
	repo := newTestRepository(t)
	hooks := t.TempDir()
	ready := filepath.Join(t.TempDir(), "integration-hook-ready")
	t.Setenv("WORKFLOW_INTEGRATION_HOOK_READY", ready)
	hook := filepath.Join(hooks, "pre-merge-commit")
	if err := os.WriteFile(hook, []byte(`#!/bin/sh
case "$PWD" in
  */integration)
    touch "$WORKFLOW_INTEGRATION_HOOK_READY"
    while :; do sleep 1; done
    ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "config", "core.hooksPath", hooks)
	plan := &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "change", Run: []string{"sh", "-c", "printf change > change.txt"}, Attempts: 1,
	}}}}
	type outcome struct {
		result *Result
		err    error
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan outcome, 1)
	output := filepath.Join(t.TempDir(), "run")
	go func() {
		result, err := Execute(ctx, plan, RunOptions{Dir: repo, OutputDir: output, Jobs: 1})
		done <- outcome{result: result, err: err}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("integration hook did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	got := <-done
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled; result = %+v", got.err, got.result)
	}
	if strings.Contains(got.err.Error(), "conflict") {
		t.Fatalf("cancellation misreported as conflict: %v", got.err)
	}
}

func TestExecuteRejectsExistingOutputDirectory(t *testing.T) {
	repo := newTestRepository(t)
	output := t.TempDir()
	_, err := Execute(context.Background(), noopPlan(), RunOptions{Dir: repo, OutputDir: output, Jobs: 1})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want existing directory rejection", err)
	}
}

func TestExecuteRejectsDirtySourceAndUnsafeOutput(t *testing.T) {
	t.Run("dirty source", func(t *testing.T) {
		repo := newTestRepository(t)
		if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Execute(context.Background(), noopPlan(), RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), Jobs: 1})
		if err == nil || !strings.Contains(err.Error(), "must be clean") {
			t.Fatalf("error = %v, want dirty repository rejection", err)
		}
	})

	t.Run("output inside source", func(t *testing.T) {
		repo := newTestRepository(t)
		_, err := Execute(context.Background(), noopPlan(), RunOptions{Dir: repo, OutputDir: filepath.Join(repo, "run"), Jobs: 1})
		if err == nil || !strings.Contains(err.Error(), "external") {
			t.Fatalf("error = %v, want unsafe output rejection", err)
		}
	})

	t.Run("default temporary root inside source", func(t *testing.T) {
		repo := newTestRepository(t)
		temporaryRoot := filepath.Join(repo, ".tmp")
		if err := os.Mkdir(temporaryRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(temporaryRoot, ".keep"), []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, repo, "add", ".tmp/.keep")
		gitRun(t, repo, "commit", "-q", "-m", "test: add temporary root")
		t.Setenv("TMPDIR", temporaryRoot)
		_, err := Execute(context.Background(), noopPlan(), RunOptions{Dir: repo, Jobs: 1})
		if err == nil || !strings.Contains(err.Error(), "external") {
			t.Fatalf("error = %v, want unsafe temporary root rejection", err)
		}
		if got := gitOutput(t, repo, "status", "--porcelain", "--untracked-files=all"); got != "" {
			t.Fatalf("caller became dirty: %s", got)
		}
	})
}

func TestExecuteValidatesDirectPlansBeforeRepositoryMutation(t *testing.T) {
	tests := []struct {
		name string
		plan *Plan
	}{
		{name: "version", plan: &Plan{Version: 0, Tasks: noopPlan().Tasks}},
		{name: "empty tasks", plan: &Plan{Version: 1}},
		{name: "too many attempts", plan: &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{ID: "task", Run: []string{"true"}, Attempts: 6}}}}},
		{name: "unsafe ID", plan: &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{ID: "a/b", Run: []string{"true"}, Attempts: 1}}}}},
		{name: "whitespace executable", plan: &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{ID: "task", Run: []string{"  "}, Attempts: 1}}}}},
		{name: "prompt expected exit", plan: &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{ID: "task", Prompt: "work", Model: "m", ExpectExit: 1, Attempts: 1}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "run")
			if _, err := Execute(context.Background(), test.plan, RunOptions{Dir: t.TempDir(), OutputDir: output, Jobs: 1}); err == nil {
				t.Fatal("Execute() accepted invalid direct plan")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("invalid plan created output directory: %v", err)
			}
		})
	}
}

func newTestRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.name", "Workflow Test")
	gitRun(t, dir, "config", "user.email", "workflow@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "seed.txt")
	gitRun(t, dir, "commit", "-q", "-m", "test: seed")
	return dir
}

func noopPlan() *Plan {
	return &Plan{Version: 1, Tasks: []Task{{TaskSpec: TaskSpec{
		ID: "noop", Run: []string{"true"}, Attempts: 1,
	}}}}
}

func writeExecutable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-codex")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func assertJSONFile(t *testing.T, path string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read JSON %s: %v", path, err)
	}
	var value any
	if err := json.Unmarshal(contents, &value); err != nil {
		t.Fatalf("invalid JSON %s: %v", path, err)
	}
}

func taskResultsByID(result *Result) map[string]TaskResult {
	results := make(map[string]TaskResult, len(result.Tasks))
	for _, task := range result.Tasks {
		results[task.ID] = task
	}
	return results
}

func parseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parse timestamp %q: %v", value, err)
	}
	return parsed
}
