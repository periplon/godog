# Self-host evidence hardening

## Objective

Close confirmed false-positive paths in the self-host evidence checker while the live workflow source remains frozen.

## Plan

1. Rebuild the accepted fixture as a real dependency DAG compiled from a committed baseline policy and feature snapshot.
2. Add failing regressions for plan drift, missing dependency ancestry, review artifact provenance and schema, and superficial implementation changes.
3. Recompile the immutable baseline inputs and compare the complete recorded plan.
4. Validate every dependency edge, read review artifacts from producer commits, enforce their required fields, and inspect owned implementation paths.
5. Run the focused checker tests and vet, then commit the evidence.

