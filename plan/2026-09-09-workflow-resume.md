# Durable workflow resume

## Objective

Resume interrupted implementation workflows from durable evidence without rerunning validated successes, changing the recorded plan/baseline/repository, or reusing unsafe partial task state.

## Plan

1. Add real-Git regressions for idempotent all-success resume, failed-task retry, checkpoint recovery, exclusive locking, and altered artifact rejection.
2. Record an immutable run manifest, append restart history, and acquire a nonblocking execution lock whose Unix ownership survives in child processes.
3. Persist successful task checkpoints before the aggregate result journal and validate their commits, dependencies, worktrees, and plan identity before reuse.
4. Reconstruct unfinished tasks from the fixed baseline and successful dependencies in new resume worktrees; retain earlier partial worktrees and logs.
5. Rebuild or validate integration idempotently, preserve cumulative attempts, and verify the clean source repository identity.
6. Run focused race tests, vet, and cross-platform compilation.

