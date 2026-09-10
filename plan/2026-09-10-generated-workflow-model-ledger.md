# Ledger

- Installed binary matches main c1fffc8. Reproduced workflow-level `model: selected-model` alongside task-level `model: ""`.
- Compiler inherits the workflow default for empty task models; existing tests inspect compiled tasks and therefore miss the misleading raw YAML.
- Scope: explicitly serialize the selected model on generated implementation and final review tasks. Preserve workflow default and compilation behavior.
- Regression confirmed failure on both implementation tasks and review-all: serialized model was empty. First implementation candidate passed the regression.
- CLI smoke check now emits the selected model at workflow and task levels; workflow plan compiles successfully. An initial smoke invocation used an incorrect temporary-directory path and failed with no matching features; rerun with a fresh fixture passed.
- Review angles: raw YAML includes normalized model values; compiled-model inheritance and deterministic generation remain covered by existing tests. Explicit task models override later workflow-default edits, now documented.
- Focused go vet and git diff --check passed. Documentation changes are exempt from TDD; regression covers the serialization change.
- `go test -race ./workflow ./cmd/godog/internal` passed, including generation, compiler, CLI, and executor integration coverage.
