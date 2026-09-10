package workflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileReasoningEffortInheritance(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh"} {
		t.Run(effort, func(t *testing.T) {
			dir := t.TempDir()
			writeTestFile(t, dir, "features/a.feature", "Feature: A\n  Scenario: A\n    Given a thing\n")
			spec := "version: 1\nname: effort\nmodel: codex\nreasoning_effort: " + effort + "\nfeatures: [features/*.feature]\ntasks:\n  - id: inherited\n    prompt: Implement\n  - id: override\n    prompt: Review\n    reasoning_effort: low\n  - id: shell\n    run: [true]\n"
			plan, err := Compile(writeTestFile(t, dir, "workflow.yaml", spec))
			if err != nil {
				t.Fatal(err)
			}
			for _, task := range plan.Tasks {
				content, _ := json.Marshal(task)
				var fields map[string]interface{}
				json.Unmarshal(content, &fields)
				want := effort
				if task.ID == "override" {
					want = "low"
				}
				if task.ID == "shell" {
					if fields["reasoning_effort"] != nil {
						t.Fatal("shell inherited reasoning effort")
					}
					continue
				}
				if fields["reasoning_effort"] != want {
					t.Fatalf("task %s effort = %v, want %s", task.ID, fields["reasoning_effort"], want)
				}
			}
		})
	}
}

func TestCompileRejectsInvalidReasoningEffort(t *testing.T) {
	for _, extra := range []string{"reasoning_effort: turbo\n", "    reasoning_effort: turbo\n"} {
		dir := t.TempDir()
		writeTestFile(t, dir, "features/a.feature", "Feature: A\n  Scenario: A\n    Given a thing\n")
		_, err := Compile(writeTestFile(t, dir, "workflow.yaml", baseSpec(extra)))
		if err == nil || !strings.Contains(err.Error(), "reasoning_effort") {
			t.Fatalf("error = %v", err)
		}
	}
}

func effortPlan(t *testing.T, effort string) *Plan {
	t.Helper()
	var plan Plan
	if err := json.Unmarshal([]byte(`{"version":1,"name":"effort","tasks":[{"id":"task","model":"m","prompt":"work","attempts":1,"reasoning_effort":"`+effort+`"}]}`), &plan); err != nil {
		t.Fatal(err)
	}
	return &plan
}

func TestValidatePlanRejectsInvalidReasoningEffort(t *testing.T) {
	if err := validatePlan(effortPlan(t, "turbo")); err == nil || !strings.Contains(err.Error(), "reasoning_effort") {
		t.Fatalf("error = %v", err)
	}
}

func TestReasoningEffortChangesFingerprints(t *testing.T) {
	low, _ := json.Marshal(effortPlan(t, "low"))
	high, _ := json.Marshal(effortPlan(t, "high"))
	if digestBytes(low) == digestBytes(high) {
		t.Fatal("resume digest ignores reasoning effort")
	}
	a, _ := projectionFingerprint(effortPlan(t, "low"))
	b, _ := projectionFingerprint(effortPlan(t, "high"))
	if a == b {
		t.Fatal("incremental projection ignores reasoning effort")
	}
}

func TestRunAttemptPassesReasoningEffort(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh"} {
		t.Run(effort, func(t *testing.T) {
			dir := t.TempDir()
			codex := writeExecutable(t, "#!/bin/sh\nprintf '%s\\n' \"$@\" > argv\n")
			_, _, err := runAttempt(context.Background(), dir, filepath.Join(dir, "log"), effortPlan(t, effort).Tasks[0], codex, "the prompt", 1)
			if err != nil {
				t.Fatal(err)
			}
			got := readFile(t, filepath.Join(dir, "argv"))
			want := "exec\n-m\nm\n-c\nmodel_reasoning_effort=\"" + effort + "\"\n--dangerously-bypass-approvals-and-sandbox\n--\nthe prompt\n"
			if got != want {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}

func TestResumePreservesReasoningEffortAndRejectsMutation(t *testing.T) {
	repo := newTestRepository(t)
	records := t.TempDir()
	t.Setenv("EFFORT_RECORDS", records)
	codex := writeExecutable(t, `#!/bin/sh
printf '%s\n' "$@" >> "$EFFORT_RECORDS/argv"
if [ ! -f "$EFFORT_RECORDS/once" ]; then touch "$EFFORT_RECORDS/once"; exit 8; fi
printf complete > complete.txt
`)
	output := filepath.Join(t.TempDir(), "run")
	if _, err := Execute(context.Background(), effortPlan(t, "xhigh"), RunOptions{Dir: repo, OutputDir: output, CodexBinary: codex}); err == nil {
		t.Fatal("initial run must fail")
	}
	if _, err := Resume(context.Background(), output, RunOptions{Dir: repo, CodexBinary: codex}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(readFile(t, filepath.Join(records, "argv")), `model_reasoning_effort="xhigh"`); got != 2 {
		t.Fatalf("effort passed %d times, want 2", got)
	}
	path := filepath.Join(output, "plan.json")
	contents := readFile(t, path)
	modified := strings.Replace(contents, `"reasoning_effort": "xhigh"`, `"reasoning_effort": "low"`, 1)
	if modified == contents {
		t.Fatal("durable plan missing effort")
	}
	writeTestFile(t, output, "plan.json", modified)
	if _, err := Resume(context.Background(), output, RunOptions{Dir: repo, CodexBinary: codex}); err == nil || !strings.Contains(err.Error(), "plan") {
		t.Fatalf("mutated effort error = %v", err)
	}
}

func TestUnsetReasoningEffortPreservesSerializedPolicy(t *testing.T) {
	plan := effortPlan(t, "")
	content, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "reasoning_effort") {
		t.Fatalf("unset effort changes durable plan: %s", content)
	}
	content, err = json.Marshal(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "reasoning_effort") {
		t.Fatalf("unset effort changes policy fingerprint: %s", content)
	}
}
