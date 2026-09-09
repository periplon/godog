package workflow

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// GenerateOptions configures deterministic generation of a prompt-based policy.
// Output is relative to the caller's working directory; feature selectors are
// relative to Dir. The output directory must already exist.
type GenerateOptions struct {
	Output string
	Dir    string
	Model  string
	Prompt string
}

// Generate converts Gherkin files into a validated workflow without invoking
// Codex or executing any task. It never replaces an existing output path.
func Generate(ctx context.Context, selectors []string, opts GenerateOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(opts.Output) == "" {
		return errors.New("workflow output path is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return errors.New("workflow model is required")
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return fmt.Errorf("resolve output: %w", err)
	}
	if _, err := os.Lstat(output); err == nil {
		return fmt.Errorf("workflow output already exists: %s", output)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect output: %w", err)
	}
	if opts.Dir == "" {
		opts.Dir = "."
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return fmt.Errorf("resolve feature directory: %w", err)
	}
	relativeSelectors := make([]string, len(selectors))
	for i, selector := range selectors {
		relativeSelectors[i] = selector
		if filepath.IsAbs(selector) {
			relativeSelectors[i], err = filepath.Rel(dir, selector)
			if err != nil {
				return fmt.Errorf("resolve feature selector: %w", err)
			}
		}
		// CLI/API inputs may name literal files with glob characters. The
		// compiled policy itself retains strictly glob-based selectors.
		if info, statErr := os.Stat(filepath.Join(dir, relativeSelectors[i])); statErr == nil && info.Mode().IsRegular() {
			relativeSelectors[i] = literalFeatureGlob(filepath.ToSlash(relativeSelectors[i]))
		}
	}
	features, err := compileFeatures(dir, relativeSelectors)
	if err != nil {
		return err
	}
	spec := Spec{Version: workflowVersion, Name: "Generated feature workflow", Model: strings.TrimSpace(opts.Model)}
	needs := make([]string, 0, len(features))
	for _, feature := range features {
		uri, err := filepath.Rel(filepath.Dir(output), filepath.Join(dir, filepath.FromSlash(feature.uri)))
		if err != nil {
			return fmt.Errorf("resolve output feature path: %w", err)
		}
		selector := literalFeatureGlob(filepath.ToSlash(uri))
		spec.Features = append(spec.Features, selector)
		digest := sha256.Sum256([]byte(feature.uri))
		id := fmt.Sprintf("implement-%x", digest[:12])
		var prerequisites []string
		if len(needs) > 0 {
			prerequisites = []string{needs[len(needs)-1]}
		}
		needs = append(needs, id)
		spec.Tasks = append(spec.Tasks, TaskSpec{ID: id, Needs: prerequisites, Features: []string{selector}, Attempts: 1, Prompt: withGeneratePrompt(generateImplementationPrompt, opts.Prompt)})
	}
	spec.Tasks = append(spec.Tasks, TaskSpec{ID: "review-all", Needs: needs, Attempts: 1, Prompt: withGeneratePrompt(generateReviewPrompt, opts.Prompt)})
	content, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode generated workflow: %w", err)
	}
	candidate, err := os.CreateTemp(filepath.Dir(output), ".godog-workflow-*.yaml")
	if err != nil {
		return fmt.Errorf("create workflow candidate: %w", err)
	}
	defer os.Remove(candidate.Name())
	if _, err := candidate.Write(content); err != nil {
		candidate.Close()
		return fmt.Errorf("write workflow candidate: %w", err)
	}
	if err := candidate.Close(); err != nil {
		return fmt.Errorf("close workflow candidate: %w", err)
	}
	if _, err := Compile(candidate.Name()); err != nil {
		return fmt.Errorf("validate generated workflow: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Linking a completed candidate publishes atomically and fails even when an
	// existing destination is a dangling symlink. Both paths share a filesystem.
	if err := os.Link(candidate.Name(), output); err != nil {
		return fmt.Errorf("publish workflow without overwrite: %w", err)
	}
	return nil
}

func literalFeatureGlob(uri string) string {
	var result strings.Builder
	for _, r := range uri {
		switch r {
		case '*', '?', '[':
			result.WriteByte('[')
			result.WriteRune(r)
			result.WriteByte(']')
		case '\\':
			result.WriteString(`\\`)
		default:
			result.WriteRune(r)
		}
	}
	return result.String()
}

func withGeneratePrompt(base, extra string) string {
	if strings.TrimSpace(extra) == "" {
		return base
	}
	return base + "\n\nAdditional project instructions:\n" + strings.TrimSpace(extra)
}

const generateImplementationPrompt = `Implement the selected Gherkin requirements in this repository.
Read the repository instructions and inspect the existing implementation and test infrastructure first.
Use TDD: add or update acceptance or regression tests, run them and confirm they fail for the expected reason, then make the minimum implementation changes to pass. Refactor only with tests green.
Use the actual repository test commands; do not invent commands or claim checks that did not run. Cover relevant edge cases implied by the scenarios.
Perform a self-review for correctness, integration, and regressions. Fix findings and rerun the relevant checks. Report changes, checks, and any remaining blockers.`

const generateReviewPrompt = `Perform an integration review of all selected Gherkin requirements after the implementation tasks.
Check scenario coverage, correctness, task interactions, regressions, and repository conventions from more than one angle.
Run the actual repository acceptance and regression checks. For every defect, use TDD: first add a failing regression test, confirm the expected failure, then fix the defect and rerun checks.
Iterate review and verification until no actionable findings remain, or explicitly report any blocker. Do not claim checks passed unless they ran successfully.`
