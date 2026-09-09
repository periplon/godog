package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteRejectsStaleTrackedInputsBeforeCreatingRun(t *testing.T) {
	for _, drift := range []string{"baseline", "transient-feature"} {
		t.Run(drift, func(t *testing.T) {
			repo := newTestRepository(t)
			original := "Feature: source provenance\n Scenario: initial\n  Given the original requirement\n"
			feature := writeTestFile(t, repo, "input.feature", original)
			spec := writeTestFile(t, repo, "workflow.yaml", "version: 1\nname: source-provenance\nfeatures: [input.feature]\ntasks:\n - id: verify\n   run: [git, status, --porcelain]\n")
			gitRun(t, repo, "add", ".")
			gitRun(t, repo, "commit", "--no-gpg-sign", "-qm", "test: define tracked inputs")
			if drift == "transient-feature" {
				if err := os.WriteFile(feature, []byte(strings.Replace(original, "original requirement", "transient requirement", 1)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := CompileIncremental(context.Background(), spec, repo)
			if err != nil {
				t.Fatal(err)
			}
			if drift == "baseline" {
				gitRun(t, repo, "commit", "--no-gpg-sign", "--allow-empty", "-qm", "test: move source baseline")
			} else if err := os.WriteFile(feature, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "execution")
			_, err = Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: output})
			if err == nil {
				t.Fatal("stale tracked inputs were executed")
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("invalid plan created run artifacts: %v", statErr)
			}
		})
	}
}

func TestExecuteTrackedNoOpCreatesAnExecutionRecord(t *testing.T) {
	repo := newTestRepository(t)
	writeTestFile(t, repo, "input.feature", "Feature: cached\n Scenario: implemented\n  Given an outcome\n")
	spec := writeTestFile(t, repo, "workflow.yaml", "version: 1\nname: cached\nfeatures: [input.feature]\ntasks:\n - id: verify\n   run: [git, status, --porcelain]\n")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "--no-gpg-sign", "-qm", "test: define cached workflow")
	plan, err := CompileIncremental(context.Background(), spec, repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "first")})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordImplementation(context.Background(), plan, result, repo); err != nil {
		t.Fatal(err)
	}
	noOp, err := CompileIncremental(context.Background(), spec, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !noOp.Tracking.NoOp {
		t.Fatal("expected no-op plan")
	}
	output := filepath.Join(t.TempDir(), "no-op")
	result, err = Execute(context.Background(), noOp, RunOptions{Dir: repo, OutputDir: output})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tasks) != 0 {
		t.Fatal("no-op executed a task")
	}
	if _, err := os.Stat(filepath.Join(output, "result.json")); err != nil {
		t.Fatal(err)
	}
}
