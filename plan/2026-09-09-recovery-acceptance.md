# Incremental and recovery acceptance

## Objective

Bind every incremental-planning and execution-resume scenario to executable
Godog acceptance checks using real temporary Git repositories and deterministic
subprocesses.

## Plan

1. Define scenario fixtures and bind all feature steps.
2. Confirm the suite fails while the new public APIs are absent.
3. Integrate the independently implemented APIs and make the acceptance checks pass.
4. Run focused, race, vet, and repository checks; commit only the acceptance suite and this task record.

