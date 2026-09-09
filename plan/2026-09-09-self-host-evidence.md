# Self-host evidence checker

## Objective

Add a bounded, external verifier for a completed `develop.yaml` workflow run. It
must validate the run artifacts, Git objects, parallel execution, review records,
resolution evidence, and substantive initial task changes without invoking Codex
or recursively running the workflow.

## Plan

1. Build representative successful and malformed run fixtures in temporary Git repositories.
2. Write failing tests for complete evidence and important false-positive cases.
3. Implement the checker as `go run ./docs/workflows/checkrun RUN_DIR`.
4. Run focused and repository-wide tests, then commit the checker with its evidence.

