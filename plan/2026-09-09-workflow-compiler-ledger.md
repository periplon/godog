# Workflow compiler ledger

- 2026-09-09: Read the shared workflow plan and API. Compiler entry point is `Compile(specPath string) (*Plan, error)`.
- 2026-09-09: Chose file-level task feature selectors: task globs resolve only within the globally resolved feature set; omitted selectors mean all global feature files.
- 2026-09-09: Chose stable scenario IDs derived from feature URI, pickle source location, and expanded scenario name. Full selected Gherkin source is appended to Codex prompts to preserve descriptions, tags, rules, backgrounds, examples, tables, and docstrings.
- 2026-09-09: TDD red confirmed with `go test ./workflow`: package build failed only because the tested `Compile` symbol was not yet implemented.
- 2026-09-09: First implementation run compiled and exercised all cases. Two test fixtures failed: arbitrary feature text is a valid description, and an omitted task selector correctly selects all global files. Updated the malformed Gherkin to put a step before any scenario and made the coverage fixture select an explicit subset.
- 2026-09-09: Gherkin also accepts step-looking text before a scenario as feature description. Replaced that parse-error fixture with an invalid language directive, which the v42 parser must reject.
- 2026-09-09: Semantic review removed an unconditional implementation instruction from generated context because it contradicted review-only tasks. Added intent-preservation coverage and YAML field-presence handling so omitted attempts defaults to 1 while explicit zero is rejected.
- 2026-09-09: Selector regressions failed as expected: an unmatched global glob was hidden by other matches and task selectors retained a leading `./` while compiled URIs did not. Require every global glob to match and normalize task selector paths before matching.
- 2026-09-09: An explicit scenario with no steps compiled successfully in the next red test. Reject empty pickles as invalid scenarios; an empty task list already returned the intended validation error.
- 2026-09-09: Green verification: `go test ./workflow`, `go test -race ./workflow`, `go vet ./workflow`, and `go test ./...` all pass.
- 2026-09-09: Final review covered semantic fidelity (exact source plus expanded steps), validation/input-loss cases (strict YAML and per-selector matching), and execution determinism (stable scenario hashes and lexical topological scheduling). No blocking compiler findings remain.
