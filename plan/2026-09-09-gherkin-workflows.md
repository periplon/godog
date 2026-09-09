# Gherkin implementation workflows

## Objective
Extend Godog with a deterministic Gherkin + workflow DSL compiler and a runnable implementation DAG. Initially support only Codex for nondeterministic tasks, invoking `codex exec -m MODEL --dangerously-bypass-approvals-and-sandbox PROMPT`. Execute parallel tasks in isolated Git worktrees. Use the feature requirements and DSL to implement and review this extension itself.

## Plan
1. Specify strict versioned YAML DSL, Gherkin acceptance features, and shared Go API.
2. Implement compiler and worktree executor with test-first regressions in independent worktrees; add CLI and acceptance bindings.
3. Bootstrap runner, then run its own implementation/review workflow with real Codex; preserve generated commits and evidence.
4. Integrate findings; run acceptance, race tests, vet, compatibility checks, and independent reviews.
5. Publish focused draft PR with includes-ai-code label; audit requirements and report remaining limitations.

## Design decisions
- Preserve upstream Go module path and existing Godog commands for compatibility.
- DSL explicitly specifies task boundaries and dependencies; compiler sorts files/tasks and expands Gherkin outlines using upstream parser. It never asks a model to guess dependencies.
- Each task receives selected feature scenarios. All supplied scenarios must be assigned to at least one task.
- Deterministic tasks execute argv without a shell. Codex tasks use explicit model, prompt, and YOLO invocation.
- Each task starts from the fixed baseline plus successful dependency commits. Failed tasks block descendants, preserve artifacts, and cause nonzero exit. Integration conflicts are reported, never silently resolved.
- Run artifacts record inputs, statuses, attempts, logs, worktrees, and result commits. Successful results are assembled in a dedicated integration worktree, without changing the caller checkout.
- Bootstrap is necessarily implemented before self-hosted execution; self-hosted tasks must produce actual implementation/test improvements, not only a demonstration.

## Verification
Test first for behavior changes; record expected red/green evidence. Execute DSL through CLI, actual Git worktree operations, fake Codex contract tests, and real Codex dogfooding. Independent reviews cover semantics, execution, and user experience. No claim of absolute absence of flaws.
