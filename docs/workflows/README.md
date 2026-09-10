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
concurrent file edits, not machine access. Linked worktrees also share Git refs
and repository configuration. Use workflow files and commands you trust. Codex must be installed and authenticated; deterministic tasks only need
the executables declared by the workflow.

## Generate a workflow from features

```sh
/tmp/godog workflow generate 'features/*.feature' \
  --output workflow.yaml --model gpt-5.6-sol
/tmp/godog workflow plan workflow.yaml --full
```

By default, `generate` writes a new workflow YAML without calling Codex or executing tasks.
It validates the feature files, sorts and deduplicates their paths, and creates
one implementation prompt task per feature in a serial dependency chain plus
a final review task. Task IDs are derived from paths. With unchanged inputs,
options and relative directory layout, generation produces the same bytes.
Generated implementation prompts request failing tests before implementation;
`--prompt 'Additional project instructions'` adds instructions to implementation
and review tasks.
`--model` is required and writes the selected model to the workflow default and
every generated implementation and review task. To change models afterward,
edit the task models, or remove them to inherit the workflow default.

Feature paths and quoted globs resolve relative to `--repo` (default `.`).
`--output` resolves relative to the current directory; its parent must exist and
an existing output is never overwritten. Generated feature references are
relative to the output file, so nested workflow directories work. No Git
repository or Codex installation is needed for deterministic generation or inspecting a full plan.

Review and edit the YAML before running it. Deterministic generation cannot infer dependencies
between features or the project's test commands. The conservative chain lets
each implementation build on earlier commits; adjust `needs` when features have
a known dependency order or can safely run independently. Add `run` tasks for
deterministic verification. Incremental planning may retain unchanged earlier
implementation tasks as prerequisites. Codex implementation
and review results are not deterministic; a successful review task does not
prove correctness.

### Plan with Codex

```sh
/tmp/godog workflow generate 'features/*.feature' --repo . \
  --output workflow.yaml --generator codex --model gpt-5.6-sol \
  --models gpt-5.6-luna,gpt-5.6-sol \
  --reasoning-effort high
/tmp/godog workflow plan workflow.yaml --full
```

Codex planning requires an installed, authenticated Codex CLI (`--codex` can
select its executable). `--model` selects the planner and the default task model;
`--models` supplies the task models it may choose from. Choose model identifiers
available to your account. Give model-specific complexity guidance with
`--prompt` if needed. Without `--models`, tasks use `--model`.

The planner inspects the selected requirements and repository in read-only mode,
then proposes task dependencies, a model, and reasoning effort for each task.
It is instructed to use parallel tasks only for isolated implementation work,
and to serialize shared code changes or uncertain dependencies. A final review
joins the implementation work. Generation validates the proposed plan before
publishing the YAML; it does not execute its tasks. Review its isolation
assumptions before running with `workflow run --jobs 2`.

`--reasoning-effort` sets the planner effort and default generated task effort.
Codex planning can adapt individual tasks to `low`, `medium`, `high`, or `xhigh`
according to complexity. The deterministic generator uses the supplied effort
uniformly. Codex planning defaults to `medium` when this option is omitted. Deterministic
generation without an explicit effort preserves the Codex configuration default. Model support for each effort depends on your provider.

The CLI uses Codex's structured-output and final-message file options; see the
[official non-interactive documentation](https://learn.chatgpt.com/docs/non-interactive-mode).

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
ready tasks use stable ordering. Recompiling unchanged inputs against the same implementation history produces
the same plan; model-generated implementations are not deterministic.

Every task declares exactly one action:

- `run`: a nonempty argv array, executed directly without shell interpolation.
- `prompt`: instructions for the initial and currently only agent adapter, Codex.
  `model` on the task overrides the workflow default. The effective command is
  `codex exec -m MODEL --dangerously-bypass-approvals-and-sandbox -- PROMPT`.
  An optional workflow or task `reasoning_effort` adds
  `-c 'model_reasoning_effort="high"'` (using the selected value). Task values
  override the workflow default; allowed values are `low`, `medium`, `high`, `xhigh`.

Task fields:

| Field | Meaning |
| --- | --- |
| `id` | Unique identifier, also used for worktree and artifact naming |
| `model` | Task model override; otherwise inherits workflow model |
| `reasoning_effort` | Task effort override; otherwise inherits workflow effort |
| `needs` | IDs that must succeed before this task starts |
| `features` | Optional globs selecting from the workflow's feature files; omitted selects all |
| `attempts` | Maximum attempts, 1 by default, at most 5 |
| `timeout` | Positive Go duration such as `30s` or `20m`; default 30 minutes |
| `expected_exit` | Expected command exit code, 0 by default; nonzero only for `run` tasks |

To make TDD stages explicit, an authoring task can create a regression, a command
with `expected_exit: 1` can verify that it fails, and a dependent implementation
task can fix it before a final command expects exit 0. Choose a focused test so
an unrelated failure cannot accidentally satisfy the red gate.

## Incremental implementation

`plan` and `run` use repository implementation history by default. Use `--repo DIR`
for the target checkout and `--full` to select every scenario regardless of history.
`plan --full` is stateless; a successful `run --full` refreshes implementation
tracking. The library's `Compile` API remains a stateless full compiler; `CompileIncremental`
adds repository-aware selection.

Successful executions record scenario semantics and their workflow policy in the
repository's common Git directory. A future plan skips matching implementations
only when their integration commit is an ancestor of the selected source HEAD.
Review and merge the result before expecting it to be reused. Failed workflows
cannot mark scenarios implemented, even when some tasks succeeded.

Adding a scenario does not invalidate unchanged siblings merely by moving their
line numbers. Changed steps, inherited context, or policy invalidate the affected
records. Necessary prerequisites remain in the task graph; implementation prompts
identify the changed scenario targets. When nothing changed, planning and running
produce a no-op result. Removing a scenario does not automatically delete code.

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
is rejected for a new run. To retry unfinished steps from its saved plan, use
`godog workflow resume /absolute/run/directory --jobs 2`. Completed task commits
are validated and reused; an explicit resume grants unfinished tasks another
bounded attempt budget and retains cumulative counts and earlier artifacts.
Resume uses the immutable saved plan and requires the original clean source
baseline and a supported saved-plan schema. Runs created before durable
checkpoints were introduced cannot be resumed automatically. It retains completed-task checkpoints and a history of execution
starts. Unfinished steps restart from their successful dependencies in fresh
worktrees; earlier partial edits remain available in the original worktrees.
A command interrupted before its success checkpoint may execute again, so
external side effects should be idempotent.

Linux, macOS, BSD and Windows use inherited operating-system locks to prevent
a second runner from restarting while an earlier task still holds the lock.
Other platforms use a lock file; after abrupt termination, stop the old processes
before removing a stale lock.

There is no automatic cleanup. Inspect `git worktree list` and remove retained worktrees with Git
when they are no longer needed.

Process-group cancellation is implemented on Unix platforms, including macOS and
Linux. Windows uses `taskkill /T /F` to terminate a process tree. Other platforms
fall back to terminating the immediate process.

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

For the initial seven-task development qualification, validate its evidence
independently of the running workflow:

```sh
go run ./docs/workflows/checkrun /absolute/path/to/run-directory
```

This checker is specific to that qualification policy, not a general run validator.
It checks recorded tasks, commits, worktrees, overlapping implementation work,
review artifacts and unresolved findings. It complements test results; it does
not prove that arbitrary model-generated code is correct.
