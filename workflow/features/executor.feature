Feature: Isolated implementation workflow execution
  A workflow uses deterministic commands where possible and Codex only for
  explicitly declared judgment tasks. Parallel tasks use distinct Git worktrees.

  Scenario: Parallel execution and dependency integration
    Given two independent implementation tasks and a dependent verification task
    When the workflow runs with two workers
    Then the independent tasks execute in distinct Git worktrees
    And verification sees both successful implementation commits
    And the original checkout remains unchanged
    And the result identifies an integration worktree and commit

  Scenario: Invoke the initial Codex adapter
    Given a Codex task with a selected model and prompt
    When the workflow runs
    Then Codex executes noninteractively with that model and YOLO mode
    And the prompt includes the task's Gherkin scenarios
    And shell metacharacters in the prompt remain literal arguments

  Scenario: Failure blocks dependent work
    Given an implementation task that fails
    When the workflow runs
    Then dependent tasks are blocked
    And the workflow returns failure
    And task logs and worktrees remain available for inspection

  Scenario: Bounded retries use executable feedback
    Given a Codex task that fails once and allows two attempts
    When the workflow runs
    Then the second attempt receives the first failure output
    And the result reports two attempts

  Scenario: Integration conflict
    Given independent tasks that make conflicting edits
    When the workflow integrates their results
    Then the workflow reports an integration failure
    And the conflicting worktree remains available for inspection

  Scenario: Cancellation
    Given a running task
    When the workflow context is cancelled
    Then execution stops and returns failure
    And no unfinished task is reported as successful

  Scenario: Verify the red phase of test-driven development
    Given a command task expecting exit code 1
    When that command exits with code 1
    Then the task succeeds and its implementation successor may run

  Scenario: Enforce a task timeout
    Given a task with a finite timeout
    When its command exceeds the timeout
    Then the task fails and its descendants are blocked
    And cancellation is never accepted as an expected command exit
