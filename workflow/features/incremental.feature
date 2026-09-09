Feature: Incremental implementation workflows
  Successful implementation records identify the scenario semantics and policy that were implemented.
  A record is reusable only when its implementation commit belongs to the source history.

  Scenario: Skip unchanged implemented scenarios
    Given a successful workflow whose integration commit is in the source history
    When the same features and policy are planned again
    Then no implementation tasks are generated

  Scenario: Implement only a newly added scenario
    Given implemented scenarios in a feature
    When a new scenario is inserted before them
    Then only the new scenario is an implementation target
    And necessary prerequisite and verification tasks remain

  Scenario: Reimplement changed behavior
    Given implemented scenarios in a feature
    When one scenario's steps change
    Then only that scenario is an implementation target

  Scenario: Invalidate shared context and policy changes
    Given implemented scenarios in a feature
    When their background or workflow policy changes
    Then the affected scenarios are implementation targets again

  Scenario: Unmerged implementation is not skipped
    Given a successful workflow whose integration commit is absent from source history
    When the features are planned again
    Then its scenarios remain implementation targets

  Scenario: Failed execution cannot mark scenarios implemented
    Given an execution with an unsuccessful verification task
    When the features are planned again
    Then its scenarios remain implementation targets
