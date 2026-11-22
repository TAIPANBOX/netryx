# Calm live figures, from the decision of 2026-11-22: the live numbers jumped
# too fast to read; they are to settle over a few minutes, so the apps,
# countries and clients that have been busy longer show the higher speed and
# can be sorted by it, and the rows are to hold their places while one reads.

Feature: the live figures settle, hold their order and sort as the owner likes

  Scenario: Now is an average over the last few minutes
    Given the router keeps the bytes of each minute for every device, app and country
    When the owner looks at the Now column of Clients, Apps, Destinations or a client's apps
    Then it shows the average speed over the window picked, three minutes unless another is picked
    And after a restart the average is taken only over the minutes since the restart
    # -> TestRecentSumsTheMinutesOfTheWindow
    # -> TestRecentCoversOnlyWhatTheStoreSaw
    # -> TestRecentReadsOnlyTheDimensionsKeysCarry
    # -> TestTheNowColumnsAreTheWindowsAverage

  Scenario: the window is one, three or ten minutes
    Given the pills 1 min, 3 min and 10 min beside the period
    When the owner picks one
    Then every Now column averages over it, and the choice is remembered
    # -> TestTheWindowIsOneThreeOrTenMinutes
    # (the pills: "the window pills are one, three and ten minutes" in web/src/steady.test.ts)

  Scenario: the live flows rank by what they moved over the window
    Given the live connection table
    When it is read live
    Then the flows that moved the most over the window come first, with those bytes, not the newest flows
    # -> TestLiveFlowsCanBeReadByRecentVolume
    # -> TestOldMinutesFallOutOfTheWindow
    # -> TestASlotReusedLaterHoldsNothingOld
    # -> TestAReusedKeyStartsItsWindowEmpty
    # -> TestLiveFlowsAreAskedByTheWindow

  Scenario: rows hold their places while the figures update
    Given a live table whose figures change on every poll
    When the order the sort wants changes
    Then the rows move at most every 30 seconds, and only when the difference is clear, gliding to their new place
    And a new row takes its place at once and a gone row leaves
    # (the order: "rows do not swap within 30 s", "a difference under the margin moves nothing",
    #  "a new row takes its place at once" in web/src/steady.test.ts)

  Scenario: any column sorts, and the choice is kept
    Given the tables of Clients, Apps, Destinations, Flows and a client's apps
    When the owner clicks a column heading
    Then the rows sort by it, a second click turns the order round, and the next visit starts the same way
    # (the sort: "rows sort by any column", "a click on the sorted column turns it round",
    #  "the sort is remembered" in web/src/steady.test.ts)

  Scenario: Pause holds the page still
    Given the owner wants to read the figures
    When they press Pause
    Then nothing on the page is asked again until they press it once more, except what they change while paused
    # (the pause: "Pause freezes every poll until it is lifted" in web/src/steady.test.ts and
    #  "a page that turns still between polls asks no more until it moves again" in web/src/poll.test.ts)
