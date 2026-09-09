package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCompileIncrementalTargetsOnlyInsertedScenarioAndPrerequisites(t *testing.T) {
	repo, spec := newIncrementalFixture(t)
	baseline := mustCompileIncremental(t, spec, repo)
	recordSuccessfulImplementation(t, baseline, repo)

	writeTestFile(t, repo, "features/behavior.feature", behaviorFeature(`
  Scenario: New behavior
    When the new action runs
    Then the new result appears
`))
	plan := mustCompileIncremental(t, spec, repo)
	if got, want := taskIDs(plan.Tasks), []string{"prepare", "implement", "verify"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("incremental task order = %v, want dependency closure %v", got, want)
	}
	if len(plan.Tasks[0].Scenarios) != 0 {
		t.Fatalf("prerequisite scenarios = %#v, want none", plan.Tasks[0].Scenarios)
	}
	if got := scenarioNames(plan.Tasks[1].Scenarios); !reflect.DeepEqual(got, []string{"New behavior"}) {
		t.Fatalf("implementation targets = %v, want only inserted scenario", got)
	}
	if len(plan.Tasks[2].Scenarios) != 0 {
		t.Fatalf("verification scenarios = %#v, want none", plan.Tasks[2].Scenarios)
	}
	for _, unchanged := range []string{"Existing behavior", "Later behavior", "setup exists"} {
		if strings.Contains(plan.Tasks[1].Prompt, unchanged) {
			t.Fatalf("incremental prompt targets unchanged sibling %q:\n%s", unchanged, plan.Tasks[1].Prompt)
		}
	}
	for _, required := range []string{"New behavior", "shared precondition", "Behavior rule", "feature context", "@important"} {
		if !strings.Contains(plan.Tasks[1].Prompt, required) {
			t.Errorf("incremental prompt missing %q:\n%s", required, plan.Tasks[1].Prompt)
		}
	}
	if got, want := incrementalReasons(plan.Tracking.Tasks), map[string]string{"prepare": "prerequisite", "implement": "changed", "verify": "verification"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tracking reasons = %v, want %v", got, want)
	}
}

func TestCompileIncrementalSemanticInvalidation(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*testing.T, string, string)
		wantByTask map[string][]string
	}{
		{
			name:       "unchanged source is no-op",
			mutate:     func(*testing.T, string, string) {},
			wantByTask: map[string][]string{},
		},
		{
			name: "line insertion does not invalidate siblings",
			mutate: func(t *testing.T, repo, _ string) {
				writeTestFile(t, repo, "features/behavior.feature", "\n\n"+behaviorFeature(""))
			},
			wantByTask: map[string][]string{},
		},
		{
			name: "background change invalidates inheriting scenarios",
			mutate: func(t *testing.T, repo, _ string) {
				writeTestFile(t, repo, "features/behavior.feature", strings.Replace(behaviorFeature(""), "shared precondition", "changed shared precondition", 1))
			},
			wantByTask: map[string][]string{"prepare": {}, "implement": {"Existing behavior", "Later behavior"}, "verify": {}},
		},
		{
			name: "policy change conservatively invalidates all",
			mutate: func(t *testing.T, _ string, spec string) {
				content := readFile(t, spec)
				if err := os.WriteFile(spec, []byte(strings.Replace(content, "Implement selected behavior.", "Implement selected behavior carefully.", 1)), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantByTask: map[string][]string{"prepare": {"Prepare repository"}, "implement": {"Existing behavior", "Later behavior"}, "verify": {"Prepare repository"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, spec := newIncrementalFixture(t)
			baseline := mustCompileIncremental(t, spec, repo)
			recordSuccessfulImplementation(t, baseline, repo)
			tt.mutate(t, repo, spec)

			plan := mustCompileIncremental(t, spec, repo)
			got := make(map[string][]string)
			for _, task := range plan.Tasks {
				got[task.ID] = scenarioNames(task.Scenarios)
			}
			if !reflect.DeepEqual(got, tt.wantByTask) {
				t.Fatalf("targets = %#v, want %#v", got, tt.wantByTask)
			}
			if len(tt.wantByTask) == 0 && (plan.Tracking == nil || !plan.Tracking.NoOp || len(plan.Tasks) != 0) {
				t.Fatalf("unchanged plan is not an explicit no-op: %#v", plan)
			}
		})
	}
}

func TestCompileIncrementalOutlineRowsUseSemanticIdentity(t *testing.T) {
	repo, spec := newIncrementalFixtureWithBehavior(t, `@important
Feature: Outlines
  Scenario Outline: value <value>
    Given input <value>
    Then output <result>
    Examples: values
      | value | result |
      | one   | 1      |
      | two   | 2      |
`)
	baseline := mustCompileIncremental(t, spec, repo)
	recordSuccessfulImplementation(t, baseline, repo)
	writeTestFile(t, repo, "features/behavior.feature", `@important
Feature: Outlines
  Scenario Outline: value <value>
    Given input <value>
    Then output <result>
    Examples: values
      | value | result |
      | zero  | 0      |
      | one   | 1      |
      | two   | 2      |
`)
	plan := mustCompileIncremental(t, spec, repo)
	if got := scenarioNames(findTask(t, plan, "implement").Scenarios); !reflect.DeepEqual(got, []string{"value zero"}) {
		t.Fatalf("outline targets = %v, want only new row", got)
	}
}

func TestCompileIncrementalDoesNotReuseSupersededSemanticVersion(t *testing.T) {
	repo, spec := newIncrementalFixture(t)
	versionA := mustCompileIncremental(t, spec, repo)
	recordSuccessfulImplementation(t, versionA, repo)

	featurePath := filepath.Join(repo, "features", "behavior.feature")
	versionBSource := strings.Replace(behaviorFeature(""), "existing result appears", "version B result appears", 1)
	if err := os.WriteFile(featurePath, []byte(versionBSource), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "features/behavior.feature")
	gitRun(t, repo, "commit", "-q", "-m", "test: version B")
	versionB := mustCompileIncremental(t, spec, repo)
	recordSuccessfulImplementation(t, versionB, repo)

	if err := os.WriteFile(featurePath, []byte(behaviorFeature("")), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "features/behavior.feature")
	gitRun(t, repo, "commit", "-q", "-m", "test: restore version A")
	reverted := mustCompileIncremental(t, spec, repo)
	if got := scenarioNames(findTask(t, reverted, "implement").Scenarios); !reflect.DeepEqual(got, []string{"Existing behavior"}) {
		t.Fatalf("reverted targets = %v, want superseded scenario to rerun", got)
	}
}

func TestValidatePlanInputsDetectsSourceAndBaselineDrift(t *testing.T) {
	repo, spec := newIncrementalFixture(t)
	plan := mustCompileIncremental(t, spec, repo)
	if err := ValidatePlanInputs(context.Background(), plan, repo); err != nil {
		t.Fatalf("fresh plan inputs rejected: %v", err)
	}
	writeTestFile(t, repo, "features/behavior.feature", strings.Replace(behaviorFeature(""), "existing result", "drifted result", 1))
	if err := ValidatePlanInputs(context.Background(), plan, repo); err == nil || !strings.Contains(err.Error(), "no longer match") {
		t.Fatalf("source drift error = %v", err)
	}
	writeTestFile(t, repo, "features/behavior.feature", behaviorFeature(""))
	gitRun(t, repo, "commit", "-q", "--allow-empty", "-m", "test: move baseline")
	if err := ValidatePlanInputs(context.Background(), plan, repo); err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("baseline drift error = %v", err)
	}
}

func TestValidatePlanInputsRejectsExecutableProjectionMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Plan)
	}{
		{name: "plan version", mutate: func(plan *Plan) { plan.Version++ }},
		{name: "plan name", mutate: func(plan *Plan) { plan.Name += "-changed" }},
		{name: "run", mutate: func(plan *Plan) { findTaskPointer(t, plan, "prepare").Run[0] = "false" }},
		{name: "prompt", mutate: func(plan *Plan) { findTaskPointer(t, plan, "implement").Prompt += " Changed." }},
		{name: "needs", mutate: func(plan *Plan) { findTaskPointer(t, plan, "verify").Needs = nil }},
		{name: "attempts", mutate: func(plan *Plan) { findTaskPointer(t, plan, "implement").Attempts++ }},
		{name: "model", mutate: func(plan *Plan) { findTaskPointer(t, plan, "implement").Model = "changed-model" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, spec := newIncrementalFixture(t)
			plan, err := CompileFull(context.Background(), spec, repo)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(plan)
			if err := ValidatePlanInputs(context.Background(), plan, repo); err == nil || !strings.Contains(err.Error(), "projection") {
				t.Fatalf("ValidatePlanInputs() error = %v, want projection mutation rejection", err)
			}
		})
	}
}

func TestImplementationRegistryRejectsUntrustedResultsAndCorruption(t *testing.T) {
	t.Run("failed execution", func(t *testing.T) {
		repo, spec := newIncrementalFixture(t)
		plan := mustCompileIncremental(t, spec, repo)
		result := successfulResult(t, plan, repo)
		result.Tasks[0].Status = "failed"
		if err := RecordImplementation(context.Background(), plan, result, repo); err == nil || !strings.Contains(err.Error(), "successful") {
			t.Fatalf("RecordImplementation() error = %v, want failed-result rejection", err)
		}
	})

	t.Run("task commit outside integration", func(t *testing.T) {
		repo, spec := newIncrementalFixture(t)
		plan := mustCompileIncremental(t, spec, repo)
		result := successfulResult(t, plan, repo)
		result.Tasks[0].Commit = gitOutput(t, repo, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "outside")
		if err := RecordImplementation(context.Background(), plan, result, repo); err == nil || !strings.Contains(err.Error(), "not contained") {
			t.Fatalf("RecordImplementation() error = %v, want task containment rejection", err)
		}
	})

	t.Run("unmerged integration is recorded but not trusted until merged", func(t *testing.T) {
		repo, spec := newIncrementalFixture(t)
		plan := mustCompileIncremental(t, spec, repo)
		result := successfulResult(t, plan, repo)
		result.Commit = gitOutput(t, repo, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "unmerged")
		if err := RecordImplementation(context.Background(), plan, result, repo); err != nil {
			t.Fatalf("RecordImplementation() rejected valid unmerged integration: %v", err)
		}
		if got := mustCompileIncremental(t, spec, repo); len(got.Tasks) == 0 {
			t.Fatal("unmerged implementation record was trusted")
		}
		gitRun(t, repo, "merge", "-q", "--ff-only", result.Commit)
		if got := mustCompileIncremental(t, spec, repo); len(got.Tasks) != 0 || !got.Tracking.NoOp {
			t.Fatalf("merged implementation record was not trusted: %#v", got)
		}
	})

	t.Run("corrupt content addressed record", func(t *testing.T) {
		repo, spec := newIncrementalFixture(t)
		plan := mustCompileIncremental(t, spec, repo)
		recordSuccessfulImplementation(t, plan, repo)
		records := testRecordFiles(t, repo)
		if err := os.WriteFile(filepath.Join(filepath.Dir(records[0]), strings.Repeat("0", 64)+".json"), []byte("{broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := CompileIncremental(context.Background(), spec, repo); err == nil || !strings.Contains(err.Error(), "corrupt implementation record") {
			t.Fatalf("CompileIncremental() error = %v, want corrupt-state rejection", err)
		}
	})

	t.Run("malformed scenario metadata", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*implementationRecord)
		}{
			{name: "empty task assignments", mutate: func(record *implementationRecord) { record.Scenarios[0].Tasks = nil }},
			{name: "empty task ID", mutate: func(record *implementationRecord) { record.Scenarios[0].Tasks = []string{""} }},
			{name: "duplicate task assignments", mutate: func(record *implementationRecord) {
				record.Scenarios[0].Tasks = append(record.Scenarios[0].Tasks, record.Scenarios[0].Tasks[0])
			}},
			{name: "unsafe task assignment", mutate: func(record *implementationRecord) { record.Scenarios[0].Tasks = []string{"../task"} }},
			{name: "duplicate scenario IDs", mutate: func(record *implementationRecord) { record.Scenarios[1].ID = record.Scenarios[0].ID }},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				repo, spec := newIncrementalFixture(t)
				plan := mustCompileIncremental(t, spec, repo)
				recordSuccessfulImplementation(t, plan, repo)
				rewriteOnlyImplementationRecord(t, repo, tt.mutate)
				if _, err := CompileIncremental(context.Background(), spec, repo); err == nil || !strings.Contains(err.Error(), "invalid scenario metadata") {
					t.Fatalf("CompileIncremental() error = %v, want corrupt scenario metadata rejection", err)
				}
			})
		}
	})

	t.Run("changed task assignment is not reused", func(t *testing.T) {
		repo, spec := newIncrementalFixture(t)
		baseline := mustCompileIncremental(t, spec, repo)
		recordSuccessfulImplementation(t, baseline, repo)
		targetID := findTask(t, baseline, "implement").Scenarios[0].ID
		rewriteOnlyImplementationRecord(t, repo, func(record *implementationRecord) {
			for i := range record.Scenarios {
				if record.Scenarios[i].ID == targetID {
					record.Scenarios[i].Tasks = []string{"unknown"}
					return
				}
			}
			t.Fatal("implementation scenario missing from record")
		})
		changed := mustCompileIncremental(t, spec, repo)
		if got := scenarioNames(findTask(t, changed, "implement").Scenarios); !reflect.DeepEqual(got, []string{"Existing behavior"}) {
			t.Fatalf("targets = %v, want scenario with changed task assignment", got)
		}
	})
}

func TestRecordImplementationIsContentAddressedAndDoesNotDirtySource(t *testing.T) {
	repo, spec := newIncrementalFixture(t)
	plan := mustCompileIncremental(t, spec, repo)
	recordSuccessfulImplementation(t, plan, repo)
	recordSuccessfulImplementation(t, plan, repo)

	records := testRecordFiles(t, repo)
	if len(records) != 1 {
		t.Fatalf("registry entries = %d, want one immutable record", len(records))
	}
	content := []byte(readFile(t, records[0]))
	digest := sha256.Sum256(content)
	if got, want := filepath.Base(records[0]), hex.EncodeToString(digest[:])+".json"; got != want {
		t.Fatalf("record name = %q, want content address %q", got, want)
	}
	if got := gitOutput(t, repo, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("registry dirtied source repository: %s", got)
	}
}

func TestCompileFullAlwaysTargetsAllScenariosWithTracking(t *testing.T) {
	repo, spec := newIncrementalFixture(t)
	baseline := mustCompileIncremental(t, spec, repo)
	recordSuccessfulImplementation(t, baseline, repo)
	full, err := CompileFull(context.Background(), spec, repo)
	if err != nil {
		t.Fatal(err)
	}
	if full.Tracking == nil || full.Tracking.NoOp || len(full.Tasks) != 3 || len(full.Tracking.BaseRecords) != 0 {
		t.Fatalf("tracked full plan = %#v", full)
	}
	for _, task := range full.Tracking.Tasks {
		if task.Reason != "changed" || len(task.ScenarioKeys) == 0 {
			t.Fatalf("full task tracking = %#v", task)
		}
	}
}

func TestCompileFullIgnoresCorruptImplementationHistory(t *testing.T) {
	repo, spec := newIncrementalFixture(t)
	baseline := mustCompileIncremental(t, spec, repo)
	recordSuccessfulImplementation(t, baseline, repo)
	records := testRecordFiles(t, repo)
	if err := os.WriteFile(filepath.Join(filepath.Dir(records[0]), strings.Repeat("0", 64)+".json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	full, err := CompileFull(context.Background(), spec, repo)
	if err != nil {
		t.Fatalf("CompileFull() read implementation history: %v", err)
	}
	if full.Tracking == nil || full.Tracking.NoOp || len(full.Tracking.BaseRecords) != 0 {
		t.Fatalf("tracked full plan = %#v, want fresh tracking without base records", full)
	}
}

func TestCompileIncrementalVerificationIncludesAllPrerequisiteBranches(t *testing.T) {
	repo, spec := newIncrementalFixtureWithDiamond(t)
	baseline := mustCompileIncremental(t, spec, repo)
	recordSuccessfulImplementation(t, baseline, repo)

	changed := strings.Replace(readFile(t, filepath.Join(repo, "features", "behavior.feature")), "existing result appears", "changed result appears", 1)
	writeTestFile(t, repo, "features/behavior.feature", changed)
	plan := mustCompileIncremental(t, spec, repo)

	if got, want := taskIDs(plan.Tasks), []string{"prepare-a", "implement", "prepare-b", "gate"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("incremental task order = %v, want complete verification dependency closure %v", got, want)
	}
	included := make(map[string]bool, len(plan.Tasks))
	for _, task := range plan.Tasks {
		included[task.ID] = true
	}
	for _, task := range plan.Tasks {
		for _, need := range task.Needs {
			if !included[need] {
				t.Fatalf("task %q depends on omitted task %q", task.ID, need)
			}
		}
	}
	if err := ValidateTracking(plan); err != nil {
		t.Fatalf("ValidateTracking() error = %v", err)
	}
}

func TestRecordImplementationAfterPolicyChangeCarriesNoStaleEvidence(t *testing.T) {
	repo, spec := newIncrementalFixture(t)
	baseline := mustCompileIncremental(t, spec, repo)
	recordSuccessfulImplementation(t, baseline, repo)
	content := strings.Replace(readFile(t, spec), "Implement selected behavior.", "Implement selected behavior carefully.", 1)
	if err := os.WriteFile(spec, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "workflow.yaml")
	gitRun(t, repo, "commit", "-q", "-m", "test: update policy")
	changed := mustCompileIncremental(t, spec, repo)
	if len(changed.Tracking.BaseRecords) != 0 {
		t.Fatalf("policy change carried stale evidence: %v", changed.Tracking.BaseRecords)
	}
	recordSuccessfulImplementation(t, changed, repo)
}

func TestLegacyCompileOmitsTrackingMetadata(t *testing.T) {
	repo, spec := newIncrementalFixture(t)
	plan, err := Compile(spec)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tracking != nil || strings.Contains(string(encoded), "tracking") {
		t.Fatalf("legacy Compile output changed: %s", encoded)
	}
	_ = repo
}

func newIncrementalFixture(t *testing.T) (string, string) {
	t.Helper()
	return newIncrementalFixtureWithBehavior(t, behaviorFeature(""))
}

func newIncrementalFixtureWithBehavior(t *testing.T, behavior string) (string, string) {
	t.Helper()
	repo := newTestRepository(t)
	writeTestFile(t, repo, "features/setup.feature", `Feature: Setup
  Scenario: Prepare repository
    Given setup exists
`)
	writeTestFile(t, repo, "features/behavior.feature", behavior)
	spec := writeTestFile(t, repo, "workflow.yaml", `version: 1
name: incremental
model: codex
features: [features/*.feature]
tasks:
  - id: prepare
    features: [features/setup.feature]
    run: [true]
  - id: implement
    needs: [prepare]
    features: [features/behavior.feature]
    prompt: Implement selected behavior.
  - id: verify
    needs: [implement]
    features: [features/setup.feature]
    run: [true]
`)
	gitRun(t, repo, "add", "workflow.yaml", "features")
	gitRun(t, repo, "commit", "-q", "-m", "test: add workflow")
	return repo, spec
}

func newIncrementalFixtureWithDiamond(t *testing.T) (string, string) {
	t.Helper()
	repo := newTestRepository(t)
	writeTestFile(t, repo, "features/setup.feature", `Feature: Setup
  Scenario: Prepare repository
    Given setup exists
`)
	writeTestFile(t, repo, "features/behavior.feature", behaviorFeature(""))
	spec := writeTestFile(t, repo, "workflow.yaml", `version: 1
name: incremental-diamond
model: codex
features: [features/*.feature]
tasks:
  - id: prepare-a
    features: [features/setup.feature]
    run: [true]
  - id: implement
    needs: [prepare-a]
    features: [features/behavior.feature]
    prompt: Implement selected behavior.
  - id: prepare-b
    features: [features/setup.feature]
    run: [true]
  - id: gate
    needs: [implement, prepare-b]
    features: [features/setup.feature]
    run: [true]
`)
	gitRun(t, repo, "add", "workflow.yaml", "features")
	gitRun(t, repo, "commit", "-q", "-m", "test: add diamond workflow")
	return repo, spec
}

func behaviorFeature(extra string) string {
	return `@important
Feature: Behaviors
  feature context

  Background:
    Given shared precondition

  @rule
  Rule: Behavior rule
    rule context

    Scenario: Existing behavior
      When existing action runs
      Then existing result appears
` + extra + `
    Scenario: Later behavior
      When later action runs
      Then later result appears
`
}

func mustCompileIncremental(t *testing.T, spec, repo string) *Plan {
	t.Helper()
	plan, err := CompileIncremental(context.Background(), spec, repo)
	if err != nil {
		t.Fatalf("CompileIncremental() error = %v", err)
	}
	return plan
}

func recordSuccessfulImplementation(t *testing.T, plan *Plan, repo string) {
	t.Helper()
	if err := RecordImplementation(context.Background(), plan, successfulResult(t, plan, repo), repo); err != nil {
		t.Fatalf("RecordImplementation() error = %v", err)
	}
}

func successfulResult(t *testing.T, plan *Plan, repo string) *Result {
	t.Helper()
	results := make([]TaskResult, len(plan.Tasks))
	for i, task := range plan.Tasks {
		results[i] = TaskResult{ID: task.ID, Status: "success", Commit: gitOutput(t, repo, "rev-parse", "HEAD")}
	}
	return &Result{Baseline: gitOutput(t, repo, "rev-parse", "HEAD"), Tasks: results, Commit: gitOutput(t, repo, "rev-parse", "HEAD")}
}

func testRecordFiles(t *testing.T, repo string) []string {
	t.Helper()
	root := filepath.Join(testGitCommonDir(t, repo), "godog", "implementations")
	var records []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			records = append(records, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(records)
	return records
}

func scenarioNames(scenarios []Scenario) []string {
	result := make([]string, len(scenarios))
	for i := range scenarios {
		result[i] = scenarios[i].Name
	}
	return result
}

func incrementalReasons(tasks []IncrementalTask) map[string]string {
	result := make(map[string]string, len(tasks))
	for _, task := range tasks {
		result[task.ID] = task.Reason
	}
	return result
}

func findTask(t *testing.T, plan *Plan, id string) Task {
	t.Helper()
	for _, task := range plan.Tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("task %q not found", id)
	return Task{}
}

func findTaskPointer(t *testing.T, plan *Plan, id string) *Task {
	t.Helper()
	for i := range plan.Tasks {
		if plan.Tasks[i].ID == id {
			return &plan.Tasks[i]
		}
	}
	t.Fatalf("task %q not found", id)
	return nil
}

func rewriteOnlyImplementationRecord(t *testing.T, repo string, mutate func(*implementationRecord)) {
	t.Helper()
	records := testRecordFiles(t, repo)
	if len(records) != 1 {
		t.Fatalf("registry entries = %d, want one", len(records))
	}
	var record implementationRecord
	if err := json.Unmarshal([]byte(readFile(t, records[0])), &record); err != nil {
		t.Fatal(err)
	}
	mutate(&record)
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	if err := os.Remove(records[0]); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(records[0]), semanticBytesHash(content)+".json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testGitCommonDir(t *testing.T, repo string) string {
	t.Helper()
	value := gitOutput(t, repo, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(value) {
		value = filepath.Join(repo, value)
	}
	return filepath.Clean(value)
}
