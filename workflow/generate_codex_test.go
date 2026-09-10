package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func plannerFixture(t *testing.T, response string) GenerateOptions {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake planner requires Unix")
	}
	dir, opts := generateFixture(t)
	responsePath := filepath.Join(dir, "response.json")
	if err := os.WriteFile(responsePath, []byte(response), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "codex")
	script := "#!/bin/sh\ncat > '" + dir + "/input.txt'\nprintf '%s\\n' \"$@\" > '" + dir + "/args.txt'\nwhile [ $# -gt 0 ]; do\n if [ \"$1\" = --output-last-message ]; then shift; cp '" + responsePath + "' \"$1\"; fi\n shift\ndone\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	opts.Generator = "codex"
	opts.CodexBinary = binary
	opts.Models = []string{"small", "large"}
	opts.ReasoningEffort = "medium"
	return opts
}

const goodPlannerResponse = `{"tasks":[{"id":"a","features":["a.feature"],"needs":[],"model":"small","reasoning_effort":"low","rationale":"Independent package a with its own tests; does not modify shared files"},{"id":"b","features":["b.feature"],"needs":[],"model":"large","reasoning_effort":"xhigh","rationale":"Independent package b with its own tests; does not modify shared files"}],"review":{"model":"large","reasoning_effort":"high"}}`

func TestGenerateCodexParallelPlan(t *testing.T) {
	opts := plannerFixture(t, goodPlannerResponse)
	opts.Prompt = "Follow project-specific style."
	if err := Generate(context.Background(), []string{"*.feature"}, opts); err != nil {
		t.Fatal(err)
	}
	p, err := Compile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tasks) != 3 || p.Tasks[0].Model != "small" || len(p.Tasks[1].Needs) != 0 || len(p.Tasks[2].Needs) != 2 {
		t.Fatalf("unexpected plan: %+v", p.Tasks)
	}
	for i, effort := range []string{"low", "xhigh", "high"} {
		if p.Tasks[i].ReasoningEffort != effort {
			t.Errorf("task %d effort = %q, want %q", i, p.Tasks[i].ReasoningEffort, effort)
		}
		if !strings.Contains(p.Tasks[i].Prompt, opts.Prompt) {
			t.Errorf("task %d lost custom prompt", i)
		}
	}
	args, err := os.ReadFile(filepath.Join(opts.Dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"--sandbox\nread-only", "--output-schema", "--output-last-message", "model_reasoning_effort=\"medium\""} {
		if !strings.Contains(string(args), s) {
			t.Errorf("missing argument %q: %s", s, args)
		}
	}
	prompt, _ := os.ReadFile(filepath.Join(opts.Dir, "input.txt"))
	for _, s := range []string{"Feature: A", "a.feature", "b.feature", "shared"} {
		if !strings.Contains(string(prompt), s) {
			t.Errorf("missing context %q", s)
		}
	}
}
func TestGenerateCodexRejectsInvalidPlans(t *testing.T) {
	cases := map[string]func(map[string]any){
		"omitted feature":   func(p map[string]any) { p["tasks"] = p["tasks"].([]any)[:1] },
		"duplicate feature": func(p map[string]any) { p["tasks"].([]any)[1].(map[string]any)["features"] = []string{"a.feature"} },
		"unknown feature":   func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["features"] = []string{"other.feature"} },
		"unknown model":     func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["model"] = "unlisted" },
		"bad effort":        func(p map[string]any) { p["review"].(map[string]any)["reasoning_effort"] = "max" },
		"no isolation":      func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["rationale"] = "" },
		"cycle": func(p map[string]any) {
			p["tasks"].([]any)[0].(map[string]any)["needs"] = []string{"b"}
			p["tasks"].([]any)[1].(map[string]any)["needs"] = []string{"a"}
		},
		"unknown dependency": func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["needs"] = []string{"missing"} },
		"command injection":  func(p map[string]any) { p["tasks"].([]any)[0].(map[string]any)["run"] = []string{"touch", "bad"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var p map[string]any
			if err := json.Unmarshal([]byte(goodPlannerResponse), &p); err != nil {
				t.Fatal(err)
			}
			mutate(p)
			b, _ := json.Marshal(p)
			opts := plannerFixture(t, string(b))
			if err := Generate(context.Background(), []string{"*.feature"}, opts); err == nil {
				t.Fatal("accepted invalid plan")
			}
			if _, err := os.Stat(opts.Output); !os.IsNotExist(err) {
				t.Fatalf("output published: %v", err)
			}
		})
	}
}

func TestGenerateCodexFailureDoesNotPublish(t *testing.T) {
	opts := plannerFixture(t, goodPlannerResponse)
	if err := os.WriteFile(opts.CodexBinary, []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Generate(context.Background(), []string{"*.feature"}, opts); err == nil {
		t.Fatal("accepted failed planner")
	}
	if _, err := os.Stat(opts.Output); !os.IsNotExist(err) {
		t.Fatalf("published on failure: %v", err)
	}
}

func TestGenerateCodexCancelsDescendants(t *testing.T) {
	opts := plannerFixture(t, goodPlannerResponse)
	marker := filepath.Join(opts.Dir, "child-survived")
	ready := filepath.Join(opts.Dir, "child-ready")
	script := "#!/bin/sh\n(sleep 0.5; touch '" + marker + "') &\ntouch '" + ready + "'\nwait\n"
	if err := os.WriteFile(opts.CodexBinary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Generate(ctx, []string{"*.feature"}, opts) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("planner child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("planner did not cancel")
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("planner child survived cancellation")
	}
	if _, err := os.Stat(opts.Output); !os.IsNotExist(err) {
		t.Fatalf("published on cancellation: %v", err)
	}
}

func TestGenerateCodexSerialAndGroupedPlans(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprint(grouped), func(t *testing.T) {
			var plan map[string]any
			if err := json.Unmarshal([]byte(goodPlannerResponse), &plan); err != nil {
				t.Fatal(err)
			}
			tasks := plan["tasks"].([]any)
			if grouped {
				tasks[0].(map[string]any)["features"] = []string{"a.feature", "b.feature"}
				plan["tasks"] = tasks[:1]
			} else {
				tasks[1].(map[string]any)["needs"] = []string{"a"}
			}
			content, _ := json.Marshal(plan)
			opts := plannerFixture(t, string(content))
			if err := Generate(context.Background(), []string{"*.feature"}, opts); err != nil {
				t.Fatal(err)
			}
			p, err := Compile(opts.Output)
			if err != nil {
				t.Fatal(err)
			}
			if grouped {
				if len(p.Tasks) != 2 || len(p.Tasks[0].Scenarios) != 2 {
					t.Fatalf("lost grouped coverage: %+v", p.Tasks)
				}
			} else {
				if len(p.Tasks[1].Needs) != 1 || p.Tasks[1].Needs[0] != "a" {
					t.Fatalf("lost serial dependency: %+v", p.Tasks)
				}
			}
		})
	}
}

func TestGenerateCodexPlanExecutesParallelTasksAndReview(t *testing.T) {
	opts := plannerFixture(t, goodPlannerResponse)
	repo := newTestRepository(t)
	for _, name := range []string{"a.feature", "b.feature"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("Feature: "+name+"\n Scenario: implemented\n  Given working behavior\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	opts.Dir = repo
	opts.Output = filepath.Join(repo, "workflow.yaml")
	if err := Generate(context.Background(), []string{"*.feature"}, opts); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "test: generated AI policy")
	plan, err := Compile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	agent := writeExecutable(t, `#!/bin/sh
set -eu
model=""
effort=""
while [ "$#" -gt 0 ]; do
 case "$1" in
 -m) shift; model="$1" ;;
 -c) shift; effort="$1" ;;
 esac
 shift
done
case "$model/$effort" in
 'small/model_reasoning_effort="low"') echo done > a.txt ;;
 'large/model_reasoning_effort="xhigh"') echo done > b.txt ;;
 'large/model_reasoning_effort="high"') test -f a.txt; test -f b.txt; echo reviewed > review.txt ;;
 *) exit 23 ;;
esac
`)
	result, err := Execute(context.Background(), plan, RunOptions{Dir: repo, OutputDir: filepath.Join(t.TempDir(), "run"), CodexBinary: agent, Jobs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(result.IntegrationWorktree, "review.txt")); got != "reviewed\n" {
		t.Fatalf("review output = %q", got)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if got := readFile(t, filepath.Join(result.IntegrationWorktree, name)); got != "done\n" {
			t.Fatalf("%s output = %q", name, got)
		}
	}
}

func TestGenerateCodexMissingOutputDirectoryDoesNotInvokePlanner(t *testing.T) {
	opts := plannerFixture(t, goodPlannerResponse)
	opts.Output = filepath.Join(opts.Dir, "missing", "workflow.yaml")
	if err := Generate(context.Background(), []string{"*.feature"}, opts); err == nil {
		t.Fatal("accepted missing output directory")
	}
	if _, err := os.Stat(filepath.Join(opts.Dir, "args.txt")); !os.IsNotExist(err) {
		t.Fatal("invoked paid planner for invalid output destination")
	}
}
