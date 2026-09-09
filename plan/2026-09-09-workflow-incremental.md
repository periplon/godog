# Incremental workflow compilation

## Objective

Compile only changed or new Gherkin scenario instances using a durable, content-addressed implementation registry outside the source tree.

## Work

1. Add regressions for insertion stability, precise prompts, inherited-context and policy invalidation, dependency prerequisites, registry trust, and corrupt records.
2. Add optional tracking metadata without changing legacy `Compile` output.
3. Implement incremental compilation and immutable record publication under the Git common directory.
4. Run focused, race, full repository, and historical-output checks; review registry and semantic matching independently.
5. Ensure tracked full compilation never reads implementation history and verification descendants retain every prerequisite branch.

## Boundaries

- Own new `workflow/incremental*.go`, this plan and ledger, and minimal additive workflow types.
- Do not modify the executor, CLI, or legacy compiler.
- Do not create a pull request.
