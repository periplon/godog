# Execution ledger

- Inspected clean main and created isolated jmca/ai-workflow-generate worktree. Base b685d76 includes generated task model fix.
- Requirements: opt-in Codex planner, isolated parallel tasks, configurable per-complexity models, all four reasoning efforts propagated to execution, unchanged deterministic default.
- Verification and independent review pending.

## Implementation and review

- Added opt-in `--generator codex`, allowed task `--models`, planner/default `--model`, `--reasoning-effort`, and executable override. Default deterministic generation remains model-free.
- Structured planner responses preserve exact feature coverage and allow grouped/serial/parallel tasks. Fixed TDD prompts and final review are supplied by the generator; compilation validates the DAG before atomic no-overwrite publication.
- Added optional workflow/task reasoning_effort through compilation, direct-plan validation, execution, incremental fingerprints and durable resume.
- Three specialists contributed implementation and independent architecture/security/compatibility reviews. Final review found no actionable correctness/security issue; help wording corrected.

## Executable evidence

- TDD red then green: missing CLI flags; lost deterministic effort; invalid options; Codex mode ignored; invalid generated policies; compiler/runtime effort support; surviving planner descendants; invalid destination invoking the planner.
- Integration caught a CLI default executable value rejecting deterministic mode; fixed and CLI suite passed. Staticcheck caught capitalized error strings; normalized and reran.
- Post-implementation coverage audit: exact task models/efforts, grouped/serial graphs, final review dependencies, custom instructions, resume integrity and absent-field serialization compatibility.
- Actual generated-DAG execution checks that parallel branches are integrated before review and correct model/effort argv reach the task runner.
- `just check` passed: formatting, both modules' vet/race tests, strict BDD (110 scenarios, 425 steps).
- Live authenticated Codex smoke passed on two isolated alpha/beta packages: two parallel gpt-5.6-luna/low tasks, gpt-5.6-sol/medium integration review, concrete path isolation rationale. Generated YAML compiled with `workflow plan --full`. This exercised generation, not model-written implementation correctness.
- Reference: https://learn.chatgpt.com/docs/non-interactive-mode and installed `codex exec --help` verified structured response, output file and sandbox CLI options.
- Model-generated isolation remains an assessed proposal requiring YAML review; structural validation cannot prove arbitrary future implementation independence.
- Final focused race/staticcheck/recipe-contract checks and draft PR delivery recorded below.
- Final `staticcheck ./...`, `REQUIRE_JUST=1 go test ./internal/devtools`, and `go test -race ./workflow ./cmd/godog/internal` passed after the final production changes.

## Delivery

- Draft PR: https://github.com/periplon/godog/pull/5, required contribution label applied.
- Implementation commit: c5612dde71a488ad149ef5d3546695447c3d6288.
- Worktree clean after commit/push; hosted checks started. No merge or deployment performed.
