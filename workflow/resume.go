package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const durableRunVersion = 1

type durableRunManifest struct {
	Version      int    `json:"version"`
	Repository   string `json:"repository"`
	GitCommonDir string `json:"git_common_dir"`
	Baseline     string `json:"baseline"`
	PlanDigest   string `json:"plan_digest"`
	CreatedAt    string `json:"created_at"`
}

type taskCompletionCheckpoint struct {
	Version      int               `json:"version"`
	PlanDigest   string            `json:"plan_digest"`
	Baseline     string            `json:"baseline"`
	LogDigest    string            `json:"log_digest"`
	Task         TaskResult        `json:"task"`
	Dependencies map[string]string `json:"dependencies"`
}

// Resume continues a durable workflow run. Successful tasks with validated
// completion checkpoints are reused; every other task receives a fresh copy of
// its configured attempt budget in a newly reconstructed worktree.
func Resume(ctx context.Context, outputDir string, opts RunOptions) (*Result, error) {
	if ctx == nil {
		return nil, errors.New("workflow: nil context")
	}
	output, err := existingRunDirectory(outputDir)
	if err != nil {
		return nil, err
	}
	lock, err := acquireExecutionLock(filepath.Join(output, "run.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.release()
	ctx = contextWithExecutionLock(ctx, lock)

	manifest, err := readRunManifest(output)
	if err != nil {
		return nil, err
	}
	plan, digest, err := readDurablePlan(output)
	if err != nil {
		return nil, err
	}
	if digest != manifest.PlanDigest {
		return nil, errors.New("workflow: plan.json does not match immutable run manifest")
	}
	if err := validatePlan(plan); err != nil {
		return nil, fmt.Errorf("workflow: saved plan is invalid: %w", err)
	}

	repoDir := opts.Dir
	if repoDir == "" {
		repoDir = manifest.Repository
	}
	repo, baseline, err := inspectRepository(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	commonDir, err := repositoryCommonDir(ctx, repo)
	if err != nil {
		return nil, err
	}
	if repo != manifest.Repository || commonDir != manifest.GitCommonDir || baseline != manifest.Baseline {
		return nil, errors.New("workflow: source repository identity or baseline does not match immutable run manifest")
	}
	if opts.Jobs < 0 {
		return nil, errors.New("workflow: jobs must not be negative")
	}
	if opts.Jobs == 0 {
		opts.Jobs = 1
	}
	if opts.CodexBinary == "" {
		opts.CodexBinary = "codex"
	}

	result, err := readDurableResult(output)
	if err != nil {
		return nil, err
	}
	if result.Repository != manifest.Repository || result.RunDirectory != output || result.Baseline != manifest.Baseline || result.PlanDigest != manifest.PlanDigest {
		return nil, errors.New("workflow: result identity does not match immutable run manifest")
	}
	if err := validateRunHistory(result.Runs); err != nil {
		return nil, err
	}
	tasks := append([]Task(nil), plan.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	indices, err := validateSavedTaskResults(tasks, result.Tasks)
	if err != nil {
		return nil, err
	}
	closeInterruptedRun(result)
	if err := recoverCompletedTasks(ctx, output, manifest, tasks, result, indices); err != nil {
		return nil, err
	}

	sequence := len(result.Runs) + 1
	result.Runs = append(result.Runs, RunRecord{Sequence: sequence, Resumed: true, StartedAt: timestamp(time.Now()), Status: "running"})
	if err := writeJSONAtomic(filepath.Join(output, "result.json"), result); err != nil {
		return nil, fmt.Errorf("workflow: persist resume start: %w", err)
	}
	result, runErr := resumeTasks(ctx, plan, opts, repo, output, manifest, result, indices, sequence)
	status := "success"
	if runErr != nil {
		status = "failed"
		if ctx.Err() != nil {
			status = "cancelled"
		}
	}
	finishRunRecord(result, status)
	if err := writeJSONAtomic(filepath.Join(output, "result.json"), result); err != nil && runErr == nil {
		runErr = fmt.Errorf("workflow: persist resume result: %w", err)
	}
	return result, runErr
}

func initializeDurableRun(ctx context.Context, output, repo, baseline string) (*executionLock, durableRunManifest, error) {
	lock, err := acquireExecutionLock(filepath.Join(output, "run.lock"))
	if err != nil {
		return nil, durableRunManifest{}, err
	}
	if err := os.Mkdir(filepath.Join(output, "checkpoints"), 0o755); err != nil {
		lock.release()
		return nil, durableRunManifest{}, fmt.Errorf("workflow: create checkpoints directory: %w", err)
	}
	digest, err := digestFile(filepath.Join(output, "plan.json"))
	if err != nil {
		lock.release()
		return nil, durableRunManifest{}, fmt.Errorf("workflow: digest plan snapshot: %w", err)
	}
	commonDir, err := repositoryCommonDir(ctx, repo)
	if err != nil {
		lock.release()
		return nil, durableRunManifest{}, err
	}
	manifest := durableRunManifest{
		Version: durableRunVersion, Repository: repo, GitCommonDir: commonDir,
		Baseline: baseline, PlanDigest: digest, CreatedAt: timestamp(time.Now()),
	}
	manifestPath := filepath.Join(output, "run.json")
	if err := writeJSONAtomic(manifestPath, manifest); err != nil {
		lock.release()
		return nil, durableRunManifest{}, fmt.Errorf("workflow: write run manifest: %w", err)
	}
	_ = os.Chmod(manifestPath, 0o444)
	return lock, manifest, nil
}

func resumeTasks(ctx context.Context, plan *Plan, opts RunOptions, repo, output string, manifest durableRunManifest, result *Result, indices map[string]int, sequence int) (*Result, error) {
	tasks := append([]Task(nil), plan.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	states := make(map[string]string, len(tasks))
	terminal := 0
	for _, task := range tasks {
		current := result.Tasks[indices[task.ID]]
		if current.Status == "success" {
			states[task.ID] = "success"
			terminal++
		} else {
			current.Status = "pending"
			current.Error = ""
			current.Commit = ""
			current.Checkpoint = ""
			current.StartedAt = ""
			current.FinishedAt = ""
			result.Tasks[indices[task.ID]] = current
			states[task.ID] = "pending"
		}
	}

	var resultMu sync.Mutex
	var persistErr error
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	update := func(taskResult TaskResult) {
		resultMu.Lock()
		result.Tasks[indices[taskResult.ID]] = taskResult
		if persistErr == nil {
			persistErr = writeJSONAtomic(filepath.Join(output, "result.json"), result)
			if persistErr != nil {
				cancelRun()
			}
		}
		resultMu.Unlock()
	}

	type completion struct {
		id     string
		result TaskResult
	}
	completed := make(chan completion, len(tasks))
	running := 0
	cancelled := false
	for terminal < len(tasks) {
		if runCtx.Err() != nil && !cancelled {
			cancelled = true
			for _, task := range tasks {
				if states[task.ID] == "pending" {
					current := result.Tasks[indices[task.ID]]
					current.Status = "cancelled"
					current.Error = runCtx.Err().Error()
					current.FinishedAt = timestamp(time.Now())
					states[task.ID] = "cancelled"
					terminal++
					update(current)
				}
			}
		}
		if !cancelled {
			changed := true
			for changed {
				changed = false
				for _, task := range tasks {
					if states[task.ID] == "pending" && dependencyFailed(task, states) {
						current := result.Tasks[indices[task.ID]]
						current.Status = "blocked"
						current.Error = "a dependency did not succeed"
						current.FinishedAt = timestamp(time.Now())
						states[task.ID] = "blocked"
						terminal++
						changed = true
						update(current)
					}
				}
			}
			for index, task := range tasks {
				if running >= opts.Jobs {
					break
				}
				if states[task.ID] != "pending" || !dependenciesSucceeded(task, states) {
					continue
				}
				prior := result.Tasks[indices[task.ID]]
				priorFeedback := readLogTail(prior.Log, retryFeedbackLimit)
				states[task.ID] = "running"
				running++
				go func(index int, task Task, prior TaskResult, priorFeedback string) {
					worktree := filepath.Join(output, "worktrees", fmt.Sprintf("%03d-%s-resume-%d", index+1, safeName(task.ID), sequence))
					logPath := filepath.Join(output, "logs", fmt.Sprintf("%03d-%s-resume-%d.log", index+1, safeName(task.ID), sequence))
					taskResult := executeTaskAt(runCtx, repo, manifest.Baseline, worktree, logPath, "create resumed worktree", prior.Attempts, priorFeedback, task, opts.CodexBinary, result, indices, update)
					completed <- completion{id: task.ID, result: taskResult}
				}(index, task, prior, priorFeedback)
			}
		}
		if running == 0 {
			if terminal == len(tasks) {
				break
			}
			return result, errors.New("workflow: resume scheduler made no progress")
		}
		done := <-completed
		running--
		terminal++
		if done.result.Status == "success" {
			checkpoint, checkpointErr := writeTaskCheckpoint(output, manifest, tasksByID(tasks)[done.id], done.result, result, indices)
			if checkpointErr != nil {
				done.result.Status = "failed"
				done.result.Error = checkpointErr.Error()
				done.result.Commit = ""
			} else {
				done.result.Checkpoint = checkpoint
			}
		}
		states[done.id] = done.result.Status
		update(done.result)
	}
	if persistErr != nil {
		return result, fmt.Errorf("workflow: persist resumed result: %w", persistErr)
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err := verifyRepositoryUnchanged(ctx, repo, manifest.Baseline); err != nil {
		return result, err
	}
	for _, task := range result.Tasks {
		if task.Status != "success" {
			if _, err := integrateResumedResults(ctx, repo, output, manifest.Baseline, result, sequence); err != nil {
				return result, err
			}
			return result, errors.New("workflow: one or more resumed tasks failed or were blocked")
		}
	}
	changed, err := integrateResumedResults(ctx, repo, output, manifest.Baseline, result, sequence)
	_ = changed
	if err != nil {
		return result, err
	}
	if err := verifyRepositoryUnchanged(ctx, repo, manifest.Baseline); err != nil {
		return result, err
	}
	return result, nil
}

func writeTaskCheckpoint(output string, manifest durableRunManifest, task Task, taskResult TaskResult, result *Result, indices map[string]int) (string, error) {
	path := checkpointPath(output, task.ID)
	taskResult.Checkpoint = path
	dependencies := make(map[string]string, len(task.Needs))
	for _, need := range task.Needs {
		dependencies[need] = result.Tasks[indices[need]].Commit
	}
	checkpoint := taskCompletionCheckpoint{
		Version: durableRunVersion, PlanDigest: manifest.PlanDigest, Baseline: manifest.Baseline,
		Task: taskResult, Dependencies: dependencies,
	}
	logDigest, err := digestFile(taskResult.Log)
	if err != nil {
		return "", fmt.Errorf("digest task %s log: %w", task.ID, err)
	}
	checkpoint.LogDigest = logDigest
	if err := writeJSONAtomic(path, checkpoint); err != nil {
		return "", fmt.Errorf("persist task %s completion checkpoint: %w", task.ID, err)
	}
	return path, nil
}

func recoverCompletedTasks(ctx context.Context, output string, manifest durableRunManifest, tasks []Task, result *Result, indices map[string]int) error {
	checkpoints := make(map[string]taskCompletionCheckpoint)
	for _, task := range tasks {
		path := checkpointPath(output, task.ID)
		var checkpoint taskCompletionCheckpoint
		err := readJSONFile(path, &checkpoint)
		if errors.Is(err, os.ErrNotExist) {
			if result.Tasks[indices[task.ID]].Status == "success" {
				return fmt.Errorf("workflow: successful task %s lacks completion checkpoint", task.ID)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("workflow: read task %s checkpoint: %w", task.ID, err)
		}
		if checkpoint.Version != durableRunVersion || checkpoint.PlanDigest != manifest.PlanDigest || checkpoint.Baseline != manifest.Baseline || checkpoint.LogDigest == "" || checkpoint.Task.ID != task.ID || checkpoint.Task.Status != "success" || checkpoint.Task.Commit == "" || checkpoint.Task.Checkpoint != path {
			return fmt.Errorf("workflow: task %s checkpoint identity is invalid", task.ID)
		}
		journal := result.Tasks[indices[task.ID]]
		if journal.Status == "success" && (journal.Commit != checkpoint.Task.Commit || journal.Worktree != checkpoint.Task.Worktree || journal.Attempts != checkpoint.Task.Attempts || journal.Checkpoint != path) {
			return fmt.Errorf("workflow: task %s result conflicts with completion checkpoint", task.ID)
		}
		if journal.Status != "success" && journal.Attempts > checkpoint.Task.Attempts {
			return fmt.Errorf("workflow: task %s result attempts conflict with completion checkpoint", task.ID)
		}
		if err := validateCheckpointGit(ctx, output, manifest, task, checkpoint); err != nil {
			return err
		}
		checkpoints[task.ID] = checkpoint
		result.Tasks[indices[task.ID]] = checkpoint.Task
	}
	for id, checkpoint := range checkpoints {
		task := tasksByID(tasks)[id]
		if len(checkpoint.Dependencies) != len(task.Needs) {
			return fmt.Errorf("workflow: task %s checkpoint dependencies are invalid", id)
		}
		for _, need := range task.Needs {
			dependency, ok := checkpoints[need]
			if !ok || checkpoint.Dependencies[need] != dependency.Task.Commit {
				return fmt.Errorf("workflow: task %s checkpoint dependency %s is invalid", id, need)
			}
			if err := gitAncestor(ctx, manifest.Repository, dependency.Task.Commit, checkpoint.Task.Commit); err != nil {
				return fmt.Errorf("workflow: task %s checkpoint does not contain dependency %s: %w", id, need, err)
			}
		}
	}
	return nil
}

func validateCheckpointGit(ctx context.Context, output string, manifest durableRunManifest, task Task, checkpoint taskCompletionCheckpoint) error {
	worktree, err := filepath.EvalSymlinks(checkpoint.Task.Worktree)
	if err != nil || !pathWithin(filepath.Join(output, "worktrees"), worktree) {
		return fmt.Errorf("workflow: task %s checkpoint worktree is invalid", task.ID)
	}
	head, err := commandOutput(ctx, worktree, "git", "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != checkpoint.Task.Commit {
		return fmt.Errorf("workflow: task %s checkpoint worktree HEAD is invalid", task.ID)
	}
	status, err := commandOutput(ctx, worktree, "git", "status", "--porcelain", "--untracked-files=all")
	if err != nil || len(status) != 0 {
		return fmt.Errorf("workflow: task %s checkpoint worktree is not clean", task.ID)
	}
	if err := gitAncestor(ctx, manifest.Repository, manifest.Baseline, checkpoint.Task.Commit); err != nil {
		return fmt.Errorf("workflow: task %s checkpoint does not contain baseline: %w", task.ID, err)
	}
	logPath, err := filepath.EvalSymlinks(checkpoint.Task.Log)
	if err != nil || !pathWithin(filepath.Join(output, "logs"), logPath) {
		return fmt.Errorf("workflow: task %s checkpoint log is invalid", task.ID)
	}
	info, err := os.Stat(logPath)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("workflow: task %s checkpoint log is invalid", task.ID)
	}
	logDigest, err := digestFile(logPath)
	if err != nil || logDigest != checkpoint.LogDigest {
		return fmt.Errorf("workflow: task %s checkpoint log was altered", task.ID)
	}
	return nil
}

func integrateResumedResults(ctx context.Context, repo, output, baseline string, result *Result, sequence int) (bool, error) {
	priorCompleted := len(result.Runs) >= 2 && result.Runs[len(result.Runs)-2].Status == "success"
	if result.IntegrationWorktree != "" {
		worktree, err := filepath.EvalSymlinks(result.IntegrationWorktree)
		if err != nil || !pathWithin(output, worktree) {
			return false, errors.New("workflow: recorded integration worktree is invalid")
		}
		status, statusErr := commandOutput(ctx, worktree, "git", "status", "--porcelain", "--untracked-files=all")
		if statusErr != nil {
			return false, fmt.Errorf("workflow: inspect recorded integration worktree: %w", statusErr)
		}
		if len(status) == 0 && result.Commit != "" {
			head, headErr := commandOutput(ctx, worktree, "git", "rev-parse", "HEAD")
			if headErr == nil && strings.TrimSpace(string(head)) == result.Commit {
				allContained := true
				for _, task := range result.Tasks {
					if task.Status == "success" && gitAncestor(ctx, repo, task.Commit, result.Commit) != nil {
						allContained = false
						break
					}
				}
				if allContained {
					return false, nil
				}
			}
		}
		if priorCompleted {
			return false, errors.New("workflow: completed integration worktree no longer matches its recorded result")
		}
		// A changed, incomplete, or conflicted integration is preserved as
		// evidence. Reconstruct from immutable task checkpoints alongside it.
	}
	worktree := filepath.Join(output, fmt.Sprintf("integration-resume-%d", sequence))
	if _, err := os.Stat(worktree); err == nil {
		return false, errors.New("workflow: resumed integration worktree already exists")
	}
	if outputBytes, err := commandOutput(ctx, repo, "git", "worktree", "add", "--detach", worktree, baseline); err != nil {
		return false, fmt.Errorf("workflow: create resumed integration worktree: %w: %s", err, strings.TrimSpace(string(outputBytes)))
	}
	result.IntegrationWorktree = worktree
	result.Commit = baseline
	if err := mergeSuccessfulTasks(ctx, worktree, result); err != nil {
		return false, err
	}
	return true, nil
}

func mergeSuccessfulTasks(ctx context.Context, worktree string, result *Result) error {
	for _, task := range result.Tasks {
		if task.Status != "success" || task.Commit == "" {
			continue
		}
		if gitAncestor(ctx, worktree, task.Commit, "HEAD") == nil {
			continue
		}
		message := "chore(workflow): integrate resumed task " + task.ID
		if output, err := commandOutput(ctx, worktree, "git", "merge", "--no-ff", "--no-gpg-sign", "-m", message, task.Commit); err != nil {
			return fmt.Errorf("workflow: resumed integration merge failed for task %q: %w: %s", task.ID, err, strings.TrimSpace(string(output)))
		}
	}
	head, err := commandOutput(ctx, worktree, "git", "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("workflow: resolve resumed integration commit: %w", err)
	}
	result.Commit = strings.TrimSpace(string(head))
	return nil
}

func readRunManifest(output string) (durableRunManifest, error) {
	var manifest durableRunManifest
	if err := readJSONFile(filepath.Join(output, "run.json"), &manifest); err != nil {
		return manifest, fmt.Errorf("workflow: read run manifest: %w", err)
	}
	if manifest.Version != durableRunVersion || manifest.Repository == "" || manifest.GitCommonDir == "" || manifest.Baseline == "" || manifest.PlanDigest == "" {
		return manifest, errors.New("workflow: run manifest is invalid")
	}
	return manifest, nil
}

func readDurablePlan(output string) (*Plan, string, error) {
	path := filepath.Join(output, "plan.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("workflow: read plan snapshot: %w", err)
	}
	var plan Plan
	if err := json.Unmarshal(contents, &plan); err != nil {
		return nil, "", fmt.Errorf("workflow: decode plan snapshot: %w", err)
	}
	return &plan, digestBytes(contents), nil
}

func readDurableResult(output string) (*Result, error) {
	var result Result
	if err := readJSONFile(filepath.Join(output, "result.json"), &result); err != nil {
		return nil, fmt.Errorf("workflow: read result journal: %w", err)
	}
	return &result, nil
}

func validateSavedTaskResults(tasks []Task, results []TaskResult) (map[string]int, error) {
	if len(tasks) != len(results) {
		return nil, errors.New("workflow: result task set does not match saved plan")
	}
	wanted := tasksByID(tasks)
	indices := make(map[string]int, len(results))
	for index, result := range results {
		if _, ok := wanted[result.ID]; !ok {
			return nil, fmt.Errorf("workflow: result contains unknown task %q", result.ID)
		}
		if _, duplicate := indices[result.ID]; duplicate {
			return nil, fmt.Errorf("workflow: result repeats task %q", result.ID)
		}
		indices[result.ID] = index
	}
	return indices, nil
}

func validateRunHistory(runs []RunRecord) error {
	if len(runs) == 0 {
		return errors.New("workflow: run history is empty")
	}
	for index, run := range runs {
		if run.Sequence != index+1 || run.StartedAt == "" || run.Resumed != (index > 0) {
			return errors.New("workflow: run history is invalid")
		}
		switch run.Status {
		case "running":
			if index != len(runs)-1 || run.FinishedAt != "" {
				return errors.New("workflow: run history is invalid")
			}
		case "success", "failed", "cancelled", "interrupted":
			if run.FinishedAt == "" {
				return errors.New("workflow: run history is invalid")
			}
		default:
			return errors.New("workflow: run history is invalid")
		}
	}
	return nil
}

func finishRunRecord(result *Result, status string) {
	if len(result.Runs) == 0 {
		return
	}
	index := len(result.Runs) - 1
	result.Runs[index].FinishedAt = timestamp(time.Now())
	result.Runs[index].Status = status
	result.Runs[index].Tasks = append([]TaskResult(nil), result.Tasks...)
}

func closeInterruptedRun(result *Result) {
	if len(result.Runs) == 0 {
		return
	}
	index := len(result.Runs) - 1
	if result.Runs[index].Status != "running" {
		return
	}
	result.Runs[index].Status = "interrupted"
	result.Runs[index].FinishedAt = timestamp(time.Now())
	result.Runs[index].Tasks = append([]TaskResult(nil), result.Tasks...)
}

func checkpointPath(output, taskID string) string {
	return filepath.Join(output, "checkpoints", digestBytes([]byte(taskID))+".json")
}

func repositoryCommonDir(ctx context.Context, repo string) (string, error) {
	contents, err := commandOutput(ctx, repo, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("workflow: resolve Git common directory: %w", err)
	}
	path := strings.TrimSpace(string(contents))
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("workflow: resolve Git common directory: %w", err)
	}
	return real, nil
}

func existingRunDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("workflow: resume output directory is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("workflow: resolve resume output directory: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("workflow: resolve resume output directory: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", errors.New("workflow: resume output path is not a directory")
	}
	return real, nil
}

func readJSONFile(path string, value any) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(contents, value); err != nil {
		return err
	}
	return nil
}

func digestFile(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return digestBytes(contents), nil
}

func digestBytes(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func readLogTail(path string, limit int) string {
	if path == "" || limit <= 0 {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	start := info.Size() - int64(limit)
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, 0); err != nil {
		return ""
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(limit)))
	if err != nil {
		return ""
	}
	return string(contents)
}

func tasksByID(tasks []Task) map[string]Task {
	result := make(map[string]Task, len(tasks))
	for _, task := range tasks {
		result[task.ID] = task
	}
	return result
}

func gitAncestor(ctx context.Context, repo, ancestor, descendant string) error {
	_, err := commandOutput(ctx, repo, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	return err
}

func pathWithin(parent, child string) bool {
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(parentReal, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
