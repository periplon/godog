# Incremental planning and execution recovery

1. Add Gherkin requirements for implementation records and resumable execution.
2. Test CLI incremental defaults, full-plan override, and explicit resume before implementation.
3. Integrate semantic scenario tracking and durable runner checkpoints from isolated worktrees.
4. Verify actual changed-scenario selection and restart side effects, then run full checks and independent review.

CLI contract: `workflow plan SPEC --repo DIR` and `workflow run SPEC --repo DIR` use implementation history by default; `--full` requests all scenarios. `workflow resume RUN_DIR` uses the saved plan and repository, with runtime job/Codex overrides. Completed implementation is reusable only when its commit is present in source ancestry.
