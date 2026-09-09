# Self-host evidence hardening ledger

- 2026-09-09: Confirmed that the checker accepted a fixture with arbitrary prompts, dependency consumers at baseline, and review artifacts added only to final integration.
- 2026-09-09: Red confirmed: `go test ./docs/workflows/checkrun` failed all five new regressions because plan drift, missing dependency ancestry, absent producer artifacts, incomplete review schema, and test-only hardening were accepted.
- 2026-09-09: Rebuilt the accepted fixture from the checked-in policy/features with real dependency merges and producer-owned review commits.
- 2026-09-09: Implemented full baseline-plan recompilation/equality, baseline and dependency ancestry checks, producer-commit artifact reads, required review fields, exact acceptance ownership, and harden production-plus-test ownership.
- 2026-09-09: Green confirmed with `go test -race ./docs/workflows/checkrun`; `go vet ./docs/workflows/checkrun` passed.
