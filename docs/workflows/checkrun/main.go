// Command checkrun verifies the retained evidence from the Godog self-host run.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/cucumber/godog/workflow"
)

const expectedPlanName = "godog-workflow-development"

var expectedTaskIDs = []string{
	"acceptance", "harden", "review-compiler", "review-execution", "reconcile", "verify", "vet",
}

var expectedNeeds = map[string][]string{
	"acceptance": {}, "harden": {},
	"review-compiler":  {"acceptance", "harden"},
	"review-execution": {"acceptance", "harden"},
	"reconcile":        {"review-compiler", "review-execution"},
	"verify":           {"reconcile"},
	"vet":              {"reconcile"},
}

type resultFile struct {
	Baseline            string       `json:"baseline"`
	Tasks               []taskResult `json:"tasks"`
	IntegrationWorktree string       `json:"integration_worktree"`
	Commit              string       `json:"commit"`
}

type taskResult struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	Worktree   string `json:"worktree"`
	Commit     string `json:"commit"`
	Log        string `json:"log"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

type reviewFile struct {
	Findings json.RawMessage `json:"findings"`
	Scope    string          `json:"scope"`
	Evidence string          `json:"evidence"`
}

type resolutionFile struct {
	Unresolved  json.RawMessage `json:"unresolved"`
	Resolutions json.RawMessage `json:"resolutions"`
}

type finding struct {
	ID           string `json:"id"`
	Severity     string `json:"severity"`
	File         string `json:"file"`
	Description  string `json:"description"`
	Reproduction string `json:"reproduction"`
}

type resolution struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: checkrun RUN_DIR")
		os.Exit(2)
	}
	if err := check(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "self-host evidence invalid:", err)
		os.Exit(1)
	}
	fmt.Println("self-host evidence verified")
}

func check(runDir string) error {
	runDir, err := filepath.Abs(runDir)
	if err != nil {
		return err
	}
	var plan workflow.Plan
	if err := readFile(filepath.Join(runDir, "plan.json"), &plan); err != nil {
		return fmt.Errorf("plan.json: %w", err)
	}
	if err := checkPlan(plan); err != nil {
		return err
	}

	var result resultFile
	if err := readFile(filepath.Join(runDir, "result.json"), &result); err != nil {
		return fmt.Errorf("result.json: %w", err)
	}
	results, err := checkResults(runDir, result)
	if err != nil {
		return err
	}
	if err := checkOverlap(results["acceptance"], results["harden"]); err != nil {
		return err
	}
	if err := checkGitEvidence(runDir, result, results); err != nil {
		return err
	}
	integration := resolvePath(runDir, result.IntegrationWorktree)
	if err := checkBaselinePlan(integration, result.Baseline, &plan); err != nil {
		return err
	}
	if err := checkReviews(integration, results); err != nil {
		return err
	}
	return nil
}

func checkPlan(plan workflow.Plan) error {
	if plan.Version != 1 || plan.Name != expectedPlanName {
		return fmt.Errorf("plan identity is version %d name %q", plan.Version, plan.Name)
	}
	ids := make([]string, len(plan.Tasks))
	seen := make(map[string]bool, len(plan.Tasks))
	for i, task := range plan.Tasks {
		ids[i] = task.ID
		if seen[task.ID] {
			return fmt.Errorf("duplicate plan task %q", task.ID)
		}
		seen[task.ID] = true
		wantNeeds, ok := expectedNeeds[task.ID]
		if !ok || !sameStrings(task.Needs, wantNeeds) {
			return fmt.Errorf("plan task %q dependencies are %v, want %v", task.ID, task.Needs, wantNeeds)
		}
		command := len(task.Run) > 0
		agent := strings.TrimSpace(task.Prompt) != ""
		if command == agent {
			return fmt.Errorf("plan task %q does not have exactly one action", task.ID)
		}
		if (task.ID == "verify" || task.ID == "vet") != command {
			return fmt.Errorf("plan task %q has wrong action type", task.ID)
		}
		if agent && task.Model != "gpt-5.6-sol" {
			return fmt.Errorf("plan task %q model is %q, want gpt-5.6-sol", task.ID, task.Model)
		}
		if command && task.ExpectExit != 0 {
			return fmt.Errorf("plan task %q expected exit is %d, want 0", task.ID, task.ExpectExit)
		}
	}
	if !exactStrings(ids, expectedTaskIDs) {
		return fmt.Errorf("plan task IDs are %v, want %v", ids, expectedTaskIDs)
	}
	if !exactStrings(plan.Tasks[5].Run, []string{"go", "test", "-race", "./..."}) {
		return fmt.Errorf("verify argv is %v", plan.Tasks[5].Run)
	}
	if !exactStrings(plan.Tasks[6].Run, []string{"go", "vet", "./..."}) {
		return fmt.Errorf("vet argv is %v", plan.Tasks[6].Run)
	}
	return nil
}

func checkResults(runDir string, result resultFile) (map[string]taskResult, error) {
	if result.Baseline == "" || result.Commit == "" || result.IntegrationWorktree == "" {
		return nil, errors.New("result lacks baseline, integration commit, or integration worktree")
	}
	if len(result.Tasks) != len(expectedTaskIDs) {
		return nil, fmt.Errorf("result task IDs count is %d, want %d", len(result.Tasks), len(expectedTaskIDs))
	}
	byID := make(map[string]taskResult, len(result.Tasks))
	worktrees := make(map[string]string, len(result.Tasks))
	for _, task := range result.Tasks {
		if _, ok := expectedNeeds[task.ID]; !ok {
			return nil, fmt.Errorf("unexpected result task %q", task.ID)
		}
		if _, duplicate := byID[task.ID]; duplicate {
			return nil, fmt.Errorf("duplicate result task %q", task.ID)
		}
		if task.Status != "success" {
			return nil, fmt.Errorf("task %s status is %q, want success", task.ID, task.Status)
		}
		if task.Attempts < 1 || task.Attempts > 5 {
			return nil, fmt.Errorf("task %s attempts is %d", task.ID, task.Attempts)
		}
		worktree, err := existingDir(resolvePath(runDir, task.Worktree))
		if err != nil {
			return nil, fmt.Errorf("task %s worktree: %w", task.ID, err)
		}
		if prior, exists := worktrees[worktree]; exists {
			return nil, fmt.Errorf("tasks %s and %s share worktree %s", prior, task.ID, worktree)
		}
		worktrees[worktree] = task.ID
		task.Worktree = worktree
		logPath := resolvePath(runDir, task.Log)
		contents, err := os.ReadFile(logPath)
		if err != nil {
			return nil, fmt.Errorf("task %s log: %w", task.ID, err)
		}
		if len(contents) == 0 {
			return nil, fmt.Errorf("task %s log is empty", task.ID)
		}
		if task.ID != "verify" && task.ID != "vet" && !strings.Contains(string(contents), "model: gpt-5.6-sol") {
			return nil, fmt.Errorf("task %s log lacks exact model evidence", task.ID)
		}
		start, finish, err := parseInterval(task)
		if err != nil {
			return nil, err
		}
		_ = start
		_ = finish
		byID[task.ID] = task
	}
	for _, id := range expectedTaskIDs {
		if _, ok := byID[id]; !ok {
			return nil, fmt.Errorf("result task IDs lack %s", id)
		}
	}
	return byID, nil
}

func checkOverlap(a, b taskResult) error {
	aStart, aFinish, _ := parseInterval(a)
	bStart, bFinish, _ := parseInterval(b)
	if !aStart.Before(bFinish) || !bStart.Before(aFinish) {
		return errors.New("acceptance and harden execution intervals did not overlap")
	}
	return nil
}

func checkGitEvidence(runDir string, result resultFile, tasks map[string]taskResult) error {
	integration, err := existingDir(resolvePath(runDir, result.IntegrationWorktree))
	if err != nil {
		return fmt.Errorf("integration worktree: %w", err)
	}
	if err := gitObject(integration, result.Baseline); err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	if err := gitObject(integration, result.Commit); err != nil {
		return fmt.Errorf("integration commit: %w", err)
	}
	head, err := gitOutput(integration, "rev-parse", "HEAD")
	if err != nil || head != result.Commit {
		return fmt.Errorf("integration worktree HEAD %q does not equal result commit %q", head, result.Commit)
	}
	for _, id := range expectedTaskIDs {
		task := tasks[id]
		if task.Worktree == integration {
			return fmt.Errorf("integration worktree is reused by task %s", id)
		}
		if err := gitObject(task.Worktree, task.Commit); err != nil {
			return fmt.Errorf("task %s commit: %w", id, err)
		}
		if err := gitRun(integration, "merge-base", "--is-ancestor", task.Commit, result.Commit); err != nil {
			return fmt.Errorf("final integration does not contain task %s commit %s", id, task.Commit)
		}
		if err := gitRun(integration, "merge-base", "--is-ancestor", result.Baseline, task.Commit); err != nil {
			return fmt.Errorf("task %s commit does not contain baseline %s", id, result.Baseline)
		}
		for _, need := range expectedNeeds[id] {
			if err := gitRun(integration, "merge-base", "--is-ancestor", tasks[need].Commit, task.Commit); err != nil {
				return fmt.Errorf("task %s commit does not contain dependency %s commit", id, need)
			}
		}
	}
	acceptanceNames, err := changedPaths(tasks["acceptance"].Worktree, result.Baseline, tasks["acceptance"].Commit)
	if err != nil {
		return fmt.Errorf("inspect acceptance commit: %w", err)
	}
	if len(acceptanceNames) != 1 || acceptanceNames[0] != "workflow/acceptance_test.go" {
		return fmt.Errorf("acceptance commit must change only workflow/acceptance_test.go, got %v", acceptanceNames)
	}
	hardenNames, err := changedPaths(tasks["harden"].Worktree, result.Baseline, tasks["harden"].Commit)
	if err != nil {
		return fmt.Errorf("inspect harden commit: %w", err)
	}
	var productionChange, testChange bool
	for _, name := range hardenNames {
		if !allowedHardenPath(name) {
			return fmt.Errorf("harden commit changes path outside its ownership: %s", name)
		}
		test := strings.HasSuffix(name, "_test.go")
		testChange = testChange || test
		productionChange = productionChange || !test
	}
	if !productionChange || !testChange {
		return errors.New("harden commit must contain production and test changes")
	}
	return nil
}

func checkReviews(integration string, tasks map[string]taskResult) error {
	findings := make(map[string]bool)
	producers := []struct {
		task string
		name string
	}{{"review-compiler", "compiler.json"}, {"review-execution", "execution.json"}}
	for _, producer := range producers {
		var review reviewFile
		if err := readGitJSON(integration, tasks[producer.task].Commit, filepath.ToSlash(filepath.Join(".workflow-review", producer.name)), &review); err != nil {
			return fmt.Errorf("review %s in producer task %s: %w", producer.name, producer.task, err)
		}
		if strings.TrimSpace(review.Scope) == "" {
			return fmt.Errorf("review %s scope is missing", producer.name)
		}
		if strings.TrimSpace(review.Evidence) == "" {
			return fmt.Errorf("review %s evidence is missing", producer.name)
		}
		var items []finding
		if len(review.Findings) == 0 {
			return fmt.Errorf("review %s findings field is missing", producer.name)
		}
		if err := json.Unmarshal(review.Findings, &items); err != nil {
			return fmt.Errorf("review %s findings must be an array: %w", producer.name, err)
		}
		for _, item := range items {
			if strings.TrimSpace(item.ID) == "" {
				return fmt.Errorf("review %s contains finding without ID", producer.name)
			}
			if strings.TrimSpace(item.Severity) == "" {
				return fmt.Errorf("review %s finding %s lacks severity", producer.name, item.ID)
			}
			if strings.TrimSpace(item.File) == "" {
				return fmt.Errorf("review %s finding %s lacks file", producer.name, item.ID)
			}
			if strings.TrimSpace(item.Description) == "" {
				return fmt.Errorf("review %s finding %s lacks description", producer.name, item.ID)
			}
			if strings.TrimSpace(item.Reproduction) == "" {
				return fmt.Errorf("review %s finding %s lacks reproduction", producer.name, item.ID)
			}
			if findings[item.ID] {
				return fmt.Errorf("duplicate review finding %q", item.ID)
			}
			findings[item.ID] = true
		}
	}
	var resolutionRecord resolutionFile
	if err := readGitJSON(integration, tasks["reconcile"].Commit, ".workflow-review/resolution.json", &resolutionRecord); err != nil {
		return fmt.Errorf("resolution: %w", err)
	}
	unresolved, err := arrayLength(resolutionRecord.Unresolved)
	if err != nil {
		return fmt.Errorf("resolution unresolved: %w", err)
	}
	if unresolved != 0 {
		return fmt.Errorf("resolution has %d unresolved findings", unresolved)
	}
	var resolutions []resolution
	if len(resolutionRecord.Resolutions) == 0 {
		return errors.New("resolution resolutions field is missing")
	}
	if err := json.Unmarshal(resolutionRecord.Resolutions, &resolutions); err != nil {
		return fmt.Errorf("resolution resolutions must be an array: %w", err)
	}
	resolved := make(map[string]bool, len(resolutions))
	for _, item := range resolutions {
		if !findings[item.ID] {
			return fmt.Errorf("resolution references unknown finding %q", item.ID)
		}
		if resolved[item.ID] {
			return fmt.Errorf("finding %q has duplicate resolutions", item.ID)
		}
		if item.Status != "fixed" && item.Status != "disproven" {
			return fmt.Errorf("finding %q resolution status is %q", item.ID, item.Status)
		}
		if strings.TrimSpace(item.Evidence) == "" {
			return fmt.Errorf("finding %q resolution lacks evidence", item.ID)
		}
		resolved[item.ID] = true
	}
	for id := range findings {
		if !resolved[id] {
			return fmt.Errorf("finding %q lacks a resolution", id)
		}
	}
	return nil
}

func checkBaselinePlan(repo, baseline string, recorded *workflow.Plan) error {
	directory, err := os.MkdirTemp("", "godog-checkrun-baseline-")
	if err != nil {
		return fmt.Errorf("create baseline snapshot: %w", err)
	}
	defer os.RemoveAll(directory)

	pathsText, err := gitOutput(repo, "ls-tree", "-r", "--name-only", baseline, "--", "workflow/features")
	if err != nil {
		return fmt.Errorf("list baseline workflow features: %w", err)
	}
	paths := []string{"docs/workflows/develop.yaml"}
	for _, name := range strings.Split(pathsText, "\n") {
		if strings.HasSuffix(name, ".feature") {
			paths = append(paths, name)
		}
	}
	for _, name := range paths {
		contents, err := gitBytes(repo, "show", baseline+":"+name)
		if err != nil {
			return fmt.Errorf("read baseline %s: %w", name, err)
		}
		target := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create baseline snapshot path: %w", err)
		}
		if err := os.WriteFile(target, contents, 0o644); err != nil {
			return fmt.Errorf("write baseline %s: %w", name, err)
		}
	}
	expected, err := workflow.Compile(filepath.Join(directory, "docs", "workflows", "develop.yaml"))
	if err != nil {
		return fmt.Errorf("compile baseline plan: %w", err)
	}
	if !reflect.DeepEqual(recorded, expected) {
		return errors.New("recorded plan does not match the full baseline plan")
	}
	return nil
}

func changedPaths(repo, from, to string) ([]string, error) {
	names, err := gitOutput(repo, "diff", "--name-only", from, to)
	if err != nil {
		return nil, err
	}
	if names == "" {
		return nil, nil
	}
	return strings.Split(names, "\n"), nil
}

func allowedHardenPath(name string) bool {
	if !strings.HasSuffix(name, ".go") {
		return false
	}
	base := strings.TrimPrefix(name, "workflow/")
	return base != name && (strings.HasPrefix(base, "executor") || strings.HasPrefix(base, "process"))
}

func readGitJSON(repo, commit, path string, dst any) error {
	body, err := gitOutput(repo, "show", commit+":"+path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("contains more than one JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func parseInterval(task taskResult) (time.Time, time.Time, error) {
	start, err := time.Parse(time.RFC3339Nano, task.StartedAt)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("task %s started_at: %w", task.ID, err)
	}
	finish, err := time.Parse(time.RFC3339Nano, task.FinishedAt)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("task %s finished_at: %w", task.ID, err)
	}
	if !start.Before(finish) {
		return time.Time{}, time.Time{}, fmt.Errorf("task %s interval is not positive", task.ID)
	}
	return start, finish, nil
}

func readFile(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("contains more than one JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func arrayLength(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, errors.New("field is missing")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return 0, errors.New("must be an array")
	}
	return len(values), nil
}

func gitObject(dir, object string) error { return gitRun(dir, "cat-file", "-e", object+"^{commit}") }

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func gitBytes(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func existingDir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("is not a directory")
	}
	return real, nil
}

func resolvePath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa, bb := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func exactStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
