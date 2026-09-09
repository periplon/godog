# Workflow executor ledger

- 2026-09-09: Inspected the workflow contract and architecture plan on clean baseline `55e6ddd`.
- 2026-09-09: Cherry-picked shared `expected_exit` and `timeout` TaskSpec fields from `29afb0a` as commit `3e59cdf`.
- 2026-09-09: Red test confirmed: `go test ./workflow` failed because the required public `Execute` function was undefined.
- 2026-09-09: Implemented deterministic ready-task scheduling, detached task worktrees, dependency commit merges, bounded retries, exact argv execution, success commits, and a dedicated integration worktree.
- 2026-09-09: Added atomic `plan.json`/`result.json` evidence, full task logs, RFC3339Nano start/finish timestamps, clean-source and external-output invariants, and persistent worktrees on every terminal path.
- 2026-09-09: Review found and fixed an expected-exit error that accepted exit 0 for nonzero expectations, a concurrent stdout/stderr writer race, descendant process leakage on cancellation, incomplete direct-plan validation, unsafe custom `TMPDIR` placement, and non-conventional merge subjects.
- 2026-09-09: Final focused verification passed with `go test -race ./workflow`; repository verification passed with `go test ./...` and `go vet ./...`; Windows amd64 workflow test binary cross-compilation passed.
