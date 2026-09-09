Feature: Workflow command line
  Godog keeps its existing test commands and adds implementation workflow commands.

  Scenario: Plan without executing
    Given a valid workflow specification
    When I run godog workflow plan with the specification
    Then standard output contains a machine-readable deterministic plan
    And no task process is started

  Scenario: Execute a workflow
    Given a valid workflow specification and a clean Git repository
    When I run godog workflow run with the specification
    Then standard output contains the workflow result
    And task failures cause a nonzero exit status

  Scenario: Reject incorrect command arguments
    When I run a workflow command without its specification
    Then the command reports a usage error
