# Self-host evidence checker ledger

- 2026-09-09: Defined the checker boundary: inspect an already completed run only; do not invoke Codex or the workflow runner.
- 2026-09-09: Started fixture-based TDD against real temporary Git objects and worktrees.
- Red: `go test ./docs/workflows/checkrun` failed because `check` and the evidence contract did not exist.
- Green: complete evidence fixture accepted and malformed task status, timing, model, resolution, commit, and change evidence rejected.
- Retry rule learned: a local variable named `resolution` shadowed the resolution record type; rename the value before verification.
- Focused checker tests and vet pass. Repository-wide tests currently cannot compile the workflow CLI because the parallel compiler/executor branches have not yet been integrated (`workflow.Compile` and `workflow.Execute` are undefined in this isolated branch).
