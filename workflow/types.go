// Package workflow compiles Gherkin and a workflow policy into executable tasks.
package workflow

import "context"

// Spec is the versioned YAML workflow policy.
type Spec struct {
	Version  int        `yaml:"version" json:"version"`
	Name     string     `yaml:"name" json:"name"`
	Features []string   `yaml:"features" json:"features"`
	Model    string     `yaml:"model" json:"model"`
	Tasks    []TaskSpec `yaml:"tasks" json:"tasks"`
}
type TaskSpec struct {
	ExpectExit int      `yaml:"expected_exit" json:"expected_exit"`
	Timeout    string   `yaml:"timeout" json:"timeout,omitempty"`
	ID         string   `yaml:"id" json:"id"`
	Needs      []string `yaml:"needs" json:"needs"`
	Features   []string `yaml:"features" json:"features"`
	Run        []string `yaml:"run" json:"run,omitempty"`
	Prompt     string   `yaml:"prompt" json:"prompt,omitempty"`
	Model      string   `yaml:"model" json:"model,omitempty"`
	Attempts   int      `yaml:"attempts" json:"attempts"`
}
type Scenario struct {
	ID    string   `json:"id"`
	URI   string   `json:"uri"`
	Name  string   `json:"name"`
	Steps []string `json:"steps"`
}
type Task struct {
	TaskSpec
	Scenarios []Scenario `json:"scenarios"`
}
type Plan struct {
	Version  int           `json:"version"`
	Name     string        `json:"name"`
	Tasks    []Task        `json:"tasks"`
	Tracking *PlanTracking `json:"tracking,omitempty"`
}

// PlanTracking records the full semantic input to an incremental plan.
type PlanTracking struct {
	Version               int                `json:"version"`
	Spec                  string             `json:"spec"`
	SpecPath              string             `json:"spec_path,omitempty"`
	Baseline              string             `json:"baseline"`
	PolicyFingerprint     string             `json:"policy_fingerprint"`
	InputFingerprint      string             `json:"input_fingerprint"`
	ProjectionFingerprint string             `json:"projection_fingerprint"`
	BaseRecords           []string           `json:"base_records,omitempty"`
	NoOp                  bool               `json:"no_op,omitempty"`
	Scenarios             []ScenarioTracking `json:"scenarios"`
	Tasks                 []IncrementalTask  `json:"tasks,omitempty"`
}

// ScenarioTracking identifies one scenario instance independently of source lines.
type ScenarioTracking struct {
	Key         string   `json:"key"`
	ID          string   `json:"id"`
	Fingerprint string   `json:"fingerprint"`
	Tasks       []string `json:"tasks"`
}

// IncrementalTask explains why a task is present in an incremental plan.
type IncrementalTask struct {
	ID           string   `json:"id"`
	Reason       string   `json:"reason"`
	ScenarioKeys []string `json:"scenario_keys,omitempty"`
}
type RunOptions struct {
	Dir         string
	OutputDir   string
	Jobs        int
	CodexBinary string
}
type TaskResult struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Log        string `json:"log,omitempty"`
	Error      string `json:"error,omitempty"`
	Checkpoint string `json:"checkpoint,omitempty"`
}

// RunRecord describes one initial or resumed execution of a durable run.
type RunRecord struct {
	Sequence   int          `json:"sequence"`
	Resumed    bool         `json:"resumed"`
	StartedAt  string       `json:"started_at"`
	FinishedAt string       `json:"finished_at,omitempty"`
	Status     string       `json:"status"`
	Tasks      []TaskResult `json:"tasks,omitempty"`
}
type Result struct {
	Baseline            string       `json:"baseline"`
	Repository          string       `json:"repository,omitempty"`
	RunDirectory        string       `json:"run_directory,omitempty"`
	PlanDigest          string       `json:"plan_digest,omitempty"`
	Tasks               []TaskResult `json:"tasks"`
	IntegrationWorktree string       `json:"integration_worktree,omitempty"`
	Commit              string       `json:"commit,omitempty"`
	Runs                []RunRecord  `json:"runs,omitempty"`
}

// Runner executes a compiled plan. Implementations must respect cancellation.
type Runner interface {
	Run(context.Context, *Plan, RunOptions) (*Result, error)
}
