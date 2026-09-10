package internal

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog/workflow"
)

func TestWorkflowGenerateCLI(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "sample.feature"), []byte("Feature: sample\n  Scenario: one\n    Given an outcome\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "generated.yaml")
	cmd := CreateWorkflowCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"generate", "*.feature", "--repo", repo, "--output", output, "--model", "test-model", "--prompt", "Preserve public interfaces."})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	plan, err := workflow.Compile(output)
	if err != nil {
		t.Fatalf("generated workflow does not compile: %v", err)
	}
	if len(plan.Tasks) != 2 {
		t.Fatalf("wanted implementation and final review, got %d tasks", len(plan.Tasks))
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(contents, []byte("test-model")) || !bytes.Contains(contents, []byte("Preserve public interfaces.")) {
		t.Fatalf("model or prompt missing: %s", contents)
	}
}

func TestWorkflowGenerateCLIRequiresInputs(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"generate", "--output", "out.yaml", "--model", "test-model"}, "requires at least 1 arg"},
		{[]string{"generate", "input.feature", "--model", "test-model"}, `required flag(s) "output" not set`},
		{[]string{"generate", "input.feature", "--output", "out.yaml"}, `required flag(s) "model" not set`},
	} {
		cmd := CreateWorkflowCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(test.args)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("args %v: got %v, want %s", test.args, err, test.want)
		}
	}
}

func TestWorkflowGenerateCLIPlannerFlags(t *testing.T) {
	cmd := CreateWorkflowCmd()
	generate, _, err := cmd.Find([]string{"generate"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"generator", "codex", "models", "reasoning-effort"} {
		if generate.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
}

func TestWorkflowGenerateCLIEffort(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "sample.feature"), []byte("Feature: sample\n  Scenario: one\n    Given an outcome\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "generated.yaml")
	cmd := CreateWorkflowCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"generate", "*.feature", "--repo", repo, "--output", output, "--model", "test-model", "--generator", "deterministic", "--reasoning-effort", "xhigh"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	plan, err := workflow.Compile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range plan.Tasks {
		if task.ReasoningEffort != "xhigh" {
			t.Errorf("task %s effort = %q", task.ID, task.ReasoningEffort)
		}
	}
}

func TestWorkflowGenerateCLIRejectsPlannerOptions(t *testing.T) {
	for _, flags := range [][]string{{"--generator", "unknown"}, {"--reasoning-effort", "max"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			repo := t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, "sample.feature"), []byte("Feature: sample\n  Scenario: one\n    Given an outcome\n"), 0600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(repo, "generated.yaml")
			cmd := CreateWorkflowCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			args := []string{"generate", "*.feature", "--repo", repo, "--output", output, "--model", "test-model"}
			cmd.SetArgs(append(args, flags...))
			if err := cmd.Execute(); err == nil {
				t.Fatal("accepted invalid planner options")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("output exists: %v", err)
			}
		})
	}
}
