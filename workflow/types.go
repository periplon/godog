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
	Version int    `json:"version"`
	Name    string `json:"name"`
	Tasks   []Task `json:"tasks"`
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
}
type Result struct {
	Baseline            string       `json:"baseline"`
	Tasks               []TaskResult `json:"tasks"`
	IntegrationWorktree string       `json:"integration_worktree,omitempty"`
	Commit              string       `json:"commit,omitempty"`
}

// Runner executes a compiled plan. Implementations must respect cancellation.
type Runner interface {
	Run(context.Context, *Plan, RunOptions) (*Result, error)
}
