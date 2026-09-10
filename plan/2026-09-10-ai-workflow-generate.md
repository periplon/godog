# Codex workflow generation

Add opt-in Codex planning while preserving deterministic generation. Codex should inspect selected requirements and repository context, choose dependencies for isolated work, and select per-task models and reasoning effort (low, medium, high, xhigh). Validate every candidate before atomic publication.

1. Add regression tests and implement reasoning effort through schema, compiler, executor and resume identity.
2. Add tested Codex generation, constrained planning response, coverage/dependency validation and CLI options.
3. Document usage, run repository gates, and obtain independent correctness/security reviews; fix findings with regressions.
4. Commit plan and ledger with changes and publish a labeled draft PR.

## Completion audit

Implementation and independent reviews complete. Regression tests cover option/default compatibility, full selected-feature coverage, grouped and parallel dependency graphs, model/effort propagation, process cleanup, resume identity and atomic output. Live Codex planning confirmed differentiated task choices. Full local gates and final changed-package gates passed; draft PR #5 is published; hosted validation is monitored separately.

Documentation-only usage/help updates are exempt from TDD; runnable examples and CLI behavior have executable checks. No durable learning beyond existing process conventions warranted a LEARNINGS.md addition.
