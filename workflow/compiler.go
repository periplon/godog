package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gherkin "github.com/cucumber/gherkin/go/v42"
	"github.com/cucumber/messages/go/v34"
	"go.yaml.in/yaml/v3"
)

const workflowVersion = 1

type compiledFeature struct {
	uri       string
	source    string
	scenarios []Scenario
}

// Compile reads a workflow specification and its Gherkin features, validates
// them, and returns tasks in deterministic topological order.
func Compile(specPath string) (*Plan, error) {
	spec, attemptsSet, specDir, err := readSpec(specPath)
	if err != nil {
		return nil, err
	}
	if spec.Version != workflowVersion {
		return nil, fmt.Errorf("workflow version must be %d, got %d", workflowVersion, spec.Version)
	}

	features, err := compileFeatures(specDir, spec.Features)
	if err != nil {
		return nil, err
	}
	tasks, err := compileTasks(spec, attemptsSet, features)
	if err != nil {
		return nil, err
	}
	return &Plan{Version: spec.Version, Name: spec.Name, Tasks: tasks}, nil
}

func readSpec(specPath string) (Spec, []bool, string, error) {
	content, err := os.ReadFile(specPath)
	if err != nil {
		return Spec{}, nil, "", fmt.Errorf("read workflow spec: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var spec Spec
	if err := decoder.Decode(&spec); err != nil {
		if errors.Is(err, io.EOF) {
			return Spec{}, nil, "", errors.New("workflow spec is empty")
		}
		return Spec{}, nil, "", fmt.Errorf("decode workflow spec: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return Spec{}, nil, "", fmt.Errorf("decode workflow spec: %w", err)
		}
		return Spec{}, nil, "", errors.New("workflow spec contains multiple YAML documents")
	}
	attemptsSet, err := taskFieldPresence(content, "attempts")
	if err != nil {
		return Spec{}, nil, "", fmt.Errorf("inspect workflow spec: %w", err)
	}
	absSpecPath, err := filepath.Abs(specPath)
	if err != nil {
		return Spec{}, nil, "", fmt.Errorf("resolve workflow spec path: %w", err)
	}
	return spec, attemptsSet, filepath.Dir(absSpecPath), nil
}

func taskFieldPresence(content []byte, field string) ([]bool, error) {
	// Decode mappings so aliases and YAML merges have the same semantics as Spec.
	var raw struct {
		Tasks []map[string]yaml.Node `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(content, &raw); err != nil {
		return nil, err
	}
	present := make([]bool, len(raw.Tasks))
	for i, task := range raw.Tasks {
		_, present[i] = task[field]
	}
	return present, nil
}

func compileFeatures(specDir string, selectors []string) ([]compiledFeature, error) {
	if len(selectors) == 0 {
		return nil, errors.New("workflow features matched no files")
	}

	pathsByAbsolute := make(map[string]string)
	for _, selector := range selectors {
		if strings.TrimSpace(selector) == "" {
			return nil, errors.New("workflow feature glob is empty")
		}
		if filepath.IsAbs(selector) {
			return nil, fmt.Errorf("workflow feature glob %q must be relative to the spec", selector)
		}
		// The base directory is literal; only the selector is a glob.
		matches, err := filepath.Glob(filepath.Join(literalFeatureGlob(filepath.ToSlash(specDir)), filepath.FromSlash(selector)))
		if err != nil {
			return nil, fmt.Errorf("invalid feature glob %q: %w", selector, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("workflow feature glob %q matched no files", selector)
		}
		for _, match := range matches {
			absolute, err := filepath.Abs(match)
			if err != nil {
				return nil, fmt.Errorf("resolve feature %q: %w", match, err)
			}
			relative, err := filepath.Rel(specDir, absolute)
			if err != nil {
				return nil, fmt.Errorf("resolve feature URI %q: %w", match, err)
			}
			pathsByAbsolute[absolute] = filepath.ToSlash(relative)
		}
	}
	if len(pathsByAbsolute) == 0 {
		return nil, errors.New("workflow features matched no files")
	}

	paths := make([]string, 0, len(pathsByAbsolute))
	for featurePath := range pathsByAbsolute {
		paths = append(paths, featurePath)
	}
	sort.Slice(paths, func(i, j int) bool {
		return pathsByAbsolute[paths[i]] < pathsByAbsolute[paths[j]]
	})

	features := make([]compiledFeature, 0, len(paths))
	for _, featurePath := range paths {
		feature, err := compileFeature(featurePath, pathsByAbsolute[featurePath])
		if err != nil {
			return nil, err
		}
		features = append(features, feature)
	}
	return features, nil
}

func compileFeature(featurePath, uri string) (compiledFeature, error) {
	content, err := os.ReadFile(featurePath)
	if err != nil {
		return compiledFeature{}, fmt.Errorf("read feature %q: %w", uri, err)
	}
	ids := &messages.Incrementing{}
	document, err := gherkin.ParseGherkinDocument(bytes.NewReader(content), ids.NewId)
	if err != nil {
		return compiledFeature{}, fmt.Errorf("parse feature %q: %w", uri, err)
	}
	if document.Feature == nil {
		return compiledFeature{}, fmt.Errorf("feature %q contains no feature", uri)
	}
	pickles := gherkin.Pickles(*document, uri, ids.NewId)
	if len(pickles) == 0 {
		return compiledFeature{}, fmt.Errorf("feature %q contains no scenarios", uri)
	}

	scenarios := make([]Scenario, 0, len(pickles))
	for _, pickle := range pickles {
		if len(pickle.Steps) == 0 {
			return compiledFeature{}, fmt.Errorf("scenario %q in feature %q contains no steps", pickle.Name, uri)
		}
		location := pickle.Location
		line, column := int64(0), int64(0)
		if location != nil {
			line, column = location.Line, location.Column
		}
		scenario := Scenario{
			ID:   scenarioID(uri, line, column, pickle.Name),
			URI:  uri,
			Name: pickle.Name,
		}
		for _, step := range pickle.Steps {
			scenario.Steps = append(scenario.Steps, renderStep(step))
		}
		scenarios = append(scenarios, scenario)
	}
	return compiledFeature{
		uri:       uri,
		source:    string(content),
		scenarios: scenarios,
	}, nil
}

func scenarioID(uri string, line, column int64, name string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s", uri, line, column, name)))
	return "scenario-" + hex.EncodeToString(digest[:12])
}

func renderStep(step *messages.PickleStep) string {
	var rendered strings.Builder
	rendered.WriteString(stepKeyword(step.Type))
	rendered.WriteByte(' ')
	rendered.WriteString(step.Text)
	if step.Argument == nil {
		return rendered.String()
	}
	if table := step.Argument.DataTable; table != nil {
		for _, row := range table.Rows {
			rendered.WriteString("\n  |")
			for _, cell := range row.Cells {
				rendered.WriteByte(' ')
				rendered.WriteString(escapeTableCell(cell.Value))
				rendered.WriteString(" |")
			}
		}
	}
	if doc := step.Argument.DocString; doc != nil {
		rendered.WriteString("\n  \"\"\"")
		rendered.WriteString(doc.MediaType)
		for _, line := range strings.Split(doc.Content, "\n") {
			rendered.WriteString("\n  ")
			rendered.WriteString(line)
		}
		rendered.WriteString("\n  \"\"\"")
	}
	return rendered.String()
}

func stepKeyword(stepType messages.PickleStepType) string {
	switch stepType {
	case messages.PickleStepType_CONTEXT:
		return "Given"
	case messages.PickleStepType_ACTION:
		return "When"
	case messages.PickleStepType_OUTCOME:
		return "Then"
	default:
		return "*"
	}
}

func escapeTableCell(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `|`, `\|`)
	return strings.ReplaceAll(value, "\n", `\n`)
}

func compileTasks(spec Spec, attemptsSet []bool, features []compiledFeature) ([]Task, error) {
	if len(spec.Tasks) == 0 {
		return nil, errors.New("workflow tasks are empty")
	}
	tasksByID := make(map[string]Task, len(spec.Tasks))
	covered := make(map[string]bool)

	for taskIndex, taskSpec := range spec.Tasks {
		attemptsExplicit := taskIndex < len(attemptsSet) && attemptsSet[taskIndex]
		if err := validateTaskSpec(&taskSpec, spec.Model, attemptsExplicit); err != nil {
			return nil, err
		}
		if _, exists := tasksByID[taskSpec.ID]; exists {
			return nil, fmt.Errorf("duplicate task id %q", taskSpec.ID)
		}
		if taskSpec.Attempts == 0 {
			taskSpec.Attempts = 1
		}
		if strings.TrimSpace(taskSpec.Prompt) != "" {
			taskSpec.Model = strings.TrimSpace(taskSpec.Model)
			if taskSpec.Model == "" {
				taskSpec.Model = strings.TrimSpace(spec.Model)
			}
		}

		selected, err := selectFeatures(taskSpec.ID, taskSpec.Features, features)
		if err != nil {
			return nil, err
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("task %q contains no scenarios", taskSpec.ID)
		}
		var scenarios []Scenario
		for _, feature := range selected {
			scenarios = append(scenarios, feature.scenarios...)
			for _, scenario := range feature.scenarios {
				covered[scenario.ID] = true
			}
		}
		if strings.TrimSpace(taskSpec.Prompt) != "" {
			taskSpec.Prompt = buildPrompt(taskSpec.Prompt, selected)
		}
		tasksByID[taskSpec.ID] = Task{TaskSpec: taskSpec, Scenarios: scenarios}
	}

	for _, feature := range features {
		for _, scenario := range feature.scenarios {
			if !covered[scenario.ID] {
				return nil, fmt.Errorf("scenario %q in %q is not assigned to any task", scenario.Name, scenario.URI)
			}
		}
	}
	return topologicalTasks(tasksByID)
}

func validateTaskSpec(task *TaskSpec, defaultModel string, attemptsExplicit bool) error {
	if strings.TrimSpace(task.ID) == "" {
		return errors.New("task id is empty")
	}
	if !isSafePathComponent(task.ID) {
		return fmt.Errorf("task id %q is not a safe path component", task.ID)
	}
	hasRun := len(task.Run) > 0
	hasPrompt := strings.TrimSpace(task.Prompt) != ""
	if hasRun == hasPrompt {
		return fmt.Errorf("task %q must define exactly one of run or prompt", task.ID)
	}
	if hasRun && strings.TrimSpace(task.Run[0]) == "" {
		return fmt.Errorf("task %q argv command is empty", task.ID)
	}
	if hasPrompt && strings.TrimSpace(task.Model) == "" && strings.TrimSpace(defaultModel) == "" {
		return fmt.Errorf("prompt task %q requires a model", task.ID)
	}
	if task.Attempts < 0 || task.Attempts > 5 || attemptsExplicit && task.Attempts == 0 {
		return fmt.Errorf("task %q attempts must be between 1 and 5", task.ID)
	}
	if task.ExpectExit < 0 || task.ExpectExit > 255 {
		return fmt.Errorf("task %q expected_exit must be between 0 and 255", task.ID)
	}
	if hasPrompt && task.ExpectExit != 0 {
		return fmt.Errorf("task %q expected_exit is only valid for run tasks", task.ID)
	}
	if task.Timeout != "" {
		timeout, err := time.ParseDuration(task.Timeout)
		if err != nil {
			return fmt.Errorf("task %q has invalid timeout %q: %w", task.ID, task.Timeout, err)
		}
		if timeout <= 0 {
			return fmt.Errorf("task %q timeout must be positive", task.ID)
		}
	}
	return nil
}

func isSafePathComponent(id string) bool {
	if id == "." || id == ".." || strings.HasPrefix(id, ".") || strings.HasSuffix(id, ".") {
		return false
	}
	for i, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || i > 0 && (r == '-' || r == '_' || r == '.') {
			continue
		}
		return false
	}
	return true
}

func selectFeatures(taskID string, selectors []string, features []compiledFeature) ([]compiledFeature, error) {
	if len(selectors) == 0 {
		return append([]compiledFeature(nil), features...), nil
	}
	selected := make(map[string]compiledFeature)
	for _, selector := range selectors {
		if strings.TrimSpace(selector) == "" || path.IsAbs(selector) {
			return nil, fmt.Errorf("task %q has invalid feature selector %q", taskID, selector)
		}
		normalizedSelector := path.Clean(filepath.ToSlash(selector))
		matched := false
		for _, feature := range features {
			ok, err := path.Match(normalizedSelector, feature.uri)
			if err != nil {
				return nil, fmt.Errorf("task %q has invalid feature selector %q: %w", taskID, selector, err)
			}
			if ok {
				matched = true
				selected[feature.uri] = feature
			}
		}
		if !matched {
			return nil, fmt.Errorf("task %q feature selector %q matched no global feature files", taskID, selector)
		}
	}
	result := make([]compiledFeature, 0, len(selected))
	for _, feature := range features {
		if _, ok := selected[feature.uri]; ok {
			result = append(result, feature)
		}
	}
	return result, nil
}

func buildPrompt(prompt string, features []compiledFeature) string {
	var result strings.Builder
	result.WriteString(strings.TrimSpace(prompt))
	result.WriteString("\n\nSelected Gherkin requirements follow as task context; follow the task instructions above.\n")
	for _, feature := range features {
		result.WriteString("\n--- BEGIN GHERKIN: ")
		result.WriteString(feature.uri)
		result.WriteString(" ---\n")
		result.WriteString(feature.source)
		if !strings.HasSuffix(feature.source, "\n") {
			result.WriteByte('\n')
		}
		result.WriteString("--- END GHERKIN: ")
		result.WriteString(feature.uri)
		result.WriteString(" ---\n")
	}
	return result.String()
}

func topologicalTasks(tasksByID map[string]Task) ([]Task, error) {
	indegree := make(map[string]int, len(tasksByID))
	dependents := make(map[string][]string, len(tasksByID))
	for id, task := range tasksByID {
		seen := make(map[string]bool)
		for _, dependency := range task.Needs {
			if _, exists := tasksByID[dependency]; !exists {
				return nil, fmt.Errorf("task %q needs unknown task %q", id, dependency)
			}
			if seen[dependency] {
				return nil, fmt.Errorf("task %q repeats dependency %q", id, dependency)
			}
			seen[dependency] = true
			indegree[id]++
			dependents[dependency] = append(dependents[dependency], id)
		}
	}
	for id := range dependents {
		sort.Strings(dependents[id])
	}

	ready := make([]string, 0, len(tasksByID))
	for id := range tasksByID {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	ordered := make([]Task, 0, len(tasksByID))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, tasksByID[id])
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(tasksByID) {
		return nil, errors.New("workflow contains a dependency cycle")
	}
	return ordered, nil
}
