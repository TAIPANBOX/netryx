# Sessions that outlive a restart, from the decision of 2026-11-22: an upgrade
# or a restart of netryx should not sign the household out while the router's
# own sessions are still valid; what is kept stays in memory and private.

Feature: a restart of netryx signs nobody out

  Scenario: the console stays signed in across a restart
    Given the owner is signed in to the console
    When netryx restarts, for an upgrade or by hand
    Then the open page stays signed in and its changes still go through, as long as the router still knows the session
    # -> TestASessionSurvivesARestart

  Scenario: what is kept is private and never half written
    Given netryx keeps its sessions across a restart
    When it writes them
    Then they are in a file only root can read, in a directory only root can enter, in memory that a reboot clears, and the file is replaced whole
    # -> TestTheSessionsFileIsPrivateAndWrittenWhole

  Scenario: a file that could have been tampered with signs nobody in
    Given the kept sessions are open to others, owned by someone else, a link or a pipe, damaged or in a directory others can write
    When netryx starts
    Then it ignores them and says why in the log, and everyone signs in again
    # -> TestASessionsFileOthersCouldTouchIsNotLoaded
    # -> TestOnlyWellFormedSessionsAreLoaded

  Scenario: an ended session is not kept
    Given a session that was signed out, or that the router has let expire
    When netryx restarts
    Then that session does not come back, even when the kept table could not be written
    # -> TestEndedSessionsLeaveTheFile
    # -> TestAFailedWriteLeavesNoStaleSessions
    # -> TestAFailedWriteRemovesNothingOutsideItsOwnDirectory
    # -> TestAFailedWriteRemovesNothingInADirectoryOthersCanWrite
