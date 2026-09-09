# Workflow compiler

## Objective

Compile strict version 1 workflow YAML and Gherkin feature files into a deterministic, validated execution plan.

## Work

1. Add regression tests for strict YAML, graph and action validation, feature selection and coverage, deterministic ordering, and rich Gherkin expansion.
2. Implement `workflow.Compile` against the shared workflow types.
3. Run focused tests, the workflow package race tests, formatting, and repository checks.
4. Review validation semantics and deterministic output independently, then commit the compiler and its evidence.

## Boundaries

- Own `workflow/compiler.go`, `workflow/compiler_test.go`, and this task's plan and ledger.
- Do not modify the shared workflow types.
- Do not create a pull request.
