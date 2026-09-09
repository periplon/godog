package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog/workflow"
)

func cliGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func cliFixture(t *testing.T, tasks string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	cliGit(t, dir, "init", "-q")
	cliGit(t, dir, "config", "user.name", "Test")
	cliGit(t, dir, "config", "user.email", "test@example.invalid")
	for name, contents := range map[string]string{
		"sample.feature": "Feature: sample\n Scenario: one\n  Given an outcome\n",
		"workflow.yaml":  "version: 1\nname: incremental-cli\nfeatures: [sample.feature]\ntasks:\n" + tasks,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cliGit(t, dir, "add", ".")
	cliGit(t, dir, "commit", "-qm", "test: initial")
	return dir, filepath.Join(dir, "workflow.yaml")
}

func builtWorkflowInvoker(t *testing.T) func(*testing.T, ...string) ([]byte, error) {
	t.Helper()
	repoRoot := cliGit(t, ".", "rev-parse", "--show-toplevel")
	binary := filepath.Join(t.TempDir(), "godog.exe")
	build := exec.Command("go", "build", "-o", binary, "./cmd/godog")
	build.Dir = repoRoot
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, b)
	}
	return func(t *testing.T, args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.Command(binary, append([]string{"workflow"}, args...)...)
		var out, diagnostic bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &diagnostic
		err := cmd.Run()
		if err != nil {
			err = fmt.Errorf("%w: %s", err, diagnostic.String())
		}
		return out.Bytes(), err
	}
}

func TestWorkflowCLIIncrementalDefaultAndFullOverride(t *testing.T) {
	invokeWorkflow := builtWorkflowInvoker(t)
	dir, spec := cliFixture(t, " - id: verify\n   run: [git, status, --porcelain]\n")
	runDir := filepath.Join(t.TempDir(), "execution")
	output, err := invokeWorkflow(t, "run", spec, "--repo", dir, "--output-dir", runDir, "--full")
	if err != nil {
		t.Fatalf("initial run: %v: %s", err, output)
	}
	var result workflow.Result
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	cliGit(t, dir, "merge", "--ff-only", result.Commit)
	for _, full := range []bool{false, true} {
		args := []string{"plan", spec, "--repo", dir}
		if full {
			args = append(args, "--full")
		}
		output, err := invokeWorkflow(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan workflow.Plan
		if err := json.Unmarshal(output, &plan); err != nil {
			t.Fatal(err)
		}
		want := 0
		if full {
			want = 1
		}
		if len(plan.Tasks) != want {
			t.Fatalf("full=%v tasks=%d want=%d: %s", full, len(plan.Tasks), want, output)
		}
	}

	noOpDir := filepath.Join(t.TempDir(), "no-op")
	output, err = invokeWorkflow(t, "run", spec, "--repo", dir, "--output-dir", noOpDir)
	if err != nil {
		t.Fatalf("no-op run: %v: %s", err, output)
	}
	var noOp workflow.Result
	if err := json.Unmarshal(output, &noOp); err != nil {
		t.Fatal(err)
	}
	if len(noOp.Tasks) != 0 {
		t.Fatalf("unchanged run executed tasks: %s", output)
	}
	output, err = invokeWorkflow(t, "resume", noOpDir)
	if err != nil {
		t.Fatalf("no-op resume: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample.feature"), []byte("Feature: sample\n Scenario: new\n  Given a different outcome\n Scenario: one\n  Given an outcome\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cliGit(t, dir, "add", ".")
	cliGit(t, dir, "commit", "-qm", "test: add scenario")
	output, err = invokeWorkflow(t, "plan", spec, "--repo", dir)
	if err != nil {
		t.Fatal(err)
	}
	var plan workflow.Plan
	if err := json.Unmarshal(output, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Tasks) != 1 || len(plan.Tasks[0].Scenarios) != 1 || plan.Tasks[0].Scenarios[0].Name != "new" {
		t.Fatalf("did not select only inserted scenario: %s", output)
	}
}

func TestWorkflowCLIResumeRetriesOnlyUnfinishedSteps(t *testing.T) {
	invokeWorkflow := builtWorkflowInvoker(t)
	external := t.TempDir()
	first := filepath.Join(external, "first")
	second := filepath.Join(external, "second")
	gate := filepath.Join(external, "gate")
	t.Setenv("GODOG_CLI_RESUME_HELPER", "1")
	argv := func(marker, gate string) string {
		b, _ := json.Marshal([]string{os.Args[0], "-test.run=^TestWorkflowCLIResumeHelper$", "--", marker, gate})
		return string(b)
	}
	tasks := fmt.Sprintf(" - id: first\n   run: %s\n - id: second\n   needs: [first]\n   run: %s\n", argv(first, ""), argv(second, gate))
	dir, spec := cliFixture(t, tasks)
	runDir := filepath.Join(external, "execution")
	output, err := invokeWorkflow(t, "run", spec, "--repo", dir, "--output-dir", runDir)
	if err == nil {
		t.Fatalf("expected gated failure: %s", output)
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	output, err = invokeWorkflow(t, "resume", runDir)
	if err != nil {
		t.Fatalf("resume: %v: %s", err, output)
	}
	for marker, want := range map[string]int{first: 1, second: 2} {
		b, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != want {
			t.Fatalf("attempts for %s=%d want=%d", marker, len(b), want)
		}
	}
	output, err = invokeWorkflow(t, "resume", runDir)
	if err != nil {
		t.Fatalf("completed resume: %v: %s", err, output)
	}
	b, _ := os.ReadFile(second)
	if len(b) != 2 {
		t.Fatal("completed resume repeated side effects")
	}
}

func TestWorkflowCLIResumeHelper(t *testing.T) {
	if os.Getenv("GODOG_CLI_RESUME_HELPER") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) != 2 {
		os.Exit(90)
	}
	f, err := os.OpenFile(args[0], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(91)
	}
	if _, err = f.WriteString("x"); err != nil {
		os.Exit(92)
	}
	if f.Close() != nil {
		os.Exit(93)
	}
	if args[1] != "" {
		if _, err := os.Stat(args[1]); err != nil {
			os.Exit(7)
		}
	}
	os.Exit(0)
}
