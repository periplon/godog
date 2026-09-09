Feature: Deterministic implementation workflow generation
  Gherkin describes required outcomes while a versioned workflow policy supplies
  task boundaries, dependency ordering, deterministic commands, and Codex prompts.

  Scenario: Reproducible generation
    Given a workflow policy and Gherkin feature files
    When the workflow is compiled twice
    Then both plans are byte identical
    And tasks appear in stable dependency order
    And every selected scenario is assigned to a task

  Scenario: Expand the full Gherkin semantics
    Given features containing backgrounds, rules, outlines, tables and doc strings
    When the workflow is compiled
    Then every example row becomes an individual scenario
    And task context preserves background steps and multiline arguments

  Scenario Outline: Reject invalid policies before execution
    Given a workflow policy with <defect>
    When the workflow is compiled
    Then compilation fails without executing any command
    Examples:
      | defect                   |
      | an unsupported version   |
      | an unknown field         |
      | a dependency cycle       |
      | an unknown dependency    |
      | duplicate task IDs       |
      | no matching features     |
      | an uncovered scenario    |
      | both run and prompt      |
      | a missing Codex model    |
      | an invalid attempt limit |
