# Incremental and recovery acceptance ledger

- 2026-09-09:09: Acceptance scope fixed at the eleven scenarios in `incremental.feature` and `resume.feature`.
- Fixtures use temporary Git repositories and the test binary as a portable deterministic task process.
- Red: `go test ./workflow` fails because `CompileIncremental`, `RecordImplementation`, `Resume`, and their tracking/history result fields do not yet exist on the shared baseline.
- Portability review found that embedding the test executable inside YAML double quotes corrupts Windows backslashes. The fixture now serializes command argv as JSON-compatible YAML and disables inherited Git commit signing.
- The unmerged-record fixture creates an integration commit with `git commit-tree` while source HEAD remains at the tracked baseline, matching the explicit baseline-bound recording contract.
