# Incremental workflow compiler ledger

- 2026-09-09: Started from root `509cab7` in isolated branch `jmca/workflow-incremental`.
- 2026-09-09: Chose semantic scenario identity from repository-relative URI, feature/rule/scenario identity, outline example values, and duplicate occurrence; source line and column are excluded.
- 2026-09-09: Chose fingerprints from expanded scenario steps plus language, descriptions, tags, rule/background context, and outline metadata. Workflow policy is fingerprinted separately and any change conservatively invalidates every scenario.
- 2026-09-09: Registry scope uses repository-relative spec paths across worktrees; external specs use a hash of their canonical absolute path. Records live under the Git common directory and are trusted only when content-address valid and their integration commit is an ancestor of current HEAD.
- 2026-09-09: TDD red confirmed with `go test ./workflow`: build failed only because `CompileIncremental` and `RecordImplementation` did not yet exist.
- 2026-09-09: Initial implementation passed insertion, no-op, background, policy, outline, registry, and legacy-output tests. Review changed registry trust to accept unmerged executor results but reuse them only after integration, retain downstream verification tasks, validate tracked baselines/inputs, and select the ancestry-maximal snapshot so a semantic reversion cannot reuse superseded evidence.
- 2026-09-09: Added tracked full compilation and public tracking/input validators for executor integration. Policy-changing full reimplementation carries no stale evidence; record publication verifies clean source, exact successful task results, baseline containment, task-commit containment, and prior evidence ancestry.
- 2026-09-09: Green verification: focused workflow tests, workflow race tests, workflow vet, and `go test ./...` pass.
