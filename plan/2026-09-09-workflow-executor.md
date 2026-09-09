# Workflow executor

## Objective

Implement deterministic DAG execution for compiled workflow plans without changing the caller checkout. Each task runs in a detached Git worktree based on one clean baseline plus its successful dependency commits, with bounded retries, durable evidence, cancellation, and a separate integration worktree.

## Plan

1. Add black-box executor tests using temporary Git repositories and fake Codex binaries.
2. Validate runtime inputs, pin the source HEAD, and create a unique external run directory with atomic plan/result snapshots.
3. Schedule ready tasks deterministically up to the configured job limit; block descendants after failures.
4. Run exact argv commands in isolated worktrees, record bounded retry feedback and full logs, and commit successful changes.
5. Merge successful task commits into a dedicated integration worktree, surfacing conflicts explicitly.
6. Run focused, race, formatting, and vet checks; review cancellation, Git isolation, and artifact behavior.

## Constraints

- Preserve every task and integration worktree for diagnosis.
- Never run task commands through an implicit shell.
- Never accept context cancellation as an expected exit.
- Do not modify the shared compiler or workflow types in this branch.

