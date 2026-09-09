Feature: Resume recorded workflow executions
  Execution records preserve the immutable plan and completed task commits.
  Restarting an execution retries unfinished work without repeating completed steps.

  Scenario: Resume after a task failure
    Given an execution with a successful prerequisite and a failed dependent task
    When that execution is resumed
    Then the successful prerequisite is not executed again
    And the failed task and its blocked dependents are retried
    And attempt history is preserved

  Scenario: Resume after interruption
    Given a stopped execution with completed and unfinished steps
    When that execution is resumed
    Then completed steps are reused from their durable checkpoints
    And only unfinished steps execute

  Scenario: Refuse a concurrent restart
    Given an execution that is still running
    When a second runner attempts to resume it
    Then the second runner is rejected without modifying execution state

  Scenario: Validate the saved execution
    Given an execution whose source baseline or saved artifacts have changed
    When that execution is resumed
    Then recovery fails before any task is executed

  Scenario: Resume a completed execution
    Given a fully successful execution
    When that execution is resumed
    Then no task is executed again
    And the recorded integration result is returned
