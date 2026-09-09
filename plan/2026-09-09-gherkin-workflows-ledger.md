# Work ledger

- 2026-09-09: Fork verified, cloned to software-factory/godog. Baseline inspected; Go 1.18 module, Cobra CLI, Gherkin v42, YAML v3 already available.
- Installed Codex CLI confirms noninteractive `exec`, `-m`, prompt argument, and `--dangerously-bypass-approvals-and-sandbox` support.
- Architecture review assigned independently. Bootstrap compiler, executor, CLI, and acceptance implementation pending.
- Baseline `go test ./...` passed before behavioral changes.
- CLI TDD red: `go test ./cmd/godog/internal` failed because CreateWorkflowCmd did not exist. Implemented plan/run commands; green awaits compiler/executor integration.
- Architecture reviewer identified explicit expected-exit and timeout requirements for TDD workflows; added shared DSL fields and assigned validation/runtime semantics to owners.
- Added source Gherkin requirements and self-hosted workflow policy. Real self-hosted run pending bootstrap.
- Live Codex probe succeeded with `gpt-5.6-sol`, approval=never and sandbox=danger-full-access; exact CLI adapter invocation is usable with current authentication.
- Independent self-host specification review found missing compiler/CLI repair ownership and outer-run verification recursion. Expanded DSL with two independent reviewers, a dependent reconciliation task, test and vet gates. Outer self-host evidence will be checked after the run rather than recursively bound inside it.
