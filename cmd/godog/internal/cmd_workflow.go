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
func CreateWorkflowCmd() cobra.Command {
	root := cobra.Command{Use: "workflow", Short: "Compile and execute implementation workflows from Gherkin"}
	planCmd := &cobra.Command{
		Use: "plan SPEC", Short: "Print a deterministic JSON plan without executing tasks", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := workflow.Compile(args[0])
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(plan)
		},
	}
	var opts workflow.RunOptions
	runCmd := &cobra.Command{
		Use: "run SPEC", Short: "Execute tasks in isolated Git worktrees", Args: cobra.ExactArgs(1),
		Long: "Execute a workflow in isolated Git worktrees. Codex tasks run with approvals and sandbox disabled (YOLO). Only execute trusted workflow specifications. The caller checkout is not updated; results are preserved in an integration worktree.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Jobs < 1 {
				return fmt.Errorf("jobs must be positive")
			}
			plan, err := workflow.Compile(args[0])
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
	root.AddCommand(planCmd, runCmd)
	return root
}
