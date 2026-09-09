package workflow

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCompileDeterministicRichGherkinPlan(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "features/accounts.feature", `@accounts
Feature: Account limits
  Rich feature description.

  Background:
    Given an account exists

  Rule: Transfers respect limits
    Rule description.

    Scenario Outline: Transfer <amount>
      When I transfer <amount>
      Then the audit contains:
        | field  | value    |
        | amount | <amount> |
      And the report is:
        """json
        {"amount": <amount>}
        """

      Examples:
        | amount |
        | 10     |
        | 20     |
`)
	writeTestFile(t, dir, "features/health.feature", `Feature: Health
  Scenario: Ready
    Then the service is ready
`)
	spec := writeTestFile(t, dir, "workflow.yaml", `version: 1
name: build
model: gpt-5-codex
features:
  - ./features/*.feature
tasks:
  - id: final
    needs: [run-check, implement]
    prompt: Review the result.
  - id: run-check
    features: [features/health.feature]
    run: [go, test, ./...]
    expected_exit: 2
    timeout: 45s
  - id: implement
    features: [./features/accounts*.feature]
    prompt: Implement the behavior.
    attempts: 2
`)

	first, err := Compile(spec)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	second, err := Compile(spec)
	if err != nil {
		t.Fatalf("second Compile() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Compile() is not deterministic:\nfirst: %#v\nsecond: %#v", first, second)
	}

	if got, want := taskIDs(first.Tasks), []string{"implement", "run-check", "final"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("task order = %v, want %v", got, want)
	}
	implement := first.Tasks[0]
	if implement.Model != "gpt-5-codex" || implement.Attempts != 2 {
		t.Fatalf("resolved prompt policy = model %q, attempts %d", implement.Model, implement.Attempts)
	}
	if len(implement.Scenarios) != 2 {
		t.Fatalf("outline scenarios = %d, want 2", len(implement.Scenarios))
	}
	if implement.Scenarios[0].ID == "" || implement.Scenarios[0].ID == implement.Scenarios[1].ID {
		t.Fatalf("scenario IDs are not stable unique IDs: %#v", implement.Scenarios)
	}
	steps := strings.Join(implement.Scenarios[0].Steps, "\n")
	for _, fragment := range []string{
		"Given an account exists",
		"When I transfer 10",
		"| amount | 10 |",
		`{"amount": 10}`,
	} {
		if !strings.Contains(steps, fragment) {
			t.Errorf("expanded scenario steps missing %q:\n%s", fragment, steps)
		}
	}
	for _, fragment := range []string{"@accounts", "Rich feature description.", "Rule description.", "Examples:", "Background:"} {
		if !strings.Contains(implement.Prompt, fragment) {
			t.Errorf("compiled prompt missing source semantics %q", fragment)
		}
	}
	if !strings.HasPrefix(implement.Prompt, "Implement the behavior.") || strings.Contains(implement.Prompt, "Implement all selected scenarios") {
		t.Fatalf("compiled prompt changed task intent: %q", implement.Prompt)
	}
	if got := first.Tasks[2].Prompt; !strings.HasPrefix(got, "Review the result.") || strings.Contains(got, "Implement all selected scenarios") {
		t.Fatalf("review prompt contains an implementation directive: %q", got)
	}
	runCheck := first.Tasks[1]
	if runCheck.Attempts != 1 || runCheck.ExpectExit != 2 || runCheck.Timeout != "45s" || runCheck.Model != "" {
		t.Fatalf("resolved run policy = %#v", runCheck.TaskSpec)
	}
	if len(runCheck.Scenarios) != 1 || runCheck.Scenarios[0].Name != "Ready" {
		t.Fatalf("run task scenarios = %#v", runCheck.Scenarios)
	}
	if got := len(first.Tasks[2].Scenarios); got != 3 {
		t.Fatalf("omitted task feature selectors selected %d scenarios, want all 3", got)
	}
}

func TestCompileRejectsInvalidSpecifications(t *testing.T) {
	validFeature := "Feature: Valid\n  Scenario: Works\n    Given it works\n"
	tests := []struct {
		name       string
		spec       string
		feature    string
		wantErr    string
		noFeature  bool
		extraFiles map[string]string
	}{
		{name: "unknown field", spec: baseSpec("mystery: true\n"), feature: validFeature, wantErr: "field mystery not found"},
		{name: "multiple yaml documents", spec: baseSpec("---\nversion: 1\n"), feature: validFeature, wantErr: "multiple YAML documents"},
		{name: "wrong version", spec: strings.Replace(baseSpec(""), "version: 1", "version: 2", 1), feature: validFeature, wantErr: "version must be 1"},
		{name: "no matching global features", spec: baseSpec(""), noFeature: true, wantErr: "matched no files"},
		{name: "one unmatched global feature glob", spec: strings.Replace(baseSpec(""), "features: [features/*.feature]", "features: [features/*.feature, missing/*.feature]", 1), feature: validFeature, wantErr: `feature glob "missing/*.feature" matched no files`},
		{name: "gherkin parse error", spec: baseSpec(""), feature: "# language: definitely-not\nFeature: Broken\n", wantErr: "parse feature"},
		{name: "empty scenarios", spec: baseSpec(""), feature: "Feature: Empty\n", wantErr: "contains no scenarios"},
		{name: "scenario without steps", spec: baseSpec(""), feature: "Feature: Empty scenario\n  Scenario: Does nothing\n", wantErr: "contains no steps"},
		{name: "empty tasks", spec: strings.Replace(baseSpec(""), "  - id: task\n    run: [true]\n", "", 1), feature: validFeature, wantErr: "tasks are empty"},
		{name: "empty task id", spec: strings.Replace(baseSpec(""), "id: task", "id: ''", 1), feature: validFeature, wantErr: "task id is empty"},
		{name: "unsafe task id", spec: strings.Replace(baseSpec(""), "id: task", "id: ../task", 1), feature: validFeature, wantErr: "safe path component"},
		{name: "duplicate task id", spec: baseSpec("  - id: task\n    run: [true]\n"), feature: validFeature, wantErr: "duplicate task id"},
		{name: "missing action", spec: strings.Replace(baseSpec(""), "    run: [true]\n", "", 1), feature: validFeature, wantErr: "exactly one of run or prompt"},
		{name: "both actions", spec: strings.Replace(baseSpec(""), "    run: [true]", "    run: [true]\n    prompt: hello", 1), feature: validFeature, wantErr: "exactly one of run or prompt"},
		{name: "empty argv", spec: strings.Replace(baseSpec(""), "run: [true]", "run: ['']", 1), feature: validFeature, wantErr: "argv command is empty"},
		{name: "prompt without model", spec: strings.Replace(strings.Replace(baseSpec(""), "model: codex\n", "", 1), "run: [true]", "prompt: do it", 1), feature: validFeature, wantErr: "requires a model"},
		{name: "explicit zero attempts", spec: strings.Replace(baseSpec(""), "run: [true]", "run: [true]\n    attempts: 0", 1), feature: validFeature, wantErr: "attempts must be between 1 and 5"},
		{name: "attempts too high", spec: strings.Replace(baseSpec(""), "run: [true]", "run: [true]\n    attempts: 6", 1), feature: validFeature, wantErr: "attempts must be between 1 and 5"},
		{name: "invalid expected exit", spec: strings.Replace(baseSpec(""), "run: [true]", "run: [true]\n    expected_exit: 256", 1), feature: validFeature, wantErr: "expected_exit must be between 0 and 255"},
		{name: "prompt expected exit", spec: strings.Replace(baseSpec(""), "run: [true]", "prompt: do it\n    expected_exit: 1", 1), feature: validFeature, wantErr: "expected_exit is only valid for run tasks"},
		{name: "invalid timeout", spec: strings.Replace(baseSpec(""), "run: [true]", "run: [true]\n    timeout: forever", 1), feature: validFeature, wantErr: "invalid timeout"},
		{name: "nonpositive timeout", spec: strings.Replace(baseSpec(""), "run: [true]", "run: [true]\n    timeout: 0s", 1), feature: validFeature, wantErr: "timeout must be positive"},
		{name: "unknown dependency", spec: strings.Replace(baseSpec(""), "run: [true]", "needs: [missing]\n    run: [true]", 1), feature: validFeature, wantErr: "unknown task"},
		{name: "dependency cycle", spec: strings.Replace(baseSpec(""), "  - id: task\n    run: [true]", "  - id: one\n    needs: [two]\n    run: [true]\n  - id: two\n    needs: [one]\n    run: [true]", 1), feature: validFeature, wantErr: "dependency cycle"},
		{name: "selector outside global set", spec: strings.Replace(baseSpec(""), "run: [true]", "features: [other/*.feature]\n    run: [true]", 1), feature: validFeature, extraFiles: map[string]string{"other/other.feature": validFeature}, wantErr: "matched no global feature files"},
		{name: "uncovered scenario", spec: strings.Replace(strings.Replace(baseSpec(""), "features: [features/*.feature]", "features: [features/*.feature, other/*.feature]", 1), "run: [true]", "features: [features/*.feature]\n    run: [true]", 1), feature: validFeature, extraFiles: map[string]string{"other/other.feature": validFeature}, wantErr: "is not assigned to any task"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tt.noFeature {
				writeTestFile(t, dir, "features/test.feature", tt.feature)
			}
			for path, content := range tt.extraFiles {
				writeTestFile(t, dir, path, content)
			}
			spec := writeTestFile(t, dir, "workflow.yaml", tt.spec)
			_, err := Compile(spec)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Compile() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func baseSpec(extra string) string {
	return `version: 1
name: test
model: codex
features: [features/*.feature]
tasks:
  - id: task
    run: [true]
` + extra
}

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func taskIDs(tasks []Task) []string {
	ids := make([]string, len(tasks))
	for i := range tasks {
		ids[i] = tasks[i].ID
	}
	return ids
}

func TestCompileRejectsZeroAttemptsThroughYAMLMerge(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "sample.feature", "Feature: merged policy\n Scenario: one\n  Given a requirement\n")
	spec := writeTestFile(t, dir, "workflow.yaml", `version: 1
features: [sample.feature]
<<:
  tasks:
    - id: check
      run: [true]
      attempts: 0
`)
	if _, err := Compile(spec); err == nil || !strings.Contains(err.Error(), "attempts must be between 1 and 5") {
		t.Fatalf("merged explicit zero attempts must fail, got %v", err)
	}
}
