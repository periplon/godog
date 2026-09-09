package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTaskTimeout = 30 * time.Minute
	retryFeedbackLimit = 32 * 1024
)

// Execute runs plan tasks in isolated Git worktrees and integrates successful
// commits in a separate worktree. It never changes the caller's checkout.
func Execute(ctx context.Context, plan *Plan, opts RunOptions) (*Result, error) {
	if ctx == nil {
		return nil, errors.New("workflow: nil context")
	}
	if err := validatePlan(plan); err != nil {
		return nil, err
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

	repo, baseline, err := inspectRepository(ctx, opts.Dir)
	if err != nil {
		return nil, err
	}
	if plan.Tracking != nil {
		if err := ValidatePlanInputs(ctx, plan, repo); err != nil {
			return nil, err
		}
	}
	output, err := createRunDirectory(repo, opts.OutputDir)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(output, "worktrees"), 0o755); err != nil {
		return nil, fmt.Errorf("workflow: create worktrees directory: %w", err)
	}
	if err := os.Mkdir(filepath.Join(output, "logs"), 0o755); err != nil {
		return nil, fmt.Errorf("workflow: create logs directory: %w", err)
	}
	if err := writeJSONAtomic(filepath.Join(output, "plan.json"), plan); err != nil {
		return nil, fmt.Errorf("workflow: write plan snapshot: %w", err)
	}
	lock, manifest, err := initializeDurableRun(ctx, output, repo, baseline)
	if err != nil {
		return nil, err
	}
	defer lock.release()
	ctx = contextWithExecutionLock(ctx, lock)

	tasks := append([]Task(nil), plan.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	result := &Result{
		Baseline: baseline, Repository: repo, RunDirectory: output, PlanDigest: manifest.PlanDigest,
		Tasks: make([]TaskResult, len(tasks)),
		Runs:  []RunRecord{{Sequence: 1, StartedAt: timestamp(time.Now()), Status: "running"}},
	}
	indices := make(map[string]int, len(tasks))
	states := make(map[string]string, len(tasks))
	for i := range tasks {
		result.Tasks[i] = TaskResult{ID: tasks[i].ID, Status: "pending"}
		indices[tasks[i].ID] = i
		states[tasks[i].ID] = "pending"
	}

	var resultMu sync.Mutex
	var persistErr error
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	persist := func() {
		resultMu.Lock()
		defer resultMu.Unlock()
		if persistErr == nil {
			persistErr = writeJSONAtomic(filepath.Join(output, "result.json"), result)
			if persistErr != nil {
				cancelRun()
			}
		}
	}
	finish := func(status string, runErr error) (*Result, error) {
		finishRunRecord(result, status)
		persist()
		if persistErr != nil {
			if runErr != nil {
				return result, fmt.Errorf("%w (persist result: %v)", runErr, persistErr)
			}
			return result, fmt.Errorf("workflow: persist result: %w", persistErr)
		}
		return result, runErr
	}
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
	persist()
	if persistErr != nil {
		return finish("failed", fmt.Errorf("workflow: persist initial result: %w", persistErr))
	}

	type completion struct {
		id     string
		result TaskResult
	}
	completed := make(chan completion, len(tasks))
	running := 0
	terminal := 0
	cancelled := false

	for terminal < len(tasks) {
		if runCtx.Err() != nil && !cancelled {
			cancelled = true
			cancelError := runCtx.Err().Error()
			if ctx.Err() == nil {
				cancelError = "workflow artifact persistence failed"
			}
			for i := range tasks {
				if states[tasks[i].ID] == "pending" {
					states[tasks[i].ID] = "cancelled"
					terminal++
					update(TaskResult{ID: tasks[i].ID, Status: "cancelled", Error: cancelError, FinishedAt: timestamp(time.Now())})
				}
			}
		}

		if !cancelled {
			changed := true
			for changed {
				changed = false
				for i := range tasks {
					if states[tasks[i].ID] != "pending" {
						continue
					}
					if dependencyFailed(tasks[i], states) {
						states[tasks[i].ID] = "blocked"
						terminal++
						changed = true
						update(TaskResult{ID: tasks[i].ID, Status: "blocked", Error: "a dependency did not succeed", FinishedAt: timestamp(time.Now())})
					}
				}
			}

			for i := range tasks {
				if running >= opts.Jobs {
					break
				}
				if states[tasks[i].ID] != "pending" || !dependenciesSucceeded(tasks[i], states) {
					continue
				}
				task := tasks[i]
				states[task.ID] = "running"
				running++
				started := timestamp(time.Now())
				update(TaskResult{ID: task.ID, Status: "running", StartedAt: started})
				go func(index int) {
					taskResult := executeTask(runCtx, repo, baseline, output, index, task, opts.CodexBinary, result, indices, update)
					completed <- completion{id: task.ID, result: taskResult}
				}(i)
			}
		}

		if running == 0 {
			if terminal == len(tasks) {
				break
			}
			return finish("failed", errors.New("workflow: scheduler made no progress"))
		}
		done := <-completed
		running--
		terminal++
		if done.result.Status == "success" {
			checkpoint, checkpointErr := writeTaskCheckpoint(output, manifest, tasksByID(tasks)[done.id], done.result, result, indices)
			if checkpointErr != nil {
				done.result = finishTask(done.result, "failed", checkpointErr)
				done.result.Commit = ""
			} else {
				done.result.Checkpoint = checkpoint
			}
		}
		states[done.id] = done.result.Status
		update(done.result)
	}

	if persistErr != nil {
		return finish("failed", fmt.Errorf("workflow: persist result: %w", persistErr))
	}
	if ctx.Err() != nil {
		return finish("cancelled", ctx.Err())
	}
	if err := verifyRepositoryUnchanged(ctx, repo, baseline); err != nil {
		return finish("failed", err)
	}

	integrationErr := integrateResults(ctx, repo, baseline, output, result)
	persist()
	if persistErr != nil {
		return finish("failed", fmt.Errorf("workflow: persist result: %w", persistErr))
	}
	if integrationErr != nil {
		return finish("failed", integrationErr)
	}
	if err := verifyRepositoryUnchanged(ctx, repo, baseline); err != nil {
		return finish("failed", err)
	}
	for _, task := range result.Tasks {
		if task.Status != "success" {
			return finish("failed", errors.New("workflow: one or more tasks failed or were blocked"))
		}
	}
	return finish("success", nil)
}

func validatePlan(plan *Plan) error {
	if plan == nil {
		return errors.New("workflow: nil plan")
	}
	if plan.Version != 1 {
		return fmt.Errorf("workflow: unsupported plan version %d", plan.Version)
	}
	if plan.Tracking != nil {
		if err := ValidateTracking(plan); err != nil {
			return err
		}
	}
	if len(plan.Tasks) == 0 && (plan.Tracking == nil || !plan.Tracking.NoOp) {
		return errors.New("workflow: plan must contain at least one task")
	}
	byID := make(map[string]Task, len(plan.Tasks))
	for _, task := range plan.Tasks {
		if !executorSafeTaskID(task.ID) {
			return fmt.Errorf("workflow: task ID %q is not a safe path component", task.ID)
		}
		if _, exists := byID[task.ID]; exists {
			return fmt.Errorf("workflow: duplicate task ID %q", task.ID)
		}
		hasPrompt := strings.TrimSpace(task.Prompt) != ""
		if (len(task.Run) == 0) == !hasPrompt {
			return fmt.Errorf("workflow: task %q must specify exactly one of run or prompt", task.ID)
		}
		if len(task.Run) > 0 && strings.TrimSpace(task.Run[0]) == "" {
			return fmt.Errorf("workflow: task %q has an empty executable", task.ID)
		}
		if hasPrompt && strings.TrimSpace(task.Model) == "" {
			return fmt.Errorf("workflow: task %q has no model", task.ID)
		}
		if task.Attempts < 1 || task.Attempts > 5 {
			return fmt.Errorf("workflow: task %q attempts must be between 1 and 5", task.ID)
		}
		if task.ExpectExit < 0 || task.ExpectExit > 255 {
			return fmt.Errorf("workflow: task %q expected exit must be between 0 and 255", task.ID)
		}
		if hasPrompt && task.ExpectExit != 0 {
			return fmt.Errorf("workflow: prompt task %q must expect exit 0", task.ID)
		}
		if _, err := taskTimeout(task); err != nil {
			return err
		}
		byID[task.ID] = task
	}
	for _, task := range plan.Tasks {
		seen := make(map[string]bool, len(task.Needs))
		for _, need := range task.Needs {
			if _, exists := byID[need]; !exists {
				return fmt.Errorf("workflow: task %q needs unknown task %q", task.ID, need)
			}
			if need == task.ID {
				return fmt.Errorf("workflow: task %q depends on itself", task.ID)
			}
			if seen[need] {
				return fmt.Errorf("workflow: task %q repeats dependency %q", task.ID, need)
			}
			seen[need] = true
		}
	}
	visiting := make(map[string]bool, len(byID))
	visited := make(map[string]bool, len(byID))
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("workflow: dependency cycle contains %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, need := range byID[id].Needs {
			if err := visit(need); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func inspectRepository(ctx context.Context, dir string) (string, string, error) {
	if dir == "" {
		dir = "."
	}
	repoBytes, err := commandOutput(ctx, dir, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("workflow: locate Git repository: %w", err)
	}
	repo, err := filepath.Abs(strings.TrimSpace(string(repoBytes)))
	if err != nil {
		return "", "", fmt.Errorf("workflow: resolve Git repository: %w", err)
	}
	status, err := commandOutput(ctx, repo, "git", "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", "", fmt.Errorf("workflow: inspect Git status: %w", err)
	}
	if len(status) != 0 {
		return "", "", errors.New("workflow: source repository must be clean")
	}
	baselineBytes, err := commandOutput(ctx, repo, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("workflow: resolve baseline: %w", err)
	}
	return repo, strings.TrimSpace(string(baselineBytes)), nil
}

func createRunDirectory(repo, requested string) (string, error) {
	if requested == "" {
		temporaryRoot, err := filepath.EvalSymlinks(os.TempDir())
		if err != nil {
			return "", fmt.Errorf("workflow: resolve temporary directory: %w", err)
		}
		if pathsOverlap(repo, temporaryRoot) {
			return "", errors.New("workflow: temporary output directory must be external to the source repository")
		}
		output, err := os.MkdirTemp(temporaryRoot, "godog-workflow-")
		if err != nil {
			return "", fmt.Errorf("workflow: create temporary output directory: %w", err)
		}
		return output, nil
	}
	abs, err := filepath.Abs(requested)
	if err != nil {
		return "", fmt.Errorf("workflow: resolve output directory: %w", err)
	}
	if _, err := os.Lstat(abs); err == nil {
		return "", fmt.Errorf("workflow: output directory %q already exists", abs)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("workflow: inspect output directory: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("workflow: resolve output parent: %w", err)
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	if pathsOverlap(repo, abs) {
		return "", errors.New("workflow: output directory must be external to the source repository")
	}
	if err := os.Mkdir(abs, 0o755); err != nil {
		return "", fmt.Errorf("workflow: create output directory: %w", err)
	}
	return abs, nil
}

func pathsOverlap(a, b string) bool {
	relAB, errAB := filepath.Rel(a, b)
	relBA, errBA := filepath.Rel(b, a)
	inside := func(rel string, err error) bool {
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return inside(relAB, errAB) || inside(relBA, errBA)
}

func executorSafeTaskID(id string) bool {
	if id == "" || strings.TrimSpace(id) != id || id[0] == '.' || id[len(id)-1] == '.' {
		return false
	}
	for index := 0; index < len(id); index++ {
		char := id[index]
		if index == 0 {
			if char < 'A' || char > 'Z' {
				if char < 'a' || char > 'z' {
					if char < '0' || char > '9' {
						return false
					}
				}
			}
			continue
		}
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func dependencyFailed(task Task, states map[string]string) bool {
	for _, need := range task.Needs {
		if states[need] == "failed" || states[need] == "blocked" || states[need] == "cancelled" {
			return true
		}
	}
	return false
}

func dependenciesSucceeded(task Task, states map[string]string) bool {
	for _, need := range task.Needs {
		if states[need] != "success" {
			return false
		}
	}
	return true
}

func executeTask(parent context.Context, repo, baseline, output string, index int, task Task, codexBinary string, aggregate *Result, indices map[string]int, update func(TaskResult)) TaskResult {
	worktree := filepath.Join(output, "worktrees", fmt.Sprintf("%03d-%s", index+1, safeName(task.ID)))
	logPath := filepath.Join(output, "logs", fmt.Sprintf("%03d-%s.log", index+1, safeName(task.ID)))
	return executeTaskAt(parent, repo, baseline, worktree, logPath, "create worktree", 0, "", task, codexBinary, aggregate, indices, update)
}

func executeTaskAt(parent context.Context, repo, baseline, worktree, logPath, createDescription string, attemptOffset int, priorFeedback string, task Task, codexBinary string, aggregate *Result, indices map[string]int, update func(TaskResult)) TaskResult {
	startedAt := timestamp(time.Now())
	result := TaskResult{ID: task.ID, Status: "running", Attempts: attemptOffset, StartedAt: startedAt}
	if err := appendLog(logPath, []byte(createDescription+": "+worktree+"\n")); err != nil {
		return finishTask(result, "failed", fmt.Errorf("initialize task log: %w", err))
	}
	result.Log = logPath
	update(result)

	timeout, _ := taskTimeout(task)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if outputBytes, err := commandOutput(ctx, repo, "git", "worktree", "add", "--detach", worktree, baseline); err != nil {
		if logErr := appendLog(logPath, append(outputBytes, []byte("create worktree failed: "+err.Error()+"\n")...)); logErr != nil {
			return finishTask(result, "failed", fmt.Errorf("create worktree: %w (append task log: %v)", err, logErr))
		}
		return finishTask(result, "failed", fmt.Errorf("create worktree: %w: %s", err, strings.TrimSpace(string(outputBytes))))
	}
	result.Worktree = worktree
	update(result)

	needs := append([]string(nil), task.Needs...)
	sort.Strings(needs)
	for _, need := range needs {
		dependency := snapshotTaskResult(aggregate, indices[need])
		message := "chore(workflow): integrate dependency " + need
		outputBytes, err := commandOutput(ctx, worktree, "git", "merge", "--no-ff", "--no-gpg-sign", "-m", message, dependency.Commit)
		if logErr := appendLog(logPath, outputBytes); logErr != nil {
			if err != nil {
				return finishTask(result, statusForParent(parent), fmt.Errorf("merge dependency %q: %w (append task log: %v)", need, err, logErr))
			}
			return finishTask(result, "failed", fmt.Errorf("append task log after merging dependency %q: %w", need, logErr))
		}
		if err != nil {
			return finishTask(result, statusForParent(parent), fmt.Errorf("merge dependency %q: %w", need, err))
		}
	}
	setupHeadBytes, err := commandOutput(ctx, worktree, "git", "rev-parse", "HEAD")
	if err != nil {
		return finishTask(result, statusForParent(parent), fmt.Errorf("resolve task base: %w", err))
	}
	setupHead := strings.TrimSpace(string(setupHeadBytes))

	prompt := task.Prompt
	if len(task.Run) == 0 && attemptOffset > 0 && priorFeedback != "" {
		prompt = retryPrompt(task.Prompt, attemptOffset, priorFeedback)
	}
	for attempt := 1; attempt <= task.Attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return finishTask(result, statusForParent(parent), err)
		}
		result.Attempts = attemptOffset + attempt
		update(result)
		tail, exitCode, runErr := runAttempt(ctx, worktree, logPath, task, codexBinary, prompt, result.Attempts)
		if err := ctx.Err(); err != nil {
			return finishTask(result, statusForParent(parent), err)
		}
		if attemptExitMatches(task.ExpectExit, exitCode, runErr) {
			commit, commitErr := commitTask(ctx, worktree, task.ID, setupHead, logPath)
			if commitErr != nil {
				return finishTask(result, statusForParent(parent), commitErr)
			}
			result.Commit = commit
			return finishTask(result, "success", nil)
		}
		if attempt == task.Attempts {
			failure := fmt.Errorf("attempt %d exited %d, expected %d", attempt, exitCode, task.ExpectExit)
			if runErr != nil {
				failure = fmt.Errorf("%w: %v", failure, runErr)
			}
			return finishTask(result, "failed", failure)
		}
		if len(task.Run) == 0 {
			prompt = retryPrompt(task.Prompt, result.Attempts, tail)
		}
	}
	return finishTask(result, "failed", errors.New("attempt loop ended unexpectedly"))
}

func snapshotTaskResult(result *Result, index int) TaskResult {
	// Dependency results are terminal before a dependent task can be launched;
	// no writer can mutate this entry after that transition.
	return result.Tasks[index]
}

func runAttempt(ctx context.Context, worktree, logPath string, task Task, codexBinary, prompt string, attempt int) (string, int, error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", -1, fmt.Errorf("open task log: %w", err)
	}
	if _, err := fmt.Fprintf(logFile, "\n=== attempt %d ===\n", attempt); err != nil {
		closeErr := logFile.Close()
		if closeErr != nil {
			return "", -1, fmt.Errorf("write task log header: %w (close task log: %v)", err, closeErr)
		}
		return "", -1, fmt.Errorf("write task log header: %w", err)
	}
	tail := &tailBuffer{limit: retryFeedbackLimit}
	var cmd *exec.Cmd
	if len(task.Run) == 0 {
		cmd = exec.Command(codexBinary, "exec", "-m", task.Model, "--dangerously-bypass-approvals-and-sandbox", "--", prompt)
	} else {
		cmd = exec.Command(task.Run[0], task.Run[1:]...)
	}
	cmd.Dir = worktree
	logOutput := &errorRecordingWriter{writer: logFile}
	combined := io.MultiWriter(logOutput, tail)
	cmd.Stdout = combined
	cmd.Stderr = combined
	runErr := runTaskCommand(ctx, cmd)
	exitCode := commandExitCode(runErr)
	closeErr := logFile.Close()
	if logOutput.err != nil {
		if closeErr != nil {
			return tail.String(), exitCode, fmt.Errorf("write task log output: %w (close task log: %v)", logOutput.err, closeErr)
		}
		return tail.String(), exitCode, fmt.Errorf("write task log output: %w", logOutput.err)
	}
	if closeErr != nil {
		return tail.String(), exitCode, fmt.Errorf("close task log: %w", closeErr)
	}
	return tail.String(), exitCode, runErr
}

func commitTask(ctx context.Context, worktree, taskID, setupHead, logPath string) (string, error) {
	if output, err := commandOutput(ctx, worktree, "git", "add", "--all"); err != nil {
		if logErr := appendLog(logPath, output); logErr != nil {
			return "", fmt.Errorf("stage task changes: %w (append task log: %v)", err, logErr)
		}
		return "", fmt.Errorf("stage task changes: %w", err)
	}
	staged, err := commandOutput(ctx, worktree, "git", "diff", "--cached", "--quiet")
	if err != nil {
		if commandExitCode(err) != 1 {
			if logErr := appendLog(logPath, staged); logErr != nil {
				return "", fmt.Errorf("inspect staged task changes: %w (append task log: %v)", err, logErr)
			}
			return "", fmt.Errorf("inspect staged task changes: %w", err)
		}
		message := "chore(workflow): complete task " + taskID
		if output, commitErr := commandOutput(ctx, worktree, "git", "commit", "--no-gpg-sign", "-m", message); commitErr != nil {
			if logErr := appendLog(logPath, output); logErr != nil {
				return "", fmt.Errorf("commit task changes: %w (append task log: %v)", commitErr, logErr)
			}
			return "", fmt.Errorf("commit task changes: %w", commitErr)
		}
	}
	headBytes, err := commandOutput(ctx, worktree, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve task commit: %w", err)
	}
	head := strings.TrimSpace(string(headBytes))
	if output, ancestorErr := commandOutput(ctx, worktree, "git", "merge-base", "--is-ancestor", setupHead, head); ancestorErr != nil {
		if logErr := appendLog(logPath, output); logErr != nil {
			return "", fmt.Errorf("task rewrote history and discarded its baseline or dependencies (append task log: %v)", logErr)
		}
		return "", errors.New("task rewrote history and discarded its baseline or dependencies")
	}
	return head, nil
}

func integrateResults(ctx context.Context, repo, baseline, output string, result *Result) error {
	worktree := filepath.Join(output, "integration")
	result.IntegrationWorktree = worktree
	outputBytes, err := commandOutput(ctx, repo, "git", "worktree", "add", "--detach", worktree, baseline)
	if err != nil {
		return fmt.Errorf("workflow: create integration worktree: %w: %s", err, strings.TrimSpace(string(outputBytes)))
	}
	for _, task := range result.Tasks {
		if task.Status != "success" || task.Commit == "" {
			continue
		}
		_, ancestorErr := commandOutput(ctx, worktree, "git", "merge-base", "--is-ancestor", task.Commit, "HEAD")
		if ancestorErr == nil {
			continue
		}
		if commandExitCode(ancestorErr) != 1 {
			return fmt.Errorf("workflow: inspect integration ancestry for task %q: %w", task.ID, ancestorErr)
		}
		message := "chore(workflow): integrate task " + task.ID
		mergeOutput, mergeErr := commandOutput(ctx, worktree, "git", "merge", "--no-ff", "--no-gpg-sign", "-m", message, task.Commit)
		if mergeErr != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("workflow: integration cancelled for task %q: %w", task.ID, ctx.Err())
			}
			unmerged, inspectErr := commandOutput(ctx, worktree, "git", "diff", "--name-only", "--diff-filter=U")
			if inspectErr == nil && len(bytes.TrimSpace(unmerged)) > 0 {
				return fmt.Errorf("workflow: integration conflict for task %q: %w: %s", task.ID, mergeErr, strings.TrimSpace(string(mergeOutput)))
			}
			return fmt.Errorf("workflow: integration merge failed for task %q: %w: %s", task.ID, mergeErr, strings.TrimSpace(string(mergeOutput)))
		}
	}
	headBytes, err := commandOutput(ctx, worktree, "git", "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("workflow: resolve integration commit: %w", err)
	}
	result.Commit = strings.TrimSpace(string(headBytes))
	return nil
}

func taskTimeout(task Task) (time.Duration, error) {
	if task.Timeout == "" {
		return defaultTaskTimeout, nil
	}
	timeout, err := time.ParseDuration(task.Timeout)
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("workflow: task %q has invalid timeout %q", task.ID, task.Timeout)
	}
	return timeout, nil
}

func retryPrompt(original string, attempt int, failure string) string {
	return original + "\n\nThe previous attempt (" + strconv.Itoa(attempt) + ") failed. Use this bounded command output to diagnose and fix the same task:\n\n" + failure
}

func finishTask(result TaskResult, status string, err error) TaskResult {
	result.Status = status
	result.FinishedAt = timestamp(time.Now())
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func statusForParent(parent context.Context) string {
	if parent.Err() != nil {
		return "cancelled"
	}
	return "failed"
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func attemptExitMatches(expected, actual int, err error) bool {
	if actual != expected {
		return false
	}
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == actual
}

func commandOutput(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := runTaskCommand(ctx, cmd)
	return output.Bytes(), err
}

func verifyRepositoryUnchanged(ctx context.Context, repo, baseline string) error {
	head, err := commandOutput(ctx, repo, "git", "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("workflow: verify source HEAD: %w", err)
	}
	if strings.TrimSpace(string(head)) != baseline {
		return errors.New("workflow: source repository HEAD changed during execution")
	}
	status, err := commandOutput(ctx, repo, "git", "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("workflow: verify source status: %w", err)
	}
	if len(status) != 0 {
		return errors.New("workflow: source repository became dirty during execution")
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	remove := true
	defer func() {
		if remove {
			os.Remove(temporaryName)
		}
	}()
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	remove = false
	return nil
}

func appendLog(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		closeErr := file.Close()
		if closeErr != nil {
			return fmt.Errorf("write: %w (close: %v)", err, closeErr)
		}
		return err
	}
	return file.Close()
}

type errorRecordingWriter struct {
	writer io.Writer
	err    error
}

func (writer *errorRecordingWriter) Write(contents []byte) (int, error) {
	written, err := writer.writer.Write(contents)
	if err == nil && written != len(contents) {
		err = io.ErrShortWrite
	}
	if err != nil && writer.err == nil {
		writer.err = err
	}
	return written, err
}

func timestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func safeName(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

type tailBuffer struct {
	limit int
	data  []byte
}

func (buffer *tailBuffer) Write(contents []byte) (int, error) {
	buffer.data = append(buffer.data, contents...)
	if len(buffer.data) > buffer.limit {
		buffer.data = append([]byte(nil), buffer.data[len(buffer.data)-buffer.limit:]...)
	}
	return len(contents), nil
}

func (buffer *tailBuffer) String() string {
	return string(bytes.TrimSpace(buffer.data))
}
