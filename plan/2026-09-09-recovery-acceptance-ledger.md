# Incremental and recovery acceptance ledger

- 2026-09-09:09: Acceptance scope fixed at the eleven scenarios in `incremental.feature` and `resume.feature`.
- Fixtures use temporary Git repositories and the test binary as a portable deterministic task process.
- Red: `go test ./workflow` fails because `CompileIncremental`, `RecordImplementation`, `Resume`, and their tracking/history result fields do not yet exist on the shared baseline.
