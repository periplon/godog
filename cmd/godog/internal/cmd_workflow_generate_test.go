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
