# The daily file: one small file per closed UTC day, so the console can compare
# a device with its own last month (the week grid of medians, rhythm and drift,
# interventions, the backup line's volume). This is the store only; the screens
# come later.

Feature: every closed day is kept for a month in one small file

  Scenario: a day is written once, after it has closed, and never again
    Given the router counted a device through a day
    When the first history tick after a minute past midnight UTC runs
    Then that day's file is written beside the history, and a later tick or a changed ring never writes it again
    # -> TestADayIsWrittenOnceWhenItClosesAndNeverRewritten
    # -> TestTheDailyFileIsWrittenByTheFirstTickAfterTheDayCloses

  Scenario: a restart in the middle of the day loses none of its hours
    Given the service was stopped mid-morning and started again a few minutes later
    When the day closes
    Then its file holds every hour, the restarted hour with what was counted before and after the restart
    # -> TestADayWrittenAfterAMidDayRestartHoldsEveryHourTheRingHolds

  Scenario: a router switched off is never read as a quiet house
    Given the router was off for part of a day
    When the day is read back
    Then the hours it was off are outside the day's hours mask and come out unknown, a quiet hour it counted comes out as zero, and a day with no file is unknown
    # -> TestHoursTheRouterWasOffAreNotInTheMask
    # -> TestADayIsAnsweredOnlyOnceClosedAndWholeInTheRing

  Scenario: one bad file never costs the others
    Given a day's file damaged on flash, cut short, of another version or under another day's name
    When netryx starts
    Then that file is skipped and named in the log, its day reads as unknown, it is never written over, and every other day loads
    # -> TestADamagedFileIsSkippedAndNamedAndTheRestLoad
    # -> TestAFileThatCannotBeADayIsRefused

  Scenario: a month is kept, and no more
    Given more than thirty days of files
    When a day is written
    Then the files of days more than thirty days back are deleted, and a write a power cut stopped goes with them
    # -> TestFilesPastThirtyDaysAreDeleted

  Scenario: the counters at the close come back as written
    Given every rule's hit counter and every interface's byte counters at the day's close
    When the day's file is read back
    Then they are the same numbers, and only the day that has just closed carries them
    # -> TestRuleHitsAndInterfaceCountersRoundTrip
    # -> TestTheHitsAreEveryRulesCounterByID

  Scenario: a busy hour does not wrap
    Given a device that moved more than 4 GiB in one hour
    When the day is read back
    Then the hour reads as what it moved, and an hour past what the file can hold reads at its top, never as a small number
    # -> TestAnHourOverFourGiBDoesNotWrap

  Scenario: only devices are kept, and a flood of them is bounded
    Given the router's own traffic, the catch-all and more than 512 devices in one day
    When the day's file is made
    Then only hardware addresses are kept, the busiest 512, and how many were left out is said
    # -> TestADayKeepsOnlyHardwareAddressesAndAtMostTheBusiest512

  Scenario: a device's month is one read
    Given a logged-in person on the console
    When the page asks for a device's days
    Then it gets the last thirty days oldest first, in bytes, with unknown days as unknown and hours the router was off as null; an unknown device is a 404 and a malformed address a 400
    # -> TestADeviceAnswersItsDaysWithUnknownDaysAsUnknown
    # -> TestEveryViewNeedsALogin

  Scenario: the file fits a small router
    Given 64 busy devices for 31 days
    When the days are written and the thirty kept are loaded
    Then one day's file stays under 16 KB and the thirty days under 1 MB of heap
    # -> TestDailyProfile
