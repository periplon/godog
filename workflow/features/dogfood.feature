Feature: Self-hosted workflow development
  The extension's own requirements and workflow policy must exercise the product
  with real Codex implementation work after the minimal bootstrap is available.

  Scenario: Generate and execute the extension's development workflow
    Given the extension's Gherkin requirements and development workflow DSL
    When the new Godog compiles and executes that workflow
    Then real Codex tasks add executable acceptance coverage and implementation improvements
    And independent tasks use isolated Git worktrees
    And deterministic verification checks their integrated results
    And the run records actual commits, attempts, logs and outcomes
