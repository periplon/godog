package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	gherkin "github.com/cucumber/gherkin/go/v42"
	"github.com/cucumber/messages/go/v34"
)

const (
	planTrackingVersion = 1
	registryVersion     = 1
)

type implementationRecord struct {
	Version           int                `json:"version"`
	Spec              string             `json:"spec"`
	PolicyFingerprint string             `json:"policy_fingerprint"`
	IntegrationCommit string             `json:"integration_commit"`
	Scenarios         []ScenarioTracking `json:"scenarios"`
}

type semanticScenario struct {
	tracking ScenarioTracking
	scenario Scenario
	context  semanticContext
}

type semanticContext struct {
	URI               string           `json:"uri"`
	Language          string           `json:"language"`
	Feature           semanticScope    `json:"feature"`
	FeatureBackground *semanticScope   `json:"feature_background,omitempty"`
	Rule              *semanticScope   `json:"rule,omitempty"`
	RuleBackground    *semanticScope   `json:"rule_background,omitempty"`
	Scenario          semanticScope    `json:"scenario"`
	Examples          *semanticExample `json:"examples,omitempty"`
	ExpandedName      string           `json:"expanded_name"`
	ExpandedSteps     []string         `json:"expanded_steps"`
}

type semanticScope struct {
	Keyword     string   `json:"keyword"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

type semanticExample struct {
	Keyword     string   `json:"keyword"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Header      []string `json:"header,omitempty"`
	Values      []string `json:"values,omitempty"`
}

type astScenarioContext struct {
	feature           semanticScope
	featureBackground *semanticScope
	rule              *semanticScope
	ruleBackground    *semanticScope
	scenario          semanticScope
}

type exampleRowContext struct {
	example semanticExample
}

type trackedCompilation struct {
	plan              *Plan
	spec              Spec
	semanticScenarios []semanticScenario
	commonDir         string
}

// CompileIncremental compiles a workflow and selects only scenario instances
// whose semantics have not been recorded at an integrated ancestor of HEAD.
func CompileIncremental(ctx context.Context, specPath, repoDir string) (*Plan, error) {
	compiled, err := buildFullTracking(ctx, specPath, repoDir)
	if err != nil {
		return nil, err
	}
	full := compiled.plan
	spec := compiled.spec
	semanticScenarios := compiled.semanticScenarios
	commonDir := compiled.commonDir
	tracking := full.Tracking
	head := tracking.Baseline
	scope := tracking.Spec
	policyFingerprint := tracking.PolicyFingerprint

	baseNames, bases, err := latestTrustedSnapshots(ctx, commonDir, scope, head)
	if err != nil {
		return nil, err
	}
	if len(bases) == 1 && bases[0].PolicyFingerprint == policyFingerprint {
		tracking.BaseRecords = append(tracking.BaseRecords, baseNames...)
	}
	changed := changedScenarioKeys(semanticScenarios, policyFingerprint, bases)
	if len(changed) == 0 {
		full.Tasks = []Task{}
		tracking.NoOp = true
		return full, nil
	}

	originalTasks := make(map[string]TaskSpec, len(spec.Tasks))
	for _, task := range spec.Tasks {
		originalTasks[task.ID] = task
	}
	semanticByID := make(map[string]*semanticScenario, len(semanticScenarios))
	for i := range semanticScenarios {
		semanticByID[semanticScenarios[i].scenario.ID] = &semanticScenarios[i]
	}
	affected := make(map[string]bool)
	changedForTask := make(map[string][]*semanticScenario)
	fullByID := make(map[string]Task, len(full.Tasks))
	for _, task := range full.Tasks {
		fullByID[task.ID] = task
		for _, scenario := range task.Scenarios {
			semantic := semanticByID[scenario.ID]
			if semantic != nil && changed[semantic.tracking.Key] {
				affected[task.ID] = true
				changedForTask[task.ID] = append(changedForTask[task.ID], semantic)
			}
		}
	}
	include := make(map[string]bool)
	dependents := make(map[string][]string)
	for _, task := range full.Tasks {
		for _, need := range task.Needs {
			dependents[need] = append(dependents[need], task.ID)
		}
	}
	var includePrerequisites func(string)
	includePrerequisites = func(id string) {
		if include[id] {
			return
		}
		include[id] = true
		for _, need := range fullByID[id].Needs {
			includePrerequisites(need)
		}
	}
	for id := range affected {
		includePrerequisites(id)
	}
	var includeVerification func(string)
	includeVerification = func(id string) {
		for _, dependent := range dependents[id] {
			includePrerequisites(dependent)
			includeVerification(dependent)
		}
	}
	for id := range affected {
		includeVerification(id)
	}

	incrementalTasks := make([]Task, 0, len(include))
	for _, task := range full.Tasks {
		if !include[task.ID] {
			continue
		}
		selected := changedForTask[task.ID]
		keys := make([]string, 0, len(selected))
		task.Scenarios = make([]Scenario, 0, len(selected))
		for _, semantic := range selected {
			task.Scenarios = append(task.Scenarios, semantic.scenario)
			keys = append(keys, semantic.tracking.Key)
		}
		reason := "prerequisite"
		if affected[task.ID] {
			reason = "changed"
		} else if isDownstreamOf(task.ID, affected, fullByID) {
			reason = "verification"
		}
		if strings.TrimSpace(task.Prompt) != "" {
			original := originalTasks[task.ID].Prompt
			if reason == "changed" {
				task.Prompt = incrementalPrompt(original, selected)
			} else if reason == "prerequisite" {
				task.Prompt = prerequisitePrompt(original)
			} else {
				task.Prompt = verificationPrompt(original)
			}
		}
		tracking.Tasks = append(tracking.Tasks, IncrementalTask{ID: task.ID, Reason: reason, ScenarioKeys: keys})
		incrementalTasks = append(incrementalTasks, task)
	}
	full.Tasks = incrementalTasks
	return full, nil
}

func buildFullTracking(ctx context.Context, specPath, repoDir string) (*trackedCompilation, error) {
	if ctx == nil {
		return nil, errors.New("workflow: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	full, err := Compile(specPath)
	if err != nil {
		return nil, err
	}
	spec, _, _, err := readSpec(specPath)
	if err != nil {
		return nil, err
	}
	repo, commonDir, head, err := incrementalRepository(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	scope, externalSpecPath, err := incrementalSpecScope(specPath, repo)
	if err != nil {
		return nil, err
	}
	policyFingerprint, err := semanticHash(spec)
	if err != nil {
		return nil, fmt.Errorf("workflow: fingerprint policy: %w", err)
	}
	semanticScenarios, err := compileSemanticScenarios(specPath, spec)
	if err != nil {
		return nil, err
	}
	if err := associateScenarioTasks(full, semanticScenarios); err != nil {
		return nil, err
	}

	tracking := &PlanTracking{
		Version:           planTrackingVersion,
		Spec:              scope,
		SpecPath:          externalSpecPath,
		Baseline:          head,
		PolicyFingerprint: policyFingerprint,
		Scenarios:         scenarioTrackingSnapshot(semanticScenarios),
	}
	tracking.InputFingerprint, err = trackingInputFingerprint(policyFingerprint, tracking.Scenarios)
	if err != nil {
		return nil, fmt.Errorf("workflow: fingerprint tracked inputs: %w", err)
	}
	full.Tracking = tracking
	return &trackedCompilation{plan: full, spec: spec, semanticScenarios: semanticScenarios, commonDir: commonDir}, nil
}

// CompileFull produces an all-scenario plan with tracking metadata suitable
// for recording after a full execution. Compile remains the stateless legacy API.
func CompileFull(ctx context.Context, specPath, repoDir string) (*Plan, error) {
	compiled, err := buildFullTracking(ctx, specPath, repoDir)
	if err != nil {
		return nil, err
	}
	full := compiled.plan
	full.Tracking.NoOp = false
	full.Tracking.Tasks = nil
	keyByID := make(map[string]string, len(full.Tracking.Scenarios))
	for _, scenario := range full.Tracking.Scenarios {
		keyByID[scenario.ID] = scenario.Key
	}
	for _, task := range full.Tasks {
		keys := make([]string, 0, len(task.Scenarios))
		for _, scenario := range task.Scenarios {
			keys = append(keys, keyByID[scenario.ID])
		}
		full.Tracking.Tasks = append(full.Tracking.Tasks, IncrementalTask{ID: task.ID, Reason: "changed", ScenarioKeys: keys})
	}
	return full, nil
}

// ValidateTracking checks that incremental metadata agrees with its executable plan.
func ValidateTracking(plan *Plan) error {
	if plan == nil || plan.Tracking == nil {
		return errors.New("workflow: plan has no tracking metadata")
	}
	tracking := plan.Tracking
	if tracking.Version != planTrackingVersion {
		return fmt.Errorf("workflow: unsupported tracking version %d", tracking.Version)
	}
	if !validSpecScope(tracking.Spec) || !validGitObjectID(tracking.Baseline) || !validSemanticHash(tracking.PolicyFingerprint) || !validSemanticHash(tracking.InputFingerprint) {
		return errors.New("workflow: invalid tracking scope or policy fingerprint")
	}
	if strings.HasPrefix(tracking.Spec, "external:") {
		if tracking.SpecPath == "" || "external:"+semanticStringHash(filepath.Clean(tracking.SpecPath)) != tracking.Spec {
			return errors.New("workflow: external spec path does not match tracking scope")
		}
	} else if tracking.SpecPath != "" {
		return errors.New("workflow: repository spec scope must not carry an external path")
	}
	byKey := make(map[string]ScenarioTracking, len(tracking.Scenarios))
	byID := make(map[string]string, len(tracking.Scenarios))
	for _, scenario := range tracking.Scenarios {
		if !validSemanticHash(scenario.Key) || !validSemanticHash(scenario.Fingerprint) || strings.TrimSpace(scenario.ID) == "" {
			return errors.New("workflow: invalid tracked scenario")
		}
		if _, exists := byKey[scenario.Key]; exists {
			return fmt.Errorf("workflow: duplicate tracked scenario key %q", scenario.Key)
		}
		if _, exists := byID[scenario.ID]; exists {
			return fmt.Errorf("workflow: duplicate tracked scenario ID %q", scenario.ID)
		}
		seenTasks := make(map[string]bool)
		for _, taskID := range scenario.Tasks {
			if strings.TrimSpace(taskID) == "" || seenTasks[taskID] {
				return fmt.Errorf("workflow: invalid task assignment for scenario %q", scenario.Key)
			}
			seenTasks[taskID] = true
		}
		if len(seenTasks) == 0 {
			return fmt.Errorf("workflow: tracked scenario %q has no task assignment", scenario.Key)
		}
		byKey[scenario.Key] = scenario
		byID[scenario.ID] = scenario.Key
	}
	wantInput, err := trackingInputFingerprint(tracking.PolicyFingerprint, tracking.Scenarios)
	if err != nil || wantInput != tracking.InputFingerprint {
		return errors.New("workflow: tracked input fingerprint does not match scenario metadata")
	}
	if tracking.NoOp {
		if len(plan.Tasks) != 0 || len(tracking.Tasks) != 0 {
			return errors.New("workflow: no-op tracking must have no executable tasks")
		}
		return nil
	}
	if len(plan.Tasks) == 0 || len(tracking.Tasks) != len(plan.Tasks) {
		return errors.New("workflow: tracking does not cover executable tasks")
	}
	planTasks := make(map[string]Task, len(plan.Tasks))
	for _, task := range plan.Tasks {
		planTasks[task.ID] = task
	}
	seen := make(map[string]bool)
	for _, trackedTask := range tracking.Tasks {
		task, exists := planTasks[trackedTask.ID]
		if !exists || seen[trackedTask.ID] {
			return fmt.Errorf("workflow: invalid tracked task %q", trackedTask.ID)
		}
		seen[trackedTask.ID] = true
		if trackedTask.Reason != "changed" && trackedTask.Reason != "prerequisite" && trackedTask.Reason != "verification" {
			return fmt.Errorf("workflow: invalid incremental reason %q", trackedTask.Reason)
		}
		if trackedTask.Reason != "changed" && (len(trackedTask.ScenarioKeys) != 0 || len(task.Scenarios) != 0) {
			return fmt.Errorf("workflow: %s task %q must have no scenario targets", trackedTask.Reason, trackedTask.ID)
		}
		if trackedTask.Reason == "changed" && len(trackedTask.ScenarioKeys) == 0 {
			return fmt.Errorf("workflow: changed task %q has no scenario targets", trackedTask.ID)
		}
		if len(trackedTask.ScenarioKeys) != len(task.Scenarios) {
			return fmt.Errorf("workflow: task %q scenario tracking mismatch", trackedTask.ID)
		}
		for i, key := range trackedTask.ScenarioKeys {
			scenario, exists := byKey[key]
			if !exists || scenario.ID != task.Scenarios[i].ID {
				return fmt.Errorf("workflow: task %q has invalid scenario target %q", trackedTask.ID, key)
			}
		}
	}
	return nil
}

// ValidatePlanInputs proves that tracked semantics and baseline still match
// the selected repository before execution or recording.
func ValidatePlanInputs(ctx context.Context, plan *Plan, repoDir string) error {
	if ctx == nil {
		return errors.New("workflow: nil context")
	}
	if err := ValidateTracking(plan); err != nil {
		return err
	}
	repo, _, head, err := incrementalRepository(ctx, repoDir)
	if err != nil {
		return err
	}
	if head != plan.Tracking.Baseline {
		return fmt.Errorf("workflow: tracked baseline %q does not match current HEAD %q", plan.Tracking.Baseline, head)
	}
	specPath := plan.Tracking.SpecPath
	if strings.HasPrefix(plan.Tracking.Spec, "repo:") {
		specPath = filepath.Join(repo, filepath.FromSlash(strings.TrimPrefix(plan.Tracking.Spec, "repo:")))
	}
	full, err := Compile(specPath)
	if err != nil {
		return fmt.Errorf("workflow: recompile tracked inputs: %w", err)
	}
	spec, _, _, err := readSpec(specPath)
	if err != nil {
		return err
	}
	policy, err := semanticHash(spec)
	if err != nil {
		return err
	}
	semantics, err := compileSemanticScenarios(specPath, spec)
	if err != nil {
		return err
	}
	if err := associateScenarioTasks(full, semantics); err != nil {
		return err
	}
	fingerprint, err := trackingInputFingerprint(policy, scenarioTrackingSnapshot(semantics))
	if err != nil {
		return err
	}
	if fingerprint != plan.Tracking.InputFingerprint {
		return errors.New("workflow: tracked inputs no longer match workflow policy and features")
	}
	return nil
}

// RecordImplementation publishes an immutable semantic snapshot after every
// task succeeded and the integration commit contains the baseline and tasks.
func RecordImplementation(ctx context.Context, plan *Plan, result *Result, repoDir string) error {
	if ctx == nil {
		return errors.New("workflow: nil context")
	}
	if err := ValidateTracking(plan); err != nil {
		return err
	}
	if plan.Tracking.NoOp || len(plan.Tasks) == 0 {
		return errors.New("workflow: no-op plan has no implementation execution to record")
	}
	if result == nil || strings.TrimSpace(result.Commit) == "" {
		return errors.New("workflow: implementation result has no integration commit")
	}
	if err := validateSuccessfulResult(plan, result); err != nil {
		return err
	}
	if err := ValidatePlanInputs(ctx, plan, repoDir); err != nil {
		return err
	}
	status, err := incrementalGitOutput(ctx, repoDir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return errors.New("workflow: source repository must be clean before recording implementation")
	}
	_, commonDir, _, err := incrementalRepository(ctx, repoDir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(result.Baseline) == "" {
		return errors.New("workflow: implementation result has no baseline")
	}
	if result.Baseline != plan.Tracking.Baseline {
		return fmt.Errorf("workflow: result baseline %q does not match tracked baseline %q", result.Baseline, plan.Tracking.Baseline)
	}
	if err := requireAncestor(ctx, repoDir, result.Baseline, result.Commit); err != nil {
		return fmt.Errorf("workflow: result baseline is not an ancestor of integration commit: %w", err)
	}
	for _, task := range result.Tasks {
		if strings.TrimSpace(task.Commit) == "" {
			return fmt.Errorf("workflow: successful task %q has no commit", task.ID)
		}
		if err := requireAncestor(ctx, repoDir, task.Commit, result.Commit); err != nil {
			return fmt.Errorf("workflow: task commit %q is not contained in integration commit: %w", task.ID, err)
		}
	}
	if err := validateCarriedEvidence(ctx, commonDir, plan, result.Commit); err != nil {
		return err
	}
	record := implementationRecord{
		Version:           registryVersion,
		Spec:              plan.Tracking.Spec,
		PolicyFingerprint: plan.Tracking.PolicyFingerprint,
		IntegrationCommit: result.Commit,
		Scenarios:         append([]ScenarioTracking(nil), plan.Tracking.Scenarios...),
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("workflow: encode implementation record: %w", err)
	}
	content = append(content, '\n')
	digest := sha256.Sum256(content)
	directory := registryScopeDirectory(commonDir, record.Spec)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("workflow: create implementation registry: %w", err)
	}
	path := filepath.Join(directory, hex.EncodeToString(digest[:])+".json")
	if err := publishImmutable(path, content); err != nil {
		return fmt.Errorf("workflow: publish implementation record: %w", err)
	}
	return nil
}

func validateCarriedEvidence(ctx context.Context, commonDir string, plan *Plan, integrationCommit string) error {
	evidence := make(map[string]string)
	for _, name := range plan.Tracking.BaseRecords {
		record, err := readImplementationRecord(commonDir, plan.Tracking.Spec, name)
		if err != nil {
			return err
		}
		if record.PolicyFingerprint != plan.Tracking.PolicyFingerprint {
			return fmt.Errorf("workflow: base record %q has a different policy", name)
		}
		if err := requireAncestor(ctx, commonDir, record.IntegrationCommit, integrationCommit); err != nil {
			return fmt.Errorf("workflow: base record %q is not contained in integration commit", name)
		}
		for _, scenario := range record.Scenarios {
			evidence[scenario.Key] = scenario.Fingerprint
		}
	}
	targeted := make(map[string]map[string]bool)
	for _, task := range plan.Tracking.Tasks {
		for _, key := range task.ScenarioKeys {
			if targeted[key] == nil {
				targeted[key] = make(map[string]bool)
			}
			targeted[key][task.ID] = true
		}
	}
	for _, scenario := range plan.Tracking.Scenarios {
		if evidence[scenario.Key] == scenario.Fingerprint {
			continue
		}
		for _, taskID := range scenario.Tasks {
			if !targeted[scenario.Key][taskID] {
				return fmt.Errorf("workflow: scenario %q has neither eligible prior evidence nor a successful target task %q", scenario.Key, taskID)
			}
		}
	}
	return nil
}

func validateSuccessfulResult(plan *Plan, result *Result) error {
	if len(result.Tasks) != len(plan.Tasks) {
		return errors.New("workflow: result is not a successful full execution of the plan")
	}
	want := make(map[string]bool, len(plan.Tasks))
	for _, task := range plan.Tasks {
		want[task.ID] = true
	}
	for _, task := range result.Tasks {
		if !want[task.ID] || task.Status != "success" {
			return errors.New("workflow: result is not a successful full execution of the plan")
		}
		delete(want, task.ID)
	}
	if len(want) != 0 {
		return errors.New("workflow: result is not a successful full execution of the plan")
	}
	return nil
}

func compileSemanticScenarios(specPath string, spec Spec) ([]semanticScenario, error) {
	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return nil, fmt.Errorf("workflow: resolve spec path: %w", err)
	}
	features, err := compileFeatures(filepath.Dir(absSpec), spec.Features)
	if err != nil {
		return nil, err
	}
	result := make([]semanticScenario, 0)
	for _, feature := range features {
		content, err := os.ReadFile(filepath.Join(filepath.Dir(absSpec), filepath.FromSlash(feature.uri)))
		if err != nil {
			return nil, fmt.Errorf("workflow: read semantic feature %q: %w", feature.uri, err)
		}
		ids := &messages.Incrementing{}
		document, err := gherkin.ParseGherkinDocument(bytes.NewReader(content), ids.NewId)
		if err != nil {
			return nil, fmt.Errorf("workflow: parse semantic feature %q: %w", feature.uri, err)
		}
		pickles := gherkin.Pickles(*document, feature.uri, ids.NewId)
		scenarioContexts, rows := indexAST(document.Feature)
		occurrences := make(map[string]int)
		for _, pickle := range pickles {
			astContext, example := pickleASTContext(pickle, scenarioContexts, rows)
			if astContext == nil {
				return nil, fmt.Errorf("workflow: scenario %q in %q has no AST context", pickle.Name, feature.uri)
			}
			steps := make([]string, 0, len(pickle.Steps))
			for _, step := range pickle.Steps {
				steps = append(steps, renderStep(step))
			}
			contextValue := semanticContext{
				URI: feature.uri, Language: document.Feature.Language,
				Feature: astContext.feature, FeatureBackground: astContext.featureBackground,
				Rule: astContext.rule, RuleBackground: astContext.ruleBackground,
				Scenario: astContext.scenario, Examples: example,
				ExpandedName: pickle.Name, ExpandedSteps: steps,
			}
			identity := struct {
				URI         string   `json:"uri"`
				Feature     string   `json:"feature"`
				Rule        string   `json:"rule,omitempty"`
				Scenario    string   `json:"scenario"`
				ExampleName string   `json:"example_name,omitempty"`
				Values      []string `json:"values,omitempty"`
			}{URI: feature.uri, Feature: astContext.feature.Name, Scenario: astContext.scenario.Name}
			if astContext.rule != nil {
				identity.Rule = astContext.rule.Name
			}
			if example != nil {
				identity.ExampleName = example.Name
				identity.Values = example.Values
			}
			baseKey, err := semanticHash(identity)
			if err != nil {
				return nil, err
			}
			occurrences[baseKey]++
			key := semanticStringHash(baseKey + "\x00" + strconv.Itoa(occurrences[baseKey]))
			fingerprint, err := semanticHash(contextValue)
			if err != nil {
				return nil, err
			}
			line, column := int64(0), int64(0)
			if pickle.Location != nil {
				line, column = pickle.Location.Line, pickle.Location.Column
			}
			result = append(result, semanticScenario{
				tracking: ScenarioTracking{Key: key, ID: scenarioID(feature.uri, line, column, pickle.Name), Fingerprint: fingerprint},
				scenario: Scenario{ID: scenarioID(feature.uri, line, column, pickle.Name), URI: feature.uri, Name: pickle.Name, Steps: steps},
				context:  contextValue,
			})
		}
	}
	return result, nil
}

func indexAST(feature *messages.Feature) (map[string]*astScenarioContext, map[string]exampleRowContext) {
	scenarios := make(map[string]*astScenarioContext)
	rows := make(map[string]exampleRowContext)
	featureScope := semanticScope{Keyword: feature.Keyword, Name: feature.Name, Description: feature.Description, Tags: semanticTags(feature.Tags)}
	var featureBackground *semanticScope
	for _, child := range feature.Children {
		if child.Background != nil {
			value := semanticScope{Keyword: child.Background.Keyword, Name: child.Background.Name, Description: child.Background.Description}
			featureBackground = &value
		}
	}
	for _, child := range feature.Children {
		if child.Scenario != nil {
			indexASTScenario(child.Scenario, featureScope, featureBackground, nil, nil, scenarios, rows)
		}
		if child.Rule != nil {
			ruleScope := semanticScope{Keyword: child.Rule.Keyword, Name: child.Rule.Name, Description: child.Rule.Description, Tags: semanticTags(child.Rule.Tags)}
			var ruleBackground *semanticScope
			for _, ruleChild := range child.Rule.Children {
				if ruleChild.Background != nil {
					value := semanticScope{Keyword: ruleChild.Background.Keyword, Name: ruleChild.Background.Name, Description: ruleChild.Background.Description}
					ruleBackground = &value
				}
			}
			for _, ruleChild := range child.Rule.Children {
				if ruleChild.Scenario != nil {
					indexASTScenario(ruleChild.Scenario, featureScope, featureBackground, &ruleScope, ruleBackground, scenarios, rows)
				}
			}
		}
	}
	return scenarios, rows
}

func indexASTScenario(scenario *messages.Scenario, feature semanticScope, featureBackground, rule *semanticScope, ruleBackground *semanticScope, scenarios map[string]*astScenarioContext, rows map[string]exampleRowContext) {
	scenarios[scenario.Id] = &astScenarioContext{
		feature: feature, featureBackground: featureBackground, rule: rule, ruleBackground: ruleBackground,
		scenario: semanticScope{Keyword: scenario.Keyword, Name: scenario.Name, Description: scenario.Description, Tags: semanticTags(scenario.Tags)},
	}
	for _, examples := range scenario.Examples {
		base := semanticExample{Keyword: examples.Keyword, Name: examples.Name, Description: examples.Description, Tags: semanticTags(examples.Tags)}
		if examples.TableHeader != nil {
			base.Header = tableValues(examples.TableHeader)
		}
		for _, row := range examples.TableBody {
			value := base
			value.Values = tableValues(row)
			rows[row.Id] = exampleRowContext{example: value}
		}
	}
}

func pickleASTContext(pickle *messages.Pickle, scenarios map[string]*astScenarioContext, rows map[string]exampleRowContext) (*astScenarioContext, *semanticExample) {
	var scenario *astScenarioContext
	var example *semanticExample
	for _, id := range pickle.AstNodeIds {
		if value := scenarios[id]; value != nil {
			scenario = value
		}
		if value, ok := rows[id]; ok {
			copy := value.example
			example = &copy
		}
	}
	return scenario, example
}

func semanticTags(tags []*messages.Tag) []string {
	values := make([]string, 0, len(tags))
	for _, tag := range tags {
		values = append(values, tag.Name)
	}
	sort.Strings(values)
	return values
}

func tableValues(row *messages.TableRow) []string {
	values := make([]string, 0, len(row.Cells))
	for _, cell := range row.Cells {
		values = append(values, cell.Value)
	}
	return values
}

func associateScenarioTasks(plan *Plan, semantics []semanticScenario) error {
	byID := make(map[string]*semanticScenario, len(semantics))
	for i := range semantics {
		byID[semantics[i].scenario.ID] = &semantics[i]
	}
	for _, task := range plan.Tasks {
		for _, scenario := range task.Scenarios {
			semantic := byID[scenario.ID]
			if semantic == nil {
				return fmt.Errorf("workflow: cannot track compiled scenario %q", scenario.ID)
			}
			semantic.tracking.Tasks = append(semantic.tracking.Tasks, task.ID)
		}
	}
	for i := range semantics {
		sort.Strings(semantics[i].tracking.Tasks)
	}
	return nil
}

func scenarioTrackingSnapshot(semantics []semanticScenario) []ScenarioTracking {
	result := make([]ScenarioTracking, len(semantics))
	for i := range semantics {
		result[i] = semantics[i].tracking
		result[i].Tasks = append([]string(nil), semantics[i].tracking.Tasks...)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

func changedScenarioKeys(current []semanticScenario, policy string, bases []implementationRecord) map[string]bool {
	changed := make(map[string]bool)
	if len(bases) != 1 || bases[0].PolicyFingerprint != policy {
		for _, scenario := range current {
			changed[scenario.tracking.Key] = true
		}
		return changed
	}
	previous := make(map[string]string, len(bases[0].Scenarios))
	for _, scenario := range bases[0].Scenarios {
		previous[scenario.Key] = scenario.Fingerprint
	}
	for _, scenario := range current {
		if previous[scenario.tracking.Key] != scenario.tracking.Fingerprint {
			changed[scenario.tracking.Key] = true
		}
	}
	return changed
}

func incrementalPrompt(original string, scenarios []*semanticScenario) string {
	var prompt strings.Builder
	prompt.WriteString(strings.TrimSpace(original))
	prompt.WriteString("\n\nOnly the changed or new scenario instances below are implementation targets for this run. Unlisted scenarios are existing context and must not be reimplemented.\n")
	for _, scenario := range scenarios {
		prompt.WriteString("\n--- BEGIN CHANGED SCENARIO: ")
		prompt.WriteString(scenario.tracking.Key)
		prompt.WriteString(" ---\n")
		writeSemanticContext(&prompt, scenario.context)
		prompt.WriteString("--- END CHANGED SCENARIO: ")
		prompt.WriteString(scenario.tracking.Key)
		prompt.WriteString(" ---\n")
	}
	return prompt.String()
}

func prerequisitePrompt(original string) string {
	return strings.TrimSpace(original) + "\n\nIncremental prerequisite context: this task is required by changed downstream tasks. Its scenarios are unchanged and are not implementation targets in this run."
}

func verificationPrompt(original string) string {
	return strings.TrimSpace(original) + "\n\nIncremental verification context: upstream requirements changed. Verify the integrated change. This task's own scenarios are unchanged and are not implementation targets in this run."
}

func isDownstreamOf(id string, affected map[string]bool, tasks map[string]Task) bool {
	seen := make(map[string]bool)
	var visit func(string) bool
	visit = func(current string) bool {
		if affected[current] {
			return true
		}
		if seen[current] {
			return false
		}
		seen[current] = true
		for _, need := range tasks[current].Needs {
			if visit(need) {
				return true
			}
		}
		return false
	}
	return visit(id)
}

func writeSemanticContext(output *strings.Builder, value semanticContext) {
	fmt.Fprintf(output, "%s: %s\n", value.Feature.Keyword, value.Feature.Name)
	writeTagsAndDescription(output, value.Feature)
	if value.FeatureBackground != nil {
		fmt.Fprintf(output, "%s: %s\n", value.FeatureBackground.Keyword, value.FeatureBackground.Name)
		writeTagsAndDescription(output, *value.FeatureBackground)
	}
	if value.Rule != nil {
		fmt.Fprintf(output, "%s: %s\n", value.Rule.Keyword, value.Rule.Name)
		writeTagsAndDescription(output, *value.Rule)
	}
	if value.RuleBackground != nil {
		fmt.Fprintf(output, "%s: %s\n", value.RuleBackground.Keyword, value.RuleBackground.Name)
		writeTagsAndDescription(output, *value.RuleBackground)
	}
	if len(value.Scenario.Tags) > 0 {
		output.WriteString(strings.Join(value.Scenario.Tags, " "))
		output.WriteByte('\n')
	}
	fmt.Fprintf(output, "%s: %s\n", value.Scenario.Keyword, value.ExpandedName)
	if value.Scenario.Description != "" {
		output.WriteString(value.Scenario.Description)
		output.WriteByte('\n')
	}
	if value.Examples != nil {
		fmt.Fprintf(output, "%s: %s\n", value.Examples.Keyword, value.Examples.Name)
		if len(value.Examples.Tags) > 0 {
			output.WriteString(strings.Join(value.Examples.Tags, " "))
			output.WriteByte('\n')
		}
		if len(value.Examples.Header) > 0 {
			fmt.Fprintf(output, "Example row: %s = %s\n", strings.Join(value.Examples.Header, ", "), strings.Join(value.Examples.Values, ", "))
		}
	}
	output.WriteString("Expanded steps, including inherited backgrounds:\n")
	for _, step := range value.ExpandedSteps {
		output.WriteString("  ")
		output.WriteString(strings.ReplaceAll(step, "\n", "\n  "))
		output.WriteByte('\n')
	}
}

func writeTagsAndDescription(output *strings.Builder, scope semanticScope) {
	if len(scope.Tags) > 0 {
		output.WriteString(strings.Join(scope.Tags, " "))
		output.WriteByte('\n')
	}
	if scope.Description != "" {
		output.WriteString(scope.Description)
		output.WriteByte('\n')
	}
}

func incrementalRepository(ctx context.Context, repoDir string) (string, string, string, error) {
	if repoDir == "" {
		repoDir = "."
	}
	repoBytes, err := incrementalGitOutput(ctx, repoDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", "", fmt.Errorf("workflow: locate incremental repository: %w", err)
	}
	repo, err := filepath.Abs(strings.TrimSpace(string(repoBytes)))
	if err != nil {
		return "", "", "", fmt.Errorf("workflow: resolve incremental repository: %w", err)
	}
	commonBytes, err := incrementalGitOutput(ctx, repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", "", "", fmt.Errorf("workflow: locate Git common directory: %w", err)
	}
	common := strings.TrimSpace(string(commonBytes))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repo, common)
	}
	common, err = filepath.Abs(common)
	if err != nil {
		return "", "", "", fmt.Errorf("workflow: resolve Git common directory: %w", err)
	}
	headBytes, err := incrementalGitOutput(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return "", "", "", fmt.Errorf("workflow: resolve current HEAD: %w", err)
	}
	return repo, common, strings.TrimSpace(string(headBytes)), nil
}

func incrementalSpecScope(specPath, repo string) (string, string, error) {
	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return "", "", fmt.Errorf("workflow: resolve spec scope: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absSpec); resolveErr == nil {
		absSpec = resolved
	}
	relative, err := filepath.Rel(repo, absSpec)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
		return "repo:" + filepath.ToSlash(relative), "", nil
	}
	return "external:" + semanticStringHash(filepath.Clean(absSpec)), filepath.Clean(absSpec), nil
}

func registryScopeDirectory(commonDir, scope string) string {
	return filepath.Join(commonDir, "godog", "implementations", semanticStringHash(scope))
}

func trustedRecords(ctx context.Context, commonDir, scope, head string) ([]string, []implementationRecord, error) {
	directory := registryScopeDirectory(commonDir, scope)
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("workflow: read implementation registry: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var names []string
	var records []implementationRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := entry.Name()
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return nil, nil, fmt.Errorf("workflow: read implementation record %q: %w", name, err)
		}
		if semanticBytesHash(content)+".json" != name {
			return nil, nil, fmt.Errorf("workflow: corrupt implementation record %q: content address mismatch", name)
		}
		record, err := decodeImplementationRecord(content)
		if err != nil {
			return nil, nil, fmt.Errorf("workflow: corrupt implementation record %q: %w", name, err)
		}
		if err := validateImplementationRecord(record, scope); err != nil {
			return nil, nil, fmt.Errorf("workflow: corrupt implementation record %q: %w", name, err)
		}
		if err := requireAncestor(ctx, commonDir, record.IntegrationCommit, head); err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			continue
		}
		names = append(names, strings.TrimSuffix(name, ".json"))
		records = append(records, record)
	}
	return names, records, nil
}

func latestTrustedSnapshots(ctx context.Context, commonDir, scope, head string) ([]string, []implementationRecord, error) {
	names, records, err := trustedRecords(ctx, commonDir, scope, head)
	if err != nil || len(records) < 2 {
		return names, records, err
	}
	maximal := make([]bool, len(records))
	for i := range maximal {
		maximal[i] = true
	}
	for i := range records {
		for j := range records {
			if i == j || records[i].IntegrationCommit == records[j].IntegrationCommit {
				continue
			}
			if err := requireAncestor(ctx, commonDir, records[i].IntegrationCommit, records[j].IntegrationCommit); err == nil {
				maximal[i] = false
				break
			} else if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
		}
	}
	var latestNames []string
	var latestRecords []implementationRecord
	for i := range records {
		if maximal[i] {
			latestNames = append(latestNames, names[i])
			latestRecords = append(latestRecords, records[i])
		}
	}
	if len(latestRecords) != 1 {
		return nil, nil, nil
	}
	return latestNames, latestRecords, nil
}

func readImplementationRecord(commonDir, scope, name string) (implementationRecord, error) {
	if !validSemanticHash(name) {
		return implementationRecord{}, fmt.Errorf("workflow: invalid base record name %q", name)
	}
	path := filepath.Join(registryScopeDirectory(commonDir, scope), name+".json")
	content, err := os.ReadFile(path)
	if err != nil {
		return implementationRecord{}, fmt.Errorf("workflow: read base record %q: %w", name, err)
	}
	if semanticBytesHash(content) != name {
		return implementationRecord{}, fmt.Errorf("workflow: corrupt implementation record %q: content address mismatch", name)
	}
	record, err := decodeImplementationRecord(content)
	if err != nil {
		return implementationRecord{}, fmt.Errorf("workflow: corrupt implementation record %q", name)
	}
	if err := validateImplementationRecord(record, scope); err != nil {
		return implementationRecord{}, fmt.Errorf("workflow: corrupt implementation record %q: %w", name, err)
	}
	return record, nil
}

func validateImplementationRecord(record implementationRecord, scope string) error {
	if record.Version != registryVersion || record.Spec != scope || !validSemanticHash(record.PolicyFingerprint) || !validGitObjectID(record.IntegrationCommit) {
		return errors.New("incompatible metadata")
	}
	seen := make(map[string]bool, len(record.Scenarios))
	for _, scenario := range record.Scenarios {
		if !validSemanticHash(scenario.Key) || !validSemanticHash(scenario.Fingerprint) || strings.TrimSpace(scenario.ID) == "" || seen[scenario.Key] {
			return errors.New("invalid scenario metadata")
		}
		seen[scenario.Key] = true
	}
	return nil
}

func validSemanticHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validSpecScope(value string) bool {
	return strings.HasPrefix(value, "repo:") && len(value) > len("repo:") || strings.HasPrefix(value, "external:") && validSemanticHash(strings.TrimPrefix(value, "external:"))
}

func decodeImplementationRecord(content []byte) (implementationRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var record implementationRecord
	if err := decoder.Decode(&record); err != nil {
		return implementationRecord{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return implementationRecord{}, errors.New("multiple JSON values")
		}
		return implementationRecord{}, err
	}
	return record, nil
}

func requireAncestor(ctx context.Context, repoDir, ancestor, head string) error {
	_, err := incrementalGitOutput(ctx, repoDir, "merge-base", "--is-ancestor", ancestor, head)
	return err
}

func incrementalGitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func semanticHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return semanticBytesHash(encoded), nil
}

func trackingInputFingerprint(policy string, scenarios []ScenarioTracking) (string, error) {
	type inputScenario struct {
		Key         string   `json:"key"`
		Fingerprint string   `json:"fingerprint"`
		Tasks       []string `json:"tasks"`
	}
	inputs := make([]inputScenario, len(scenarios))
	for i, scenario := range scenarios {
		inputs[i] = inputScenario{Key: scenario.Key, Fingerprint: scenario.Fingerprint, Tasks: append([]string(nil), scenario.Tasks...)}
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Key < inputs[j].Key })
	return semanticHash(struct {
		Policy    string          `json:"policy"`
		Scenarios []inputScenario `json:"scenarios"`
	}{Policy: policy, Scenarios: inputs})
}

func semanticStringHash(value string) string {
	return semanticBytesHash([]byte(value))
}

func semanticBytesHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func publishImmutable(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, content) {
			return nil
		}
		return errors.New("content-addressed record already exists with different content")
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".record-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		if existing, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(existing, content) {
			return nil
		}
		return err
	}
	remove = false
	return nil
}
