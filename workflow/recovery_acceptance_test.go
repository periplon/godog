package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/cucumber/godog/workflow"
)

type recoveryAcceptance struct {
	root, repo, spec, output string
	plan                     *workflow.Plan
	result, previous         *workflow.Result
	err                      error
	cancel                   context.CancelFunc
	done                     chan executeOutcome
	counts                   map[string]string
}

type executeOutcome struct {
	result *workflow.Result
	err    error
}

func TestRecoveryAcceptance(t *testing.T) {
	world := &recoveryAcceptance{}
	suite := godog.TestSuite{
		Name:                "workflow incremental and recovery acceptance",
		ScenarioInitializer: world.initialize,
		Options:             &godog.Options{Format: "progress", Paths: []string{"features/incremental.feature", "features/resume.feature"}, Strict: true, TestingT: t},
	}
	if status := suite.Run(); status != 0 {
		t.Fatalf("recovery acceptance suite failed with status %d", status)
	}
}

func (w *recoveryAcceptance) initialize(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		root, err := os.MkdirTemp("", "godog-recovery-acceptance-")
		if err != nil {
			return ctx, err
		}
		*w = recoveryAcceptance{root: root, counts: map[string]string{}}
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, scenarioErr error) (context.Context, error) {
		if w.cancel != nil {
			w.cancel()
		}
		if w.done != nil {
			select {
			case <-w.done:
			case <-time.After(3 * time.Second):
			}
		}
		cleanupErr := os.RemoveAll(w.root)
		if scenarioErr != nil {
			return ctx, scenarioErr
		}
		return ctx, cleanupErr
	})

	sc.Step(`^a successful workflow whose integration commit is in the source history$`, w.successfulRecordedWorkflow)
	sc.Step(`^implemented scenarios in a feature$`, w.successfulRecordedWorkflow)
	sc.Step(`^the same features and policy are planned again$`, w.planIncrementally)
	sc.Step(`^a new scenario is inserted before them$`, w.insertScenario)
	sc.Step(`^one scenario's steps change$`, w.changeScenario)
	sc.Step(`^their background or workflow policy changes$`, w.changeBackgroundAndPolicy)
	sc.Step(`^a successful workflow whose integration commit is absent from source history$`, w.unmergedRecordedWorkflow)
	sc.Step(`^an execution with an unsuccessful verification task$`, w.failedImplementationRecord)
	sc.Step(`^the features are planned again$`, w.planIncrementally)
	sc.Step(`^no implementation tasks are generated$`, w.noTasksGenerated)
	sc.Step(`^only the new scenario is an implementation target$`, w.onlyNewScenario)
	sc.Step(`^necessary prerequisite and verification tasks remain$`, w.prerequisitesAndVerificationRemain)
	sc.Step(`^only that scenario is an implementation target$`, w.onlyChangedScenario)
	sc.Step(`^the affected scenarios are implementation targets again$`, w.allBehaviorScenariosTargeted)
	sc.Step(`^its scenarios remain implementation targets$`, w.behaviorScenariosRemainTargets)

	sc.Step(`^an execution with a successful prerequisite and a failed dependent task$`, w.failedDependentExecution)
	sc.Step(`^a stopped execution with completed and unfinished steps$`, w.interruptedExecution)
	sc.Step(`^an execution that is still running$`, w.liveExecution)
	sc.Step(`^an execution whose source baseline or saved artifacts have changed$`, w.alteredExecution)
	sc.Step(`^a fully successful execution$`, w.completedExecution)
	sc.Step(`^that execution is resumed$`, w.resumeExecution)
	sc.Step(`^a second runner attempts to resume it$`, w.resumeExecution)
	sc.Step(`^recovery fails before any task is executed$`, w.recoveryRejectedBeforeExecution)
	sc.Step(`^the successful prerequisite is not executed again$`, w.prerequisiteNotRepeated)
	sc.Step(`^the failed task and its blocked dependents are retried$`, w.failureAndBlockedRetried)
	sc.Step(`^attempt history is preserved$`, w.attemptHistoryPreserved)
	sc.Step(`^completed steps are reused from their durable checkpoints$`, w.completedCheckpointReused)
	sc.Step(`^only unfinished steps execute$`, w.onlyUnfinishedExecuted)
	sc.Step(`^the second runner is rejected without modifying execution state$`, w.concurrentResumeRejected)
	sc.Step(`^no task is executed again$`, w.noTaskRepeated)
	sc.Step(`^the recorded integration result is returned$`, w.integrationResultReturned)
}

func (w *recoveryAcceptance) successfulRecordedWorkflow() error {
	if err := w.makeIncrementalFixture(); err != nil {
		return err
	}
	plan, err := workflow.CompileIncremental(context.Background(), w.spec, w.repo)
	if err != nil {
		return err
	}
	w.plan = plan
	return workflow.RecordImplementation(context.Background(), plan, successfulResult(plan, git(tgit{dir: w.repo}, "rev-parse", "HEAD")), w.repo)
}

func (w *recoveryAcceptance) unmergedRecordedWorkflow() error {
	if err := w.makeIncrementalFixture(); err != nil {
		return err
	}
	plan, err := workflow.CompileIncremental(context.Background(), w.spec, w.repo)
	if err != nil {
		return err
	}
	w.plan = plan
	old := git(tgit{dir: w.repo}, "rev-parse", "HEAD")
	git(tgit{dir: w.repo}, "commit", "--no-gpg-sign", "--allow-empty", "-qm", "temporary integrated result")
	newHead := git(tgit{dir: w.repo}, "rev-parse", "HEAD")
	if err := workflow.RecordImplementation(context.Background(), w.plan, successfulResult(w.plan, newHead), w.repo); err != nil {
		return err
	}
	git(tgit{dir: w.repo}, "reset", "--hard", "-q", old)
	return nil
}

func (w *recoveryAcceptance) failedImplementationRecord() error {
	if err := w.makeIncrementalFixture(); err != nil {
		return err
	}
	plan, err := workflow.CompileIncremental(context.Background(), w.spec, w.repo)
	if err != nil {
		return err
	}
	result := successfulResult(plan, git(tgit{dir: w.repo}, "rev-parse", "HEAD"))
	result.Tasks[len(result.Tasks)-1].Status = "failed"
	if err := workflow.RecordImplementation(context.Background(), plan, result, w.repo); err == nil {
		return errors.New("failed execution was recorded as implemented")
	}
	return nil
}

func (w *recoveryAcceptance) makeIncrementalFixture() error {
	if err := w.initRepo(); err != nil {
		return err
	}
	if err := write(w.repo, "features/setup.feature", "Feature: Setup\n  Scenario: Prepare\n    Given setup exists\n"); err != nil {
		return err
	}
	if err := write(w.repo, "features/behavior.feature", behaviorFeature("shared context", "first result")); err != nil {
		return err
	}
	w.spec = filepath.Join(w.repo, "workflow.yaml")
	if err := write(w.repo, "workflow.yaml", incrementalSpec("Implement behavior.")); err != nil {
		return err
	}
	git(tgit{dir: w.repo}, "add", ".")
	git(tgit{dir: w.repo}, "commit", "--no-gpg-sign", "-qm", "fixture")
	return nil
}

func (w *recoveryAcceptance) planIncrementally() error {
	w.plan, w.err = workflow.CompileIncremental(context.Background(), w.spec, w.repo)
	return nil
}

func (w *recoveryAcceptance) insertScenario() error {
	return write(w.repo, "features/behavior.feature", strings.Replace(behaviorFeature("shared context", "first result"), "  Scenario: First", "  Scenario: Added\n    When added action runs\n    Then added result appears\n\n  Scenario: First", 1))
}

func (w *recoveryAcceptance) changeScenario() error {
	return write(w.repo, "features/behavior.feature", behaviorFeature("shared context", "changed result"))
}

func (w *recoveryAcceptance) changeBackgroundAndPolicy() error {
	if err := write(w.repo, "features/behavior.feature", behaviorFeature("changed shared context", "first result")); err != nil {
		return err
	}
	return write(w.repo, "workflow.yaml", incrementalSpec("Implement behavior carefully."))
}

func (w *recoveryAcceptance) noTasksGenerated() error {
	if w.err != nil {
		return w.err
	}
	if len(w.plan.Tasks) != 0 || w.plan.Tracking == nil || !w.plan.Tracking.NoOp {
		return fmt.Errorf("plan is not a no-op: %#v", w.plan)
	}
	return nil
}

func (w *recoveryAcceptance) onlyNewScenario() error     { return w.onlyTargets("Added") }
func (w *recoveryAcceptance) onlyChangedScenario() error { return w.onlyTargets("First") }

func (w *recoveryAcceptance) onlyTargets(want ...string) error {
	if w.err != nil {
		return w.err
	}
	task := findTask(w.plan, "implement")
	if task == nil {
		return errors.New("implementation task missing")
	}
	got := scenarioNames(task.Scenarios)
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("implementation targets %v, want %v", got, want)
	}
	return nil
}

func (w *recoveryAcceptance) prerequisitesAndVerificationRemain() error {
	got := taskIDs(w.plan.Tasks)
	want := []string{"prepare", "implement", "verify"}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("task closure %v, want %v", got, want)
	}
	return nil
}

func (w *recoveryAcceptance) allBehaviorScenariosTargeted() error {
	return w.onlyTargets("First", "Second")
}
func (w *recoveryAcceptance) behaviorScenariosRemainTargets() error {
	return w.onlyTargets("First", "Second")
}

func (w *recoveryAcceptance) failedDependentExecution() error {
	if err := w.makeExecutionFixture(); err != nil {
		return err
	}
	w.counts["base"] = filepath.Join(w.root, "base.count")
	w.counts["finish"] = filepath.Join(w.root, "finish.count")
	w.counts["verify"] = filepath.Join(w.root, "verify.count")
	w.plan = &workflow.Plan{Version: 1, Name: "resume failure", Tasks: []workflow.Task{
		commandTask("base", nil, "count-write", w.counts["base"], "base.txt"),
		commandTask("finish", []string{"base"}, "fail-once", w.counts["finish"], "finish.txt", "base.txt"),
		commandTask("verify", []string{"finish"}, "count", w.counts["verify"]),
	}}
	w.previous, w.err = workflow.Execute(context.Background(), w.plan, workflow.RunOptions{Dir: w.repo, OutputDir: w.output, Jobs: 1})
	if w.err == nil {
		return errors.New("fixture execution unexpectedly succeeded")
	}
	return nil
}

func (w *recoveryAcceptance) interruptedExecution() error {
	if err := w.makeExecutionFixture(); err != nil {
		return err
	}
	w.counts["base"] = filepath.Join(w.root, "base.count")
	w.counts["wait"] = filepath.Join(w.root, "wait.count")
	started, release := filepath.Join(w.root, "started"), filepath.Join(w.root, "release")
	w.plan = &workflow.Plan{Version: 1, Name: "interrupted", Tasks: []workflow.Task{
		commandTask("base", nil, "count-write", w.counts["base"], "base.txt"),
		commandTask("wait", []string{"base"}, "gate", w.counts["wait"], started, release),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	done := make(chan executeOutcome, 1)
	go func() {
		result, err := workflow.Execute(ctx, w.plan, workflow.RunOptions{Dir: w.repo, OutputDir: w.output, Jobs: 1})
		done <- executeOutcome{result, err}
	}()
	if err := waitFile(started); err != nil {
		return err
	}
	cancel()
	out := <-done
	w.cancel, w.previous, w.err = nil, out.result, out.err
	return os.WriteFile(release, []byte("release"), 0o600)
}

func (w *recoveryAcceptance) liveExecution() error {
	if err := w.makeExecutionFixture(); err != nil {
		return err
	}
	started, release := filepath.Join(w.root, "live-started"), filepath.Join(w.root, "live-release")
	w.counts["live"] = filepath.Join(w.root, "live.count")
	w.plan = &workflow.Plan{Version: 1, Tasks: []workflow.Task{commandTask("live", nil, "gate", w.counts["live"], started, release)}}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan executeOutcome, 1)
	go func() {
		result, err := workflow.Execute(ctx, w.plan, workflow.RunOptions{Dir: w.repo, OutputDir: w.output, Jobs: 1})
		w.done <- executeOutcome{result, err}
	}()
	return waitFile(started)
}

func (w *recoveryAcceptance) alteredExecution() error {
	if err := w.completedExecution(); err != nil {
		return err
	}
	planPath := filepath.Join(w.output, "plan.json")
	b, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	return os.WriteFile(planPath, append(b, '\n'), 0o600)
}

func (w *recoveryAcceptance) completedExecution() error {
	if err := w.makeExecutionFixture(); err != nil {
		return err
	}
	w.counts["complete"] = filepath.Join(w.root, "complete.count")
	w.plan = &workflow.Plan{Version: 1, Name: "completed", Tasks: []workflow.Task{commandTask("complete", nil, "count", w.counts["complete"])}}
	w.previous, w.err = workflow.Execute(context.Background(), w.plan, workflow.RunOptions{Dir: w.repo, OutputDir: w.output, Jobs: 1})
	return w.err
}

func (w *recoveryAcceptance) makeExecutionFixture() error {
	if err := w.initRepo(); err != nil {
		return err
	}
	w.output = filepath.Join(w.root, "run")
	return nil
}

func (w *recoveryAcceptance) initRepo() error {
	w.repo = filepath.Join(w.root, "repo")
	if err := os.MkdirAll(w.repo, 0o755); err != nil {
		return err
	}
	git(tgit{dir: w.repo}, "init", "-q")
	git(tgit{dir: w.repo}, "config", "user.name", "Acceptance")
	git(tgit{dir: w.repo}, "config", "user.email", "acceptance@example.invalid")
	if err := write(w.repo, "README.md", "fixture\n"); err != nil {
		return err
	}
	git(tgit{dir: w.repo}, "add", ".")
	git(tgit{dir: w.repo}, "commit", "--no-gpg-sign", "-qm", "baseline")
	return nil
}

func (w *recoveryAcceptance) resumeExecution() error {
	before, _ := os.ReadFile(filepath.Join(w.output, "result.json"))
	w.result, w.err = workflow.Resume(context.Background(), w.output, workflow.RunOptions{Jobs: 1})
	if w.done != nil && w.err != nil {
		after, _ := os.ReadFile(filepath.Join(w.output, "result.json"))
		if !reflect.DeepEqual(before, after) {
			return errors.New("rejected concurrent resume modified result.json")
		}
	}
	return nil
}

func (w *recoveryAcceptance) prerequisiteNotRepeated() error { return expectCount(w.counts["base"], 1) }
func (w *recoveryAcceptance) failureAndBlockedRetried() error {
	if w.err != nil {
		return w.err
	}
	if err := expectCount(w.counts["finish"], 2); err != nil {
		return err
	}
	return expectCount(w.counts["verify"], 1)
}
func (w *recoveryAcceptance) attemptHistoryPreserved() error {
	task := resultTask(w.result, "finish")
	if task == nil || task.Attempts != 2 {
		return fmt.Errorf("finish attempts = %#v, want 2", task)
	}
	if len(w.result.Runs) != 2 || !w.result.Runs[1].Resumed {
		return fmt.Errorf("run history = %#v", w.result.Runs)
	}
	return nil
}
func (w *recoveryAcceptance) completedCheckpointReused() error {
	return expectCount(w.counts["base"], 1)
}
func (w *recoveryAcceptance) onlyUnfinishedExecuted() error {
	if w.err != nil {
		return w.err
	}
	if err := expectCount(w.counts["wait"], 2); err != nil {
		return err
	}
	return expectCount(w.counts["base"], 1)
}
func (w *recoveryAcceptance) concurrentResumeRejected() error {
	if w.err == nil || !strings.Contains(w.err.Error(), "locked") {
		return fmt.Errorf("concurrent Resume error = %v", w.err)
	}
	return os.WriteFile(filepath.Join(w.root, "live-release"), []byte("release"), 0o600)
}
func (w *recoveryAcceptance) recoveryRejectedBeforeExecution() error {
	if w.err == nil || !strings.Contains(w.err.Error(), "plan") {
		return fmt.Errorf("altered execution Resume error = %v", w.err)
	}
	return expectCount(w.counts["complete"], 1)
}
func (w *recoveryAcceptance) noTaskRepeated() error {
	if w.err != nil {
		return w.err
	}
	return expectCount(w.counts["complete"], 1)
}
func (w *recoveryAcceptance) integrationResultReturned() error {
	if w.result.Commit != w.previous.Commit || w.result.IntegrationWorktree != w.previous.IntegrationWorktree {
		return fmt.Errorf("resume returned different integration result")
	}
	return nil
}

func behaviorFeature(background, firstResult string) string {
	return fmt.Sprintf(`Feature: Behavior
  Background:
    Given %s

  Scenario: First
    When first action runs
    Then %s

  Scenario: Second
    When second action runs
    Then second result appears
`, background, firstResult)
}

func incrementalSpec(prompt string) string {
	prepare, _ := json.Marshal([]string{os.Args[0], "-test.run=^TestRecoveryTaskHelper$", "--", "count"})
	verify, _ := json.Marshal([]string{os.Args[0], "-test.run=^TestRecoveryTaskHelper$", "--", "count"})
	return fmt.Sprintf(`version: 1
name: incremental
model: codex
features: [features/*.feature]
tasks:
  - id: prepare
    features: [features/setup.feature]
    run: %s
  - id: implement
    needs: [prepare]
    features: [features/behavior.feature]
    prompt: %s
  - id: verify
    needs: [implement]
    features: [features/behavior.feature]
    run: %s
`, prepare, prompt, verify)
}

func successfulResult(plan *workflow.Plan, commit string) *workflow.Result {
	tasks := make([]workflow.TaskResult, len(plan.Tasks))
	for i, task := range plan.Tasks {
		tasks[i] = workflow.TaskResult{ID: task.ID, Status: "success", Commit: commit}
	}
	return &workflow.Result{Baseline: commit, Tasks: tasks, Commit: commit}
}

func commandTask(id string, needs []string, args ...string) workflow.Task {
	return workflow.Task{TaskSpec: workflow.TaskSpec{ID: id, Needs: needs, Run: append([]string{os.Args[0], "-test.run=^TestRecoveryTaskHelper$", "--"}, args...), Attempts: 1, Timeout: "5s"}}
}

func TestRecoveryTaskHelper(t *testing.T) {
	args := helperArgs(os.Args)
	if len(args) == 0 {
		t.Skip("task subprocess helper")
	}
	switch args[0] {
	case "count":
		increment(args[1])
	case "count-write":
		increment(args[1])
		mustHelperWrite(args[2], "done")
	case "fail-once":
		count := increment(args[1])
		if len(args) > 3 {
			if _, err := os.Stat(args[3]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(9)
			}
		}
		if count == 1 {
			os.Exit(7)
		}
		mustHelperWrite(args[2], "done")
	case "gate":
		increment(args[1])
		mustHelperWrite(args[2], "started")
		for {
			if _, err := os.Stat(args[3]); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	default:
		t.Fatalf("unknown helper action %q", args[0])
	}
}

func helperArgs(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return nil
}
func increment(path string) int {
	value := 0
	if b, err := os.ReadFile(path); err == nil {
		value, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	value++
	mustHelperWrite(path, strconv.Itoa(value))
	return value
}
func mustHelperWrite(path, value string) {
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		panic(err)
	}
}

func write(root, name, contents string) error {
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(contents), 0o600)
}

type tgit struct{ dir string }

func git(cfg tgit, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", cfg.dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("git %v: %v: %s", args, err, out))
	}
	return strings.TrimSpace(string(out))
}

func taskIDs(tasks []workflow.Task) []string {
	ids := make([]string, len(tasks))
	for i := range tasks {
		ids[i] = tasks[i].ID
	}
	return ids
}
func scenarioNames(items []workflow.Scenario) []string {
	names := make([]string, len(items))
	for i := range items {
		names[i] = items[i].Name
	}
	return names
}
func findTask(plan *workflow.Plan, id string) *workflow.Task {
	for i := range plan.Tasks {
		if plan.Tasks[i].ID == id {
			return &plan.Tasks[i]
		}
	}
	return nil
}
func resultTask(result *workflow.Result, id string) *workflow.TaskResult {
	if result == nil {
		return nil
	}
	for i := range result.Tasks {
		if result.Tasks[i].ID == id {
			return &result.Tasks[i]
		}
	}
	return nil
}
func expectCount(path string, want int) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("count %s = %d, want %d", path, got, want)
	}
	return nil
}
func waitFile(path string) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", path)
}

func updateJSON(path string, edit func(map[string]any)) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	edit(v)
	b, err = json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
