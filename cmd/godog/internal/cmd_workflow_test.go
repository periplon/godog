package internal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowPlanCLI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.feature"), []byte("Feature: sample\n  Scenario: one\n    Given an outcome\n"), 0600); err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(dir, "workflow.yaml")
	if err := os.WriteFile(spec, []byte("version: 1\nname: sample\nfeatures: [sample.feature]\ntasks:\n  - id: check\n    run: [echo, hello]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var first []byte
	for i := 0; i < 2; i++ {
		cmd := CreateWorkflowCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"plan", spec})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
			t.Fatalf("invalid JSON: %s: %v", out.String(), err)
		}
		if parsed["name"] != "sample" {
			t.Fatalf("wrong plan: %s", out.String())
		}
		if i == 0 {
			first = append([]byte(nil), out.Bytes()...)
		} else if !bytes.Equal(first, out.Bytes()) {
			t.Fatal("plan output is nondeterministic")
		}
	}
}

func TestWorkflowCLIRejectsArguments(t *testing.T) {
	for _, args := range [][]string{{"plan"}, {"run"}, {"plan", "one", "two"}, {"run", "one", "--jobs", "0"}} {
		cmd := CreateWorkflowCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}

func TestWorkflowRunCLIReportsResultAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			dir := t.TempDir()
			git := func(args ...string) string {
				c := exec.Command("git", args...)
				c.Dir = dir
				c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
				b, err := c.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %s: %v", args, b, err)
				}
				return strings.TrimSpace(string(b))
			}
			git("init")
			git("config", "user.name", "Test")
			git("config", "user.email", "test@example.invalid")
			if err := os.WriteFile(filepath.Join(dir, "sample.feature"), []byte("Feature: sample\n Scenario: one\n  Given an outcome\n"), 0600); err != nil {
				t.Fatal(err)
			}
			argv := "[git, status, --porcelain]"
			if fail {
				argv = "[git, nonexistent-godog-test-command]"
			}
			spec := filepath.Join(dir, "workflow.yaml")
			if err := os.WriteFile(spec, []byte("version: 1\nname: cli-run\nfeatures: [sample.feature]\ntasks:\n - id: check\n   run: "+argv+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", ".")
			git("commit", "-m", "initial")
			before := git("rev-parse", "HEAD")
			cmd := CreateWorkflowCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&bytes.Buffer{})
			outputDir := filepath.Join(t.TempDir(), "run")
			cmd.SetArgs([]string{"run", spec, "--repo", dir, "--output-dir", outputDir})
			err := cmd.Execute()
			if (err != nil) != fail {
				t.Fatalf("failure=%v got %v: %s", fail, err, out.String())
			}
			var result struct {
				Baseline string `json:"baseline"`
				Tasks    []struct {
					Status string `json:"status"`
				} `json:"tasks"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("result is not JSON: %v: %s", err, out.String())
			}
			if result.Baseline != before || len(result.Tasks) != 1 {
				t.Fatalf("incorrect result: %s", out.String())
			}
			if after := git("rev-parse", "HEAD"); after != before {
				t.Fatal("caller HEAD changed")
			}
			if git("status", "--porcelain") != "" {
				t.Fatal("caller checkout modified")
			}
		})
	}
}

func TestBuiltWorkflowCLIEmitsJSONAndReturnsNonzeroOnTaskFailure(t *testing.T) {
	repoRootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := strings.TrimSpace(string(repoRootBytes))
	binary := filepath.Join(t.TempDir(), "godog")
	build := exec.Command("go", "build", "-o", binary, "./cmd/godog")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build godog: %v\n%s", err, output)
	}

	for _, test := range []struct {
		name     string
		run      string
		wantFail bool
	}{
		{name: "success", run: "[git, status, --porcelain]"},
		{name: "failure", run: "[git, nonexistent-godog-test-command]", wantFail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			git := func(args ...string) {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, output)
				}
			}
			git("init", "-q")
			git("config", "user.name", "Test")
			git("config", "user.email", "test@example.invalid")
			if err := os.WriteFile(filepath.Join(dir, "sample.feature"), []byte("Feature: sample\n Scenario: one\n  Given an outcome\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			spec := filepath.Join(dir, "workflow.yaml")
			contents := "version: 1\nname: built-cli\nfeatures: [sample.feature]\ntasks:\n - id: check\n   run: " + test.run + "\n"
			if err := os.WriteFile(spec, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			git("add", ".")
			git("commit", "-q", "-m", "test: initial")

			cmd := exec.Command(binary, "workflow", "run", spec, "--repo", dir, "--output-dir", filepath.Join(t.TempDir(), "run"))
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			runErr := cmd.Run()
			if test.wantFail {
				var exitErr *exec.ExitError
				if !errors.As(runErr, &exitErr) || exitErr.ExitCode() == 0 {
					t.Fatalf("failure exit = %v, stderr = %s", runErr, stderr.String())
				}
			} else if runErr != nil {
				t.Fatalf("success run: %v, stderr = %s", runErr, stderr.String())
			}
			var result struct {
				Tasks []struct {
					Status string `json:"status"`
				} `json:"tasks"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
			}
			if len(result.Tasks) != 1 {
				t.Fatalf("unexpected result: %s", stdout.String())
			}
			wantStatus := "success"
			if test.wantFail {
				wantStatus = "failed"
			}
			if result.Tasks[0].Status != wantStatus {
				t.Fatalf("task status = %q, want %q", result.Tasks[0].Status, wantStatus)
			}
		})
	}
}
