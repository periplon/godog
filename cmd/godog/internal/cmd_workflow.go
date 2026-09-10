package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cucumber/godog/workflow"
	"github.com/spf13/cobra"
)

// CreateWorkflowCmd creates implementation workflow commands.
func CreateWorkflowCmd() *cobra.Command {
	root := &cobra.Command{Use: "workflow", Short: "Compile and execute implementation workflows from Gherkin"}
	var generateOpts workflow.GenerateOptions
	generateCmd := &cobra.Command{
		Use:          "generate FEATURE...",
		Short:        "Generate a workflow YAML from feature files from feature files",
		Long:         "Generate a workflow with implementation prompts and a final review. By default generation is deterministic, with one implementation task per feature. Use --generator codex to let Codex inspect the repository and plan dependencies, models, and reasoning efforts. Generation never executes workflow tasks. Quote globs to resolve them relative to --repo. Review the generated policy before execution.",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return workflow.Generate(cmd.Context(), args, generateOpts)
		},
	}
	generateCmd.Flags().StringVar(&generateOpts.Output, "output", "", "New YAML file (relative to current directory; parent must exist)")
	generateCmd.Flags().StringVar(&generateOpts.Dir, "repo", ".", "Base directory for feature paths and globs")
	generateCmd.Flags().StringVar(&generateOpts.Model, "model", "", "Default task model and Codex planner model")
	generateCmd.Flags().StringVar(&generateOpts.Prompt, "prompt", "", "Additional instructions for generated implementation and review tasks")
	generateCmd.Flags().StringVar(&generateOpts.Generator, "generator", "deterministic", "Generation method: deterministic or codex")
	generateCmd.Flags().StringVar(&generateOpts.CodexBinary, "codex", "", "Codex executable for planning (default codex)")
	generateCmd.Flags().StringSliceVar(&generateOpts.Models, "models", nil, "Allowed task models for Codex planning (comma-separated; defaults to --model)")
	generateCmd.Flags().StringVar(&generateOpts.ReasoningEffort, "reasoning-effort", "", "Planner/default task effort: low, medium, high, or xhigh")
	_ = generateCmd.MarkFlagRequired("output")
	_ = generateCmd.MarkFlagRequired("model")
	var planRepo string
	var planFull, runFull bool
	planCmd := &cobra.Command{
		SilenceUsage: true,
		Use:          "plan SPEC",
		Short:        "Print a deterministic JSON plan without executing tasks",
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := compileWorkflow(cmd, args[0], planRepo, planFull)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(plan)
		},
	}
	planCmd.Flags().StringVar(&planRepo, "repo", ".", "Git repository whose implementation history is used")
	planCmd.Flags().BoolVar(&planFull, "full", false, "Plan all scenarios, ignoring implementation history")
	var opts workflow.RunOptions
	runCmd := &cobra.Command{
		SilenceUsage: true,
		Use:          "run SPEC",
		Short:        "Execute tasks in isolated Git worktrees",
		Args:         cobra.ExactArgs(1),
		Long:         "Execute a workflow in isolated Git worktrees. Codex tasks run with approvals and sandbox disabled (YOLO). Only execute trusted workflow specifications. The caller checkout is not updated; results are preserved in an integration worktree.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Jobs < 1 {
				return fmt.Errorf("jobs must be positive")
			}
			compile := workflow.CompileIncremental
			if runFull {
				compile = workflow.CompileFull
			}
			plan, err := compile(cmd.Context(), args[0], opts.Dir)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			result, runErr := workflow.Execute(ctx, plan, opts)
			if result != nil {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(result); err != nil {
					return err
				}
			}
			return runErr
		},
	}
	runCmd.Flags().StringVar(&opts.Dir, "repo", ".", "Git repository to implement in (must be clean)")
	runCmd.Flags().StringVar(&opts.OutputDir, "output-dir", "", "New directory for run artifacts (default: temporary directory)")
	runCmd.Flags().IntVarP(&opts.Jobs, "jobs", "j", 1, "Maximum parallel tasks")
	runCmd.Flags().StringVar(&opts.CodexBinary, "codex", "codex", "Codex executable")
	runCmd.Flags().BoolVar(&runFull, "full", false, "Run all scenarios, ignoring implementation history")
	var resumeOpts workflow.RunOptions
	resumeCmd := &cobra.Command{
		Use: "resume RUN_DIR", Short: "Retry unfinished steps from a saved execution", Args: cobra.ExactArgs(1), SilenceUsage: true,
		Long: "Resume the saved plan at its original source baseline. Successful steps are validated and reused. Each explicit restart grants unfinished tasks a fresh bounded attempt budget; cumulative attempts and prior artifacts are retained.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if resumeOpts.Jobs < 1 {
				return fmt.Errorf("jobs must be positive")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			result, runErr := workflow.Resume(ctx, args[0], resumeOpts)
			if result != nil {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(result); err != nil {
					return err
				}
			}
			return runErr
		},
	}
	resumeCmd.Flags().StringVar(&resumeOpts.Dir, "repo", "", "Require this repository to match the saved execution (default: saved repository)")
	resumeCmd.Flags().IntVarP(&resumeOpts.Jobs, "jobs", "j", 1, "Maximum parallel tasks")
	resumeCmd.Flags().StringVar(&resumeOpts.CodexBinary, "codex", "codex", "Codex executable")
	root.AddCommand(generateCmd, planCmd, runCmd, resumeCmd)
	return root
}

func compileWorkflow(cmd *cobra.Command, spec, repo string, full bool) (*workflow.Plan, error) {
	if full {
		return workflow.Compile(spec)
	}
	return workflow.CompileIncremental(cmd.Context(), spec, repo)
}
