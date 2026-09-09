package workflow

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func generateFixture(t *testing.T) (string, GenerateOptions) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"a.feature", "b.feature"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("Feature: A\n Scenario: A\n  Given a thing\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, GenerateOptions{Dir: dir, Output: filepath.Join(dir, "workflow.yaml"), Model: "test-model"}
}
func TestGenerateDeterministicCoverage(t *testing.T) {
	dir, opts := generateFixture(t)
	opts.Prompt = "Follow repository conventions."
	if err := Generate(context.Background(), []string{"b.feature", "a.feature", "a.feature"}, opts); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tasks) != 3 {
		t.Fatalf("tasks: %d", len(plan.Tasks))
	}
	for _, task := range plan.Tasks {
		for _, s := range []string{"TDD", "review", "Follow repository conventions."} {
			if !strings.Contains(task.Prompt, s) {
				t.Errorf("task %s missing %q", task.ID, s)
			}
		}
		if task.Model != "test-model" {
			t.Errorf("model: %s", task.Model)
		}
	}
	if len(plan.Tasks[1].Needs) != 1 || plan.Tasks[1].Needs[0] != plan.Tasks[0].ID {
		t.Fatal("implementation tasks must be serial")
	}
	if len(plan.Tasks[2].Needs) != 2 {
		t.Fatal("review must depend on both implementations")
	}
	opts.Output = filepath.Join(dir, "second.yaml")
	if err := Generate(context.Background(), []string{"*.feature"}, opts); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("output not deterministic")
	}
}
func TestGeneratePreflightAndNoOverwrite(t *testing.T) {
	for _, mode := range []string{"existing", "symlink", "invalid-feature", "missing-model", "missing-output", "no-features", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			dir, opts := generateFixture(t)
			features := []string{"a.feature"}
			ctx := context.Background()
			switch mode {
			case "existing":
				mustGenerateFixture(t, os.WriteFile(opts.Output, []byte("keep"), 0600))
			case "symlink":
				func() {
					if err := os.Symlink("missing", opts.Output); err != nil {
						if runtime.GOOS == "windows" {
							t.Skipf("symlink unavailable: %v", err)
						}
						t.Fatal(err)
					}
				}()
			case "invalid-feature":
				mustGenerateFixture(t, os.WriteFile(filepath.Join(dir, "a.feature"), []byte("bad"), 0600))
			case "missing-model":
				opts.Model = ""
			case "missing-output":
				opts.Output = ""
			case "no-features":
				features = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := Generate(ctx, features, opts); err == nil {
				t.Fatal("expected error")
			}
			if mode == "existing" {
				data, err := os.ReadFile(opts.Output)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != "keep" {
					t.Fatal("overwritten")
				}
			} else if mode == "symlink" {
				if target, err := os.Readlink(opts.Output); err != nil || target != "missing" {
					t.Fatal("symlink changed")
				}
			} else if _, err := os.Lstat(filepath.Join(dir, "workflow.yaml")); !os.IsNotExist(err) {
				t.Fatal("output created")
			}
		})
	}
}
func TestGenerateNestedOutput(t *testing.T) {
	dir, opts := generateFixture(t)
	mustGenerateFixture(t, os.Mkdir(filepath.Join(dir, "plans"), 0700))
	opts.Output = filepath.Join(dir, "plans/workflow.yaml")
	if err := Generate(context.Background(), []string{filepath.Join(dir, "a.feature")}, opts); err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tasks[0].Scenarios[0].URI != "../a.feature" {
		t.Fatalf("URI: %s", plan.Tasks[0].Scenarios[0].URI)
	}
}
func TestGenerateLiteralGlobCharacters(t *testing.T) {
	dir, opts := generateFixture(t)
	mustGenerateFixture(t, os.Rename(filepath.Join(dir, "a.feature"), filepath.Join(dir, "a[1].feature")))
	if err := Generate(context.Background(), []string{"*.feature"}, opts); err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tasks) != 3 {
		t.Fatal("missing feature")
	}
}

func TestGenerateLiteralFilenameAndGlobDirectory(t *testing.T) {
	for _, name := range []string{"a[1].feature", "a.feature"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "repo[1]")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte("Feature: A\n Scenario: A\n  Given A\n"), 0600); err != nil {
				t.Fatal(err)
			}
			opts := GenerateOptions{Dir: dir, Output: filepath.Join(dir, "workflow.yaml"), Model: "test"}
			if err := Generate(context.Background(), []string{name}, opts); err != nil {
				t.Fatal(err)
			}
			if _, err := Compile(opts.Output); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func mustGenerateFixture(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestGenerateStableTaskIDs(t *testing.T) {
	dir, opts := generateFixture(t)
	mustGenerateFixture(t, Generate(context.Background(), []string{"b.feature"}, opts))
	first, err := Compile(opts.Output)
	mustGenerateFixture(t, err)
	opts.Output = filepath.Join(dir, "second.yaml")
	mustGenerateFixture(t, Generate(context.Background(), []string{"*.feature"}, opts))
	second, err := Compile(opts.Output)
	mustGenerateFixture(t, err)
	if first.Tasks[0].ID != second.Tasks[1].ID {
		t.Fatal("adding earlier feature changed task identity")
	}
}
func TestGenerateLiteralBackslashUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a directory separator on Windows")
	}
	dir, opts := generateFixture(t)
	name := `a\b.feature`
	mustGenerateFixture(t, os.Rename(filepath.Join(dir, "a.feature"), filepath.Join(dir, name)))
	mustGenerateFixture(t, Generate(context.Background(), []string{name}, opts))
	plan, err := Compile(opts.Output)
	mustGenerateFixture(t, err)
	if plan.Tasks[0].Scenarios[0].URI != name {
		t.Fatal("literal backslash lost")
	}
}

func TestGenerateEscapedSelectorCollision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asterisk filenames are not supported on Windows")
	}
	dir, opts := generateFixture(t)
	mustGenerateFixture(t, os.Rename(filepath.Join(dir, "a.feature"), filepath.Join(dir, "a*.feature")))
	mustGenerateFixture(t, os.Rename(filepath.Join(dir, "b.feature"), filepath.Join(dir, "a[*].feature")))
	mustGenerateFixture(t, Generate(context.Background(), []string{"*.feature"}, opts))
	plan, err := Compile(opts.Output)
	mustGenerateFixture(t, err)
	if len(plan.Tasks) != 3 {
		t.Fatal("lost literal glob filename")
	}
}
