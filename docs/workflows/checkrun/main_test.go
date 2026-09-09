package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog/workflow"
)

const completeCompilerReview = `{"findings":[{"id":"C-1","severity":"high","file":"workflow/compiler.go","description":"compiler defect","reproduction":"go test ./workflow"}],"scope":"compiler and CLI","evidence":"go test ./workflow"}`
const structuredCompilerReview = `{"findings":[{"id":"C-1","severity":"high","file":"workflow/compiler.go","description":"compiler defect","reproduction":"go test ./workflow"}],"checked_scope":["compiler and CLI"],"test_evidence":[{"command":"go test ./workflow","result":"pass","details":"workflow tests passed"}]}`

func TestCheckAcceptsCompleteSelfHostEvidence(t *testing.T) {
	runDir := makeFixture(t)
	if err := check(runDir); err != nil {
		t.Fatalf("complete evidence rejected: %v", err)
	}
}

func TestCheckAcceptsStructuredReviewEvidence(t *testing.T) {
	runDir := makeFixtureWithOptions(t, fixtureOptions{
		compilerReview:   structuredCompilerReview,
		hardenProduction: true,
	})
	if err := check(runDir); err != nil {
		t.Fatalf("structured review evidence rejected: %v", err)
	}
}

func TestCheckRejectsIncompleteStructuredReviewEvidence(t *testing.T) {
	tests := []struct {
		name   string
		review string
		want   string
	}{
		{
			name:   "empty checked scope",
			review: `{"findings":[{"id":"C-1","severity":"high","file":"workflow/compiler.go","description":"compiler defect","reproduction":"go test ./workflow"}],"checked_scope":[""],"test_evidence":[{"command":"go test ./workflow","result":"pass","details":"passed"}]}`,
			want:   "checked_scope",
		},
		{
			name:   "empty test command",
			review: `{"findings":[{"id":"C-1","severity":"high","file":"workflow/compiler.go","description":"compiler defect","reproduction":"go test ./workflow"}],"checked_scope":["compiler"],"test_evidence":[{"command":"","result":"pass","details":"passed"}]}`,
			want:   "command",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runDir := makeFixtureWithOptions(t, fixtureOptions{
				compilerReview:   test.review,
				hardenProduction: true,
			})
			if err := check(runDir); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestCheckRejectsMalformedEvidence(t *testing.T) {
	tests := []struct {
		name string
		edit func(t *testing.T, runDir string)
		want string
	}{
		{"missing task", func(t *testing.T, d string) {
			updateJSON(t, filepath.Join(d, "result.json"), func(v map[string]any) {
				v["tasks"] = v["tasks"].([]any)[:6]
			})
		}, "task IDs"},
		{"failed task", func(t *testing.T, d string) {
			updateTask(t, d, "verify", func(v map[string]any) { v["status"] = "failed" })
		}, "verify status"},
		{"nonoverlapping initial agents", func(t *testing.T, d string) {
			updateTask(t, d, "harden", func(v map[string]any) {
				v["started_at"] = "2026-09-09T10:02:00Z"
				v["finished_at"] = "2026-09-09T10:03:00Z"
			})
		}, "did not overlap"},
		{"missing model evidence", func(t *testing.T, d string) {
			p := taskLog(t, d, "acceptance")
			mustWrite(t, p, "provider: openai\n")
		}, "model"},
		{"unresolved review", func(t *testing.T, d string) {
			commitIntegrationFile(t, d, ".workflow-review/resolution.json", `{"unresolved":["C-1"],"resolutions":[]}`)
		}, "unresolved"},
		{"missing disposition evidence", func(t *testing.T, d string) {
			commitIntegrationFile(t, d, ".workflow-review/resolution.json", `{"unresolved":[],"resolutions":[{"id":"C-1","status":"fixed","evidence":""}]}`)
		}, "lacks evidence"},
		{"invalid task commit", func(t *testing.T, d string) {
			updateTask(t, d, "harden", func(v map[string]any) { v["commit"] = "not-a-commit" })
		}, "harden commit"},
		{"no test change", func(t *testing.T, d string) {
			baseline := resultBaseline(t, d)
			updateTask(t, d, "acceptance", func(v map[string]any) { v["commit"] = baseline })
		}, "acceptance commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := makeFixture(t)
			tt.edit(t, d)
			err := check(d)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestCheckRejectsFalsePositiveEvidence(t *testing.T) {
	t.Run("recorded plan differs from baseline compilation", func(t *testing.T) {
		d := makeFixture(t)
		updateJSON(t, filepath.Join(d, "plan.json"), func(v map[string]any) {
			for _, raw := range v["tasks"].([]any) {
				task := raw.(map[string]any)
				if task["id"] == "acceptance" {
					task["prompt"] = "perform something else"
				}
			}
		})
		if err := check(d); err == nil || !strings.Contains(err.Error(), "baseline plan") {
			t.Fatalf("got %v, want baseline plan mismatch", err)
		}
	})

	t.Run("consumer commit lacks dependency", func(t *testing.T) {
		d := makeFixture(t)
		baseline := resultBaseline(t, d)
		updateTask(t, d, "reconcile", func(v map[string]any) { v["commit"] = baseline })
		if err := check(d); err == nil || !strings.Contains(err.Error(), "dependency") {
			t.Fatalf("got %v, want dependency ancestry error", err)
		}
	})

	t.Run("review artifact absent from producer commit", func(t *testing.T) {
		d := makeFixture(t)
		worktree := taskWorktree(t, d, "review-compiler")
		beforeArtifact := mustRun(t, worktree, "git", "rev-parse", "HEAD^")
		updateTask(t, d, "review-compiler", func(v map[string]any) { v["commit"] = beforeArtifact })
		if err := check(d); err == nil || !strings.Contains(err.Error(), "compiler.json") {
			t.Fatalf("got %v, want producer artifact error", err)
		}
	})

	t.Run("review finding lacks required schema", func(t *testing.T) {
		d := makeFixtureWithOptions(t, fixtureOptions{
			compilerReview:   `{"findings":[{"id":"C-1"}],"scope":"compiler","evidence":"go test"}`,
			hardenProduction: true,
		})
		if err := check(d); err == nil || !strings.Contains(err.Error(), "severity") {
			t.Fatalf("got %v, want finding schema error", err)
		}
	})

	t.Run("harden changes only a test", func(t *testing.T) {
		d := makeFixtureWithOptions(t, fixtureOptions{
			compilerReview:   completeCompilerReview,
			hardenProduction: false,
		})
		if err := check(d); err == nil || !strings.Contains(err.Error(), "production") {
			t.Fatalf("got %v, want production change error", err)
		}
	})

	t.Run("acceptance changes a different test", func(t *testing.T) {
		d := makeFixtureWithOptions(t, fixtureOptions{
			compilerReview:   completeCompilerReview,
			hardenProduction: true,
			acceptancePath:   "workflow/other_test.go",
		})
		if err := check(d); err == nil || !strings.Contains(err.Error(), "workflow/acceptance_test.go") {
			t.Fatalf("got %v, want acceptance ownership error", err)
		}
	})
}

func makeFixture(t *testing.T) string {
	t.Helper()
	return makeFixtureWithOptions(t, fixtureOptions{
		compilerReview:   completeCompilerReview,
		hardenProduction: true,
	})
}

type fixtureOptions struct {
	compilerReview   string
	hardenProduction bool
	acceptancePath   string
}

func makeFixtureWithOptions(t *testing.T, options fixtureOptions) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	mustRun(t, root, "git", "init", "-q", repo)
	mustRun(t, repo, "git", "config", "user.name", "Fixture")
	mustRun(t, repo, "git", "config", "user.email", "fixture@example.invalid")
	copyWorkflowInputs(t, repo)
	mustRun(t, repo, "git", "add", "docs/workflows/develop.yaml", "workflow/features")
	mustRun(t, repo, "git", "commit", "-qm", "baseline")
	baseline := mustRun(t, repo, "git", "rev-parse", "HEAD")
	plan, err := workflow.Compile(filepath.Join(repo, "docs", "workflows", "develop.yaml"))
	if err != nil {
		t.Fatalf("compile fixture plan: %v", err)
	}

	taskDirs := map[string]string{}
	commits := map[string]string{}
	if options.acceptancePath == "" {
		options.acceptancePath = "workflow/acceptance_test.go"
	}
	for _, id := range expectedTaskIDs {
		d := filepath.Join(root, "worktrees", id)
		mustRun(t, repo, "git", "worktree", "add", "-q", "-b", "fixture-"+id, d, baseline)
		taskDirs[id] = d
		for _, need := range expectedNeeds[id] {
			mustRun(t, d, "git", "merge", "--no-ff", "-qm", "integrate "+need, commits[need])
		}
		switch id {
		case "acceptance":
			mustWrite(t, filepath.Join(d, filepath.FromSlash(options.acceptancePath)), "package workflow\n")
			mustRun(t, d, "git", "add", ".")
			mustRun(t, d, "git", "commit", "-qm", id)
		case "harden":
			mustWrite(t, filepath.Join(d, "workflow", "executor_test.go"), "package workflow\n")
			if options.hardenProduction {
				mustWrite(t, filepath.Join(d, "workflow", "executor.go"), "package workflow\n")
			}
			mustRun(t, d, "git", "add", ".")
			mustRun(t, d, "git", "commit", "-qm", id)
		case "review-compiler":
			mustWrite(t, filepath.Join(d, ".workflow-review", "compiler.json"), options.compilerReview)
			mustRun(t, d, "git", "add", ".workflow-review/compiler.json")
			mustRun(t, d, "git", "commit", "-qm", id)
		case "review-execution":
			mustWrite(t, filepath.Join(d, ".workflow-review", "execution.json"), `{"findings":[],"scope":"executor","evidence":"go test ./workflow"}`)
			mustRun(t, d, "git", "add", ".workflow-review/execution.json")
			mustRun(t, d, "git", "commit", "-qm", id)
		case "reconcile":
			mustWrite(t, filepath.Join(d, ".workflow-review", "resolution.json"), `{"unresolved":[],"resolutions":[{"id":"C-1","status":"fixed","evidence":"regression red then green"}]}`)
			mustRun(t, d, "git", "add", ".workflow-review/resolution.json")
			mustRun(t, d, "git", "commit", "-qm", id)
		}
		commits[id] = mustRun(t, d, "git", "rev-parse", "HEAD")
	}

	integration := filepath.Join(root, "integration")
	mustRun(t, repo, "git", "worktree", "add", "-q", "-b", "fixture-integration", integration, baseline)
	for _, id := range expectedTaskIDs {
		mustRun(t, integration, "git", "merge", "--no-ff", "-qm", "integrate "+id, commits[id])
	}
	finalCommit := mustRun(t, integration, "git", "rev-parse", "HEAD")

	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	results := make([]map[string]any, 0, len(expectedTaskIDs))
	for i, id := range expectedTaskIDs {
		log := filepath.Join(runDir, id+".log")
		body := "command task\n"
		if id != "verify" && id != "vet" {
			body = "model: gpt-5.6-sol\nprovider: openai\n"
		}
		mustWrite(t, log, body)
		start := time.Date(2026, 9, 9, 10, i, 0, 0, time.UTC)
		finish := start.Add(time.Minute)
		if id == "harden" {
			start = time.Date(2026, 9, 9, 10, 0, 30, 0, time.UTC)
			finish = start.Add(time.Minute)
		}
		results = append(results, map[string]any{
			"id": id, "status": "success", "attempts": 1,
			"worktree": taskDirs[id], "commit": commits[id], "log": log,
			"started_at": start.Format(time.RFC3339Nano), "finished_at": finish.Format(time.RFC3339Nano),
		})
	}
	writeJSON(t, filepath.Join(runDir, "plan.json"), plan)
	writeJSON(t, filepath.Join(runDir, "result.json"), map[string]any{
		"baseline": baseline, "tasks": results,
		"integration_worktree": integration, "commit": finalCommit,
	})
	return runDir
}

func copyWorkflowInputs(t *testing.T, destination string) {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Clean(filepath.Join(workingDirectory, "..", "..", ".."))
	paths := []string{"docs/workflows/develop.yaml"}
	features, err := filepath.Glob(filepath.Join(repository, "workflow", "features", "*.feature"))
	if err != nil {
		t.Fatal(err)
	}
	for _, feature := range features {
		relative, err := filepath.Rel(repository, feature)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, relative)
	}
	for _, path := range paths {
		contents, err := os.ReadFile(filepath.Join(repository, path))
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(destination, path), string(contents))
	}
}

func resultBaseline(t *testing.T, d string) string {
	t.Helper()
	var v map[string]any
	readJSON(t, filepath.Join(d, "result.json"), &v)
	return v["baseline"].(string)
}

func updateTask(t *testing.T, d, id string, edit func(map[string]any)) {
	t.Helper()
	updateJSON(t, filepath.Join(d, "result.json"), func(v map[string]any) {
		for _, raw := range v["tasks"].([]any) {
			task := raw.(map[string]any)
			if task["id"] == id {
				edit(task)
				return
			}
		}
		t.Fatalf("fixture task %s missing", id)
	})
}

func taskLog(t *testing.T, d, id string) string {
	t.Helper()
	var v map[string]any
	readJSON(t, filepath.Join(d, "result.json"), &v)
	for _, raw := range v["tasks"].([]any) {
		task := raw.(map[string]any)
		if task["id"] == id {
			return task["log"].(string)
		}
	}
	t.Fatalf("fixture task %s missing", id)
	return ""
}

func taskWorktree(t *testing.T, d, id string) string {
	t.Helper()
	var v map[string]any
	readJSON(t, filepath.Join(d, "result.json"), &v)
	for _, raw := range v["tasks"].([]any) {
		task := raw.(map[string]any)
		if task["id"] == id {
			return task["worktree"].(string)
		}
	}
	t.Fatalf("fixture task %s missing", id)
	return ""
}

func integrationDir(t *testing.T, d string) string {
	t.Helper()
	var v map[string]any
	readJSON(t, filepath.Join(d, "result.json"), &v)
	return v["integration_worktree"].(string)
}

func commitIntegrationFile(t *testing.T, d, name, body string) {
	t.Helper()
	reconcile := taskWorktree(t, d, "reconcile")
	mustWrite(t, filepath.Join(reconcile, filepath.FromSlash(name)), body)
	mustRun(t, reconcile, "git", "add", name)
	mustRun(t, reconcile, "git", "commit", "-qm", "alter evidence")
	reconcileCommit := mustRun(t, reconcile, "git", "rev-parse", "HEAD")
	updateTask(t, d, "reconcile", func(v map[string]any) { v["commit"] = reconcileCommit })

	integration := integrationDir(t, d)
	for _, id := range []string{"verify", "vet"} {
		worktree := taskWorktree(t, d, id)
		mustRun(t, worktree, "git", "merge", "--no-ff", "-qm", "integrate altered reconciliation", reconcileCommit)
		commit := mustRun(t, worktree, "git", "rev-parse", "HEAD")
		updateTask(t, d, id, func(v map[string]any) { v["commit"] = commit })
		mustRun(t, integration, "git", "merge", "--no-ff", "-qm", "integrate altered "+id, commit)
	}
	head := mustRun(t, integration, "git", "rev-parse", "HEAD")
	updateJSON(t, filepath.Join(d, "result.json"), func(v map[string]any) { v["commit"] = head })
}

func updateJSON(t *testing.T, path string, edit func(map[string]any)) {
	t.Helper()
	var v map[string]any
	readJSON(t, path, &v)
	edit(v)
	writeJSON(t, path, v)
}

func readJSON(t *testing.T, path string, dst any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, string(b))
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}
