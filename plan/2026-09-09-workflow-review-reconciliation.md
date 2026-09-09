# Workflow review reconciliation plan

1. [x] Preserve and inventory both independent review files and map every finding ID.
2. [x] Add one regression per actionable finding and capture the expected red result.
3. [x] Apply the minimum implementation fix and run the targeted test after each change.
4. [x] Run broader workflow tests and build the CLI.
5. [x] Test the built CLI as a subprocess for valid JSON output and nonzero failure exit.
6. [x] Write `.workflow-review/resolution.json` with a concrete disposition and evidence for every finding.
7. [x] Review the final diff, repository status, and verification evidence without committing.
