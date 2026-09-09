package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/cucumber/godog/workflow"
)

type compilerAcceptance struct {
	dir              string
	specPath         string
	sentinelPath     string
	wantCompileError string
	plans            []*workflow.Plan
	planBytes        [][]byte
	compileErr       error
}

func TestCompilerAcceptance(t *testing.T) {
	world := &compilerAcceptance{}
	suite := godog.TestSuite{
		Name:                "workflow compiler acceptance",
		ScenarioInitializer: world.initializeScenario,
		Options: &godog.Options{
			Format:   "progress",
			Paths:    []string{"features/compiler.feature"},
			Strict:   true,
			TestingT: t,
		},
	}

	if status := suite.Run(); status != 0 {
		t.Fatalf("compiler acceptance suite failed with status %d", status)
	}
}

func (w *compilerAcceptance) initializeScenario(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		dir, err := os.MkdirTemp("", "godog-workflow-acceptance-")
		if err != nil {
			return ctx, fmt.Errorf("create acceptance fixture directory: %w", err)
		}
		*w = compilerAcceptance{
			dir:          dir,
			sentinelPath: filepath.Join(dir, "task-command-executed"),
		}
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, scenarioErr error) (context.Context, error) {
		cleanupErr := os.RemoveAll(w.dir)
		if scenarioErr != nil {
			return ctx, scenarioErr
		}
		if cleanupErr != nil {
			return ctx, fmt.Errorf("remove acceptance fixture directory: %w", cleanupErr)
		}
		return ctx, nil
	})

	sc.Step(`^a workflow policy and Gherkin feature files$`, w.aWorkflowPolicyAndGherkinFeatureFiles)
	sc.Step(`^features containing backgrounds, rules, outlines, tables and doc strings$`, w.richGherkinFeatures)
	sc.Step(`^a workflow policy with (.+)$`, w.aWorkflowPolicyWithDefect)
	sc.Step(`^the workflow is compiled twice$`, w.theWorkflowIsCompiledTwice)
	sc.Step(`^the workflow is compiled$`, w.theWorkflowIsCompiled)
	sc.Step(`^both plans are byte identical$`, w.bothPlansAreByteIdentical)
	sc.Step(`^tasks appear in stable dependency order$`, w.tasksAppearInStableDependencyOrder)
	sc.Step(`^every selected scenario is assigned to a task$`, w.everySelectedScenarioIsAssignedToATask)
	sc.Step(`^every example row becomes an individual scenario$`, w.everyExampleRowBecomesAnIndividualScenario)
	sc.Step(`^task context preserves background steps and multiline arguments$`, w.taskContextPreservesBackgroundStepsAndMultilineArguments)
	sc.Step(`^compilation fails without executing any command$`, w.compilationFailsWithoutExecutingAnyCommand)
}

func (w *compilerAcceptance) aWorkflowPolicyAndGherkinFeatureFiles() error {
	if err := w.writeFile("features/build.feature", `Feature: Build
  Scenario: Compile
    Given source code
  Scenario: Package
    Given compiled code
`); err != nil {
		return err
	}
	if err := w.writeFile("features/check.feature", `Feature: Check
  Scenario: Test
    Given a compiled package
`); err != nil {
		return err
	}
	return w.writeSpec(`version: 1
name: deterministic
features: [features/*.feature]
tasks:
  - id: release
    needs: [check, build]
    features: [features/build.feature]
    run: [true]
  - id: check
    needs: [build]
    features: [features/check.feature]
    run: [true]
  - id: build
    features: [features/build.feature]
    run: [true]
`)
}

func (w *compilerAcceptance) richGherkinFeatures() error {
	if err := w.writeFile("features/transfers.feature", `@transfers
Feature: Account transfers
  Background:
    Given an account exists

  Rule: Transfers respect limits
    Background:
      Given transfers are enabled

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
`); err != nil {
		return err
	}
	return w.writeSpec(`version: 1
name: rich-gherkin
model: codex
features: [features/*.feature]
tasks:
  - id: implement
    prompt: Implement transfers.
`)
}

func (w *compilerAcceptance) aWorkflowPolicyWithDefect(defect string) error {
	validFeature := "Feature: Valid\n  Scenario: Works\n    Given valid input\n"
	if err := w.writeFile("features/valid.feature", validFeature); err != nil {
		return err
	}

	run := fmt.Sprintf("run: [touch, %q]", w.sentinelPath)
	var spec string
	switch defect {
	case "an unsupported version":
		w.wantCompileError = "workflow version must be 1"
		spec = fmt.Sprintf("version: 2\nfeatures: [features/*.feature]\ntasks:\n  - id: task\n    %s\n", run)
	case "an unknown field":
		w.wantCompileError = "field mystery not found"
		spec = fmt.Sprintf("version: 1\nmystery: true\nfeatures: [features/*.feature]\ntasks:\n  - id: task\n    %s\n", run)
	case "a dependency cycle":
		w.wantCompileError = "dependency cycle"
		spec = fmt.Sprintf("version: 1\nfeatures: [features/*.feature]\ntasks:\n  - id: first\n    needs: [second]\n    %s\n  - id: second\n    needs: [first]\n    %s\n", run, run)
	case "an unknown dependency":
		w.wantCompileError = "needs unknown task"
		spec = fmt.Sprintf("version: 1\nfeatures: [features/*.feature]\ntasks:\n  - id: task\n    needs: [missing]\n    %s\n", run)
	case "duplicate task IDs":
		w.wantCompileError = "duplicate task id"
		spec = fmt.Sprintf("version: 1\nfeatures: [features/*.feature]\ntasks:\n  - id: task\n    %s\n  - id: task\n    %s\n", run, run)
	case "no matching features":
		w.wantCompileError = "matched no files"
		spec = fmt.Sprintf("version: 1\nfeatures: [missing/*.feature]\ntasks:\n  - id: task\n    %s\n", run)
	case "an uncovered scenario":
		w.wantCompileError = "is not assigned to any task"
		if err := w.writeFile("features/uncovered.feature", "Feature: Uncovered\n  Scenario: Missed\n    Given omitted input\n"); err != nil {
			return err
		}
		spec = fmt.Sprintf("version: 1\nfeatures: [features/*.feature]\ntasks:\n  - id: task\n    features: [features/valid.feature]\n    %s\n", run)
	case "both run and prompt":
		w.wantCompileError = "exactly one of run or prompt"
		spec = fmt.Sprintf("version: 1\nmodel: codex\nfeatures: [features/*.feature]\ntasks:\n  - id: task\n    %s\n    prompt: implement\n", run)
	case "a missing Codex model":
		w.wantCompileError = "requires a model"
		spec = fmt.Sprintf("version: 1\nfeatures: [features/*.feature]\ntasks:\n  - id: sentinel\n    %s\n  - id: task\n    prompt: implement\n", run)
	case "an invalid attempt limit":
		w.wantCompileError = "attempts must be between 1 and 5"
		spec = fmt.Sprintf("version: 1\nfeatures: [features/*.feature]\ntasks:\n  - id: task\n    %s\n    attempts: 0\n", run)
	default:
		return fmt.Errorf("acceptance fixture does not define defect %q", defect)
	}
	return w.writeSpec(spec)
}

func (w *compilerAcceptance) theWorkflowIsCompiledTwice() error {
	if err := w.compile(); err != nil {
		return err
	}
	return w.compile()
}

func (w *compilerAcceptance) theWorkflowIsCompiled() error {
	plan, err := workflow.Compile(w.specPath)
	w.compileErr = err
	if err == nil {
		w.plans = append(w.plans, plan)
		encoded, marshalErr := json.Marshal(plan)
		if marshalErr != nil {
			return fmt.Errorf("marshal compiled plan: %w", marshalErr)
		}
		w.planBytes = append(w.planBytes, encoded)
	}
	return nil
}

func (w *compilerAcceptance) compile() error {
	if err := w.theWorkflowIsCompiled(); err != nil {
		return err
	}
	if w.compileErr != nil {
		return fmt.Errorf("Compile(%q): %w", w.specPath, w.compileErr)
	}
	return nil
}

func (w *compilerAcceptance) bothPlansAreByteIdentical() error {
	if len(w.planBytes) != 2 {
		return fmt.Errorf("compiled %d plans, want 2", len(w.planBytes))
	}
	if !bytes.Equal(w.planBytes[0], w.planBytes[1]) {
		return fmt.Errorf("compiled plan bytes differ:\nfirst:  %s\nsecond: %s", w.planBytes[0], w.planBytes[1])
	}
	return nil
}

func (w *compilerAcceptance) tasksAppearInStableDependencyOrder() error {
	plan, err := w.singlePlan()
	if err != nil {
		return err
	}
	got := make([]string, len(plan.Tasks))
	for i, task := range plan.Tasks {
		got[i] = task.ID
	}
	want := []string{"build", "check", "release"}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("task order is %v, want %v", got, want)
	}
	return nil
}

func (w *compilerAcceptance) everySelectedScenarioIsAssignedToATask() error {
	plan, err := w.singlePlan()
	if err != nil {
		return err
	}
	want := map[string]bool{
		"features/build.feature\x00Compile": false,
		"features/build.feature\x00Package": false,
		"features/check.feature\x00Test":    false,
	}
	for _, task := range plan.Tasks {
		for _, scenario := range task.Scenarios {
			key := scenario.URI + "\x00" + scenario.Name
			if _, selected := want[key]; selected {
				want[key] = true
			}
		}
	}
	var missing []string
	for scenario, assigned := range want {
		if !assigned {
			missing = append(missing, strings.ReplaceAll(scenario, "\x00", ": "))
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("selected scenarios not assigned to any task: %v", missing)
	}
	return nil
}

func (w *compilerAcceptance) everyExampleRowBecomesAnIndividualScenario() error {
	plan, err := w.singlePlan()
	if err != nil {
		return err
	}
	scenarios := plan.Tasks[0].Scenarios
	if len(scenarios) != 2 {
		return fmt.Errorf("compiled %d scenarios, want one for each of 2 example rows", len(scenarios))
	}
	gotNames := []string{scenarios[0].Name, scenarios[1].Name}
	wantNames := []string{"Transfer 10", "Transfer 20"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		return fmt.Errorf("expanded scenario names are %v, want %v", gotNames, wantNames)
	}
	for i, amount := range []string{"10", "20"} {
		steps := strings.Join(scenarios[i].Steps, "\n")
		if !strings.Contains(steps, "When I transfer "+amount) || !strings.Contains(steps, "| amount | "+amount+" |") || !strings.Contains(steps, `{"amount": `+amount+`}`) {
			return fmt.Errorf("example row %s was not substituted into all step arguments:\n%s", amount, steps)
		}
	}
	return nil
}

func (w *compilerAcceptance) taskContextPreservesBackgroundStepsAndMultilineArguments() error {
	plan, err := w.singlePlan()
	if err != nil {
		return err
	}
	task := plan.Tasks[0]
	for _, scenario := range task.Scenarios {
		steps := strings.Join(scenario.Steps, "\n")
		for _, fragment := range []string{
			"Given an account exists",
			"Given transfers are enabled",
			"Then the audit contains:\n  | field | value |",
			"Then the report is:\n  \"\"\"json",
		} {
			if !strings.Contains(steps, fragment) {
				return fmt.Errorf("scenario %q context is missing %q:\n%s", scenario.Name, fragment, steps)
			}
		}
	}
	for _, fragment := range []string{"Background:", "Rule: Transfers respect limits", "| amount | <amount> |", `{"amount": <amount>}`} {
		if !strings.Contains(task.Prompt, fragment) {
			return fmt.Errorf("task prompt is missing Gherkin context %q:\n%s", fragment, task.Prompt)
		}
	}
	return nil
}

func (w *compilerAcceptance) compilationFailsWithoutExecutingAnyCommand() error {
	if w.compileErr == nil {
		return fmt.Errorf("Compile(%q) succeeded for invalid specification", w.specPath)
	}
	if !strings.Contains(w.compileErr.Error(), w.wantCompileError) {
		return fmt.Errorf("Compile(%q) error %q does not contain %q", w.specPath, w.compileErr, w.wantCompileError)
	}
	if _, err := os.Stat(w.sentinelPath); err == nil {
		return fmt.Errorf("invalid specification executed task command and created %q", w.sentinelPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect task command sentinel: %w", err)
	}
	return nil
}

func (w *compilerAcceptance) singlePlan() (*workflow.Plan, error) {
	if w.compileErr != nil {
		return nil, fmt.Errorf("Compile(%q): %w", w.specPath, w.compileErr)
	}
	if len(w.plans) == 0 {
		return nil, fmt.Errorf("workflow was not compiled")
	}
	return w.plans[0], nil
}

func (w *compilerAcceptance) writeSpec(content string) error {
	w.specPath = filepath.Join(w.dir, "workflow.yaml")
	return w.writeFile("workflow.yaml", content)
}

func (w *compilerAcceptance) writeFile(name, content string) error {
	path := filepath.Join(w.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create fixture parent for %q: %w", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write fixture %q: %w", name, err)
	}
	return nil
}
