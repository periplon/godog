package workflow

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Exercise the generated dependency graph through the real runner and registry.
// The fake agent edits the same file in each task, exposing baseline isolation bugs.
func TestGeneratedWorkflowExecutesAndTracksImplementation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake agent uses a POSIX shell")
	}
	repo := newTestRepository(t)
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(repo, name+".feature"), []byte("Feature: "+name+"\n Scenario: implemented\n  Given a working behavior\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	spec := filepath.Join(repo, "workflow.yaml")
	if err := Generate(context.Background(), []string{"*.feature"}, GenerateOptions{Dir: repo, Output: spec, Model: "test-model"}); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "test: generated policy")
	plan, err := CompileIncremental(context.Background(), spec, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tasks) != 3 {
		t.Fatalf("want two implementation tasks and review, got %d", len(plan.Tasks))
	}
	codex := writeExecutable(t, "#!/bin/sh\nprintf 'completed\\n' >> shared.txt\n")
	result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), CodexBinary: codex, Jobs: 3})
	if result != nil {
		t.Cleanup(func() { os.RemoveAll(result.RunDirectory) })
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(result.IntegrationWorktree, "shared.txt")); got != "completed\ncompleted\ncompleted\n" {
		t.Fatalf("tasks did not build on prior changes: %q", got)
	}
	gitRun(t, repo, "merge", "--ff-only", result.Commit)
	next, err := CompileIncremental(context.Background(), spec, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Tasks) != 0 {
		t.Fatalf("generated workflow not tracked after merge: %d tasks", len(next.Tasks))
	}
}
