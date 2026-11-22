# Seeing more about a thing from wherever it is named, from the decision of
# 2026-11-22: each row says how its last hour went, any thing can be found by
# typing its name, a name shows a short card before it is opened, and a thing's
# view names what it belongs to and what belongs to it.

Feature: every thing in the console can be found and seen in place

  Scenario: each row of Clients and Apps shows its last hour
    Given the client list or the apps list
    When the owner looks at a row
    Then it shows a small line of that row's traffic over the last hour
    And the minutes the router had not counted yet are left blank, not drawn as quiet
    # -> TestSparksAreEachKeysMinutesInSteps
    # -> TestAnEmptyStoreKnowsNoStep
    # -> TestTheListsCarryAnHourOfSparkline
    # (the line: "a list's hour is drawn only from where the router began counting" in web/src/live.test.ts)
