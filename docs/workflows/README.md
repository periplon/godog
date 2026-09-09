# Implementation workflows

Build the extended Godog CLI, inspect the plan, then execute it in a clean Git repository:

```sh
go build -o /tmp/godog ./cmd/godog
/tmp/godog workflow plan docs/workflows/develop.yaml
/tmp/godog workflow run docs/workflows/develop.yaml --repo . --jobs 2
```

`plan` is read-only and prints deterministic JSON. `run` prints a JSON result,
returns nonzero on failure, and preserves task logs and Git worktrees. The result
identifies the integration worktree and commit; review that checkout before
merging it into your branch. The source checkout is left at its original commit.

Codex tasks execute with **approvals and sandbox disabled**. Git worktrees isolate
concurrent file edits, not machine access. Use workflow files and commands you
trust. Codex must be installed and authenticated; deterministic tasks only need
the executables declared by the workflow.

## Workflow DSL version 1

```yaml
version: 1
name: implement-invitations
features: [features/invitations.feature]
model: gpt-5.6-sol
tasks:
  - id: implement
    prompt: |
      Implement the supplied scenarios. Write a failing regression first,
      confirm the expected failure, then implement and run the tests.
    attempts: 2
    timeout: 20m
  - id: verify
    needs: [implement]
    run: [go, test, ./...]
    timeout: 5m
```

Feature globs resolve relative to the YAML file. The compiler parses Gherkin,
expands scenario outlines and backgrounds, includes multiline step arguments,
and attaches selected scenario context to tasks. It rejects malformed features,
unknown policy fields, invalid dependencies and uncovered scenarios. Files and
ready tasks use stable ordering. Recompiling unchanged inputs produces the same
plan; model-generated implementations are not deterministic.

Every task declares exactly one action:

- `run`: a nonempty argv array, executed directly without shell interpolation.
- `prompt`: instructions for the initial and currently only agent adapter, Codex.
  `model` on the task overrides the workflow default. The effective command is
  `codex exec -m MODEL --dangerously-bypass-approvals-and-sandbox PROMPT`.

Task fields:

| Field | Meaning |
| --- | --- |
| `id` | Unique identifier, also used for worktree and artifact naming |
| `needs` | IDs that must succeed before this task starts |
| `features` | Optional globs selecting from the workflow's feature files; omitted selects all |
| `attempts` | Maximum attempts, 1 by default, at most 5 |
| `timeout` | Positive Go duration such as `30s` or `20m`; default 30 minutes |
| `expected_exit` | Expected command exit code, 0 by default; nonzero only for `run` tasks |

To make TDD stages explicit, an authoring task can create a regression, a command
with `expected_exit: 1` can verify that it fails, and a dependent implementation
task can fix it before a final command expects exit 0. Choose a focused test so
an unrelated failure cannot accidentally satisfy the red gate.

## Execution and failure

The runner captures a clean repository baseline. Each task gets its own detached
Git worktree with successful dependency commits integrated in stable order.
Independent tasks can run concurrently, bounded by `--jobs`. Successful changes
are committed and a final integration worktree combines the results. Conflicting
edits cause an explicit failure and leave the conflict available for inspection.
The runner does not automatically resolve conflicts or publish changes.

A failed task blocks its descendants. Retry limits are explicit; retries reuse
the task worktree, and Codex receives bounded previous failure output as feedback.
The reported attempt count includes unsuccessful attempts. A zero Codex exit code
means the process succeeded; add deterministic verification tasks to establish
that the implementation satisfies its requirements.

Use `--output-dir /absolute/new/directory` to select a new artifact directory, or
let the runner allocate a temporary directory. Reusing an existing run directory
is rejected. Runs preserve evidence and worktrees; there is no automatic resume
or cleanup. Inspect `git worktree list` and remove retained worktrees with Git
when they are no longer needed.

`--repo` defaults to the current directory. `--codex` selects the executable for
the Codex adapter. Interrupting a run cancels its tasks and returns failure.

## Developing this extension with itself

[develop.yaml](develop.yaml) consumes the requirements in
[workflow/features](../../workflow/features). Once the minimal compiler and runner
are built, independent Codex tasks extend executable acceptance coverage and
harden execution, then a deterministic task tests their integrated changes.

The bootstrap is written before the runner exists. Actual self-hosted run evidence
and review findings are tracked in the dated files under [plan](../../plan).
Ordinary CI uses fake Codex executables for repeatable adapter tests and does not
require a paid model session. The explicit development workflow uses real Codex.

After a real self-hosted run, validate its evidence independently of the running
workflow:

```sh
go run ./docs/workflows/checkrun /absolute/path/to/run-directory
```

This checks recorded tasks, commits, worktrees, overlapping implementation work,
review artifacts and unresolved findings. It complements test results; it does
not prove that arbitrary model-generated code is correct.
