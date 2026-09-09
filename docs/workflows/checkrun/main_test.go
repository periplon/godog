package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckAcceptsCompleteSelfHostEvidence(t *testing.T) {
	runDir := makeFixture(t)
	if err := check(runDir); err != nil {
		t.Fatalf("complete evidence rejected: %v", err)
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

func makeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	mustRun(t, root, "git", "init", "-q", repo)
	mustRun(t, repo, "git", "config", "user.name", "Fixture")
	mustRun(t, repo, "git", "config", "user.email", "fixture@example.invalid")
	mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
	mustRun(t, repo, "git", "add", "README.md")
	mustRun(t, repo, "git", "commit", "-qm", "baseline")
	baseline := mustRun(t, repo, "git", "rev-parse", "HEAD")

	taskDirs := map[string]string{}
	commits := map[string]string{}
	for _, id := range expectedTaskIDs {
		d := filepath.Join(root, "worktrees", id)
		mustRun(t, repo, "git", "worktree", "add", "-q", "-b", "fixture-"+id, d, baseline)
		taskDirs[id] = d
		if id == "acceptance" || id == "harden" {
			mustWrite(t, filepath.Join(d, id+"_test.go"), "package fixture\n")
			mustWrite(t, filepath.Join(d, id+".go"), "package fixture\n")
			mustRun(t, d, "git", "add", ".")
			mustRun(t, d, "git", "commit", "-qm", id)
		}
		commits[id] = mustRun(t, d, "git", "rev-parse", "HEAD")
	}

	integration := filepath.Join(root, "integration")
	mustRun(t, repo, "git", "worktree", "add", "-q", "-b", "fixture-integration", integration, baseline)
	for _, id := range []string{"acceptance", "harden"} {
		mustRun(t, integration, "git", "merge", "--no-ff", "-qm", "integrate "+id, commits[id])
	}
	mustWrite(t, filepath.Join(integration, ".workflow-review", "compiler.json"), `{"findings":[{"id":"C-1"}],"scope":"compiler","evidence":"go test"}`)
	mustWrite(t, filepath.Join(integration, ".workflow-review", "execution.json"), `{"findings":[],"scope":"executor","evidence":"go test"}`)
	mustWrite(t, filepath.Join(integration, ".workflow-review", "resolution.json"), `{"unresolved":[],"resolutions":[{"id":"C-1","status":"fixed","evidence":"regression red then green"}]}`)
	mustRun(t, integration, "git", "add", ".workflow-review")
	mustRun(t, integration, "git", "commit", "-qm", "review evidence")
	finalCommit := mustRun(t, integration, "git", "rev-parse", "HEAD")

	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	tasks := make([]map[string]any, 0, len(expectedTaskIDs))
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
		planTask := map[string]any{"id": id, "needs": expectedNeeds[id]}
		if id == "verify" || id == "vet" {
			if id == "verify" {
				planTask["run"] = []string{"go", "test", "-race", "./..."}
			}
			if id == "vet" {
				planTask["run"] = []string{"go", "vet", "./..."}
			}
			planTask["expected_exit"] = 0
		} else {
			planTask["prompt"] = "perform " + id
			planTask["model"] = "gpt-5.6-sol"
		}
		tasks = append(tasks, planTask)
		results = append(results, map[string]any{
			"id": id, "status": "success", "attempts": 1,
			"worktree": taskDirs[id], "commit": commits[id], "log": log,
			"started_at": start.Format(time.RFC3339Nano), "finished_at": finish.Format(time.RFC3339Nano),
		})
	}
	writeJSON(t, filepath.Join(runDir, "plan.json"), map[string]any{"version": 1, "name": expectedPlanName, "tasks": tasks})
	writeJSON(t, filepath.Join(runDir, "result.json"), map[string]any{
		"baseline": baseline, "tasks": results,
		"integration_worktree": integration, "commit": finalCommit,
	})
	return runDir
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

func integrationDir(t *testing.T, d string) string {
	t.Helper()
	var v map[string]any
	readJSON(t, filepath.Join(d, "result.json"), &v)
	return v["integration_worktree"].(string)
}

func commitIntegrationFile(t *testing.T, d, name, body string) {
	t.Helper()
	integration := integrationDir(t, d)
	mustWrite(t, filepath.Join(integration, filepath.FromSlash(name)), body)
	mustRun(t, integration, "git", "add", name)
	mustRun(t, integration, "git", "commit", "-qm", "alter evidence")
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
