# Opening the console from LuCI and over https, from the decision of
# 2026-11-22: LuCI stays the only sign-in (no password or second factor of the
# console's own), a user already signed in to LuCI opens the console with no
# second login, and the console on the house network is served over https.

Feature: the console is opened from LuCI without a second login, and over https

  Scenario: a user signed in to LuCI opens the console from its menu
    Given a user signed in to LuCI whose account may view netryx
    When the user opens Network, then Network console
    Then LuCI's page gets a one-time code for that user's own LuCI session
    And the console exchanges it for a session of its own, with no login form
    And the user may change rules only if the account may write
    # -> TestASignedInLuCIUserOpensTheConsoleWithACode
    # -> TestAHandedOffViewerCannotWrite
    # -> TestAHandoffIsMintedForTheCallersOwnSession
    # -> TestTheLuCIObjectOffersTheHandoffOnly

  Scenario: a code opens one session, for 30 seconds, and leaves no trace
    Given a code LuCI's page received
    When it is used a second time, or 30 seconds after it was made, or with the clock set back
    Then it opens nothing
    And only a hash of it is kept, in a private file in RAM, and it is never logged
    And it is taken only as JSON in the body of a POST, never from a query or a form
    And the console takes it out of the address and the history as soon as the page starts
    # -> TestACodeIsGoodOnce
    # -> TestEveryCodeIsItsOwn
    # -> TestACodeOpensOneSessionOnly
    # -> TestACodeExpiresAfterThirtySeconds
    # -> TestAnExpiredCodeOpensNothing
    # -> TestACodeFromTheFutureIsNoCode
    # -> TestTheCodeIsNeverWrittenDown
    # -> TestTheCodeIsNeverLoggedAndOnlyTakenAsJSON
    # -> TestAPendingFileOthersCouldTouchIsNoCode
    # (the page: "a code from LuCI is read from the address fragment only, and only in its exact shape" in web/src/api.test.ts)

  Scenario: a code opens only in the browser that asked for it
    Given LuCI's page left a random value in the browser's cookie before it asked for the code
    When the code is brought by another browser, without that value or with another
    Then it opens nothing, and it is spent
    # -> TestACodeOpensOnlyInTheBrowserThatAskedForIt
    # -> TestAMintNeedsTheBrowsersNonce
    # -> TestACodeSentToAnotherBrowserOpensNothing

  Scenario: a code for the wrong session opens nothing
    Given a code made for a LuCI session that has since ended, that rpcd never issued, or whose user may not view netryx
    When it is brought to the console
    Then the console refuses it and opens no session
    And a page cannot ask for a code for any session but its own
    # -> TestACodeForADeadOrForeignSessionOpensNothing
    # -> TestACodeForAUserWithoutTheViewGrantOpensNothing
    # -> TestAHandoffWithoutAGoodSessionMintsNothing
    # -> TestAMintRefusesWhatIsNotASessionOrAUser
    # -> TestAMintLeavesAnotherMintsFileAlone

  Scenario: guessing codes is limited and bounded
    Given someone sending made-up codes
    Then they are limited like password guesses
    And the codes waiting to be used are at most 32, the expired ones cleared
    # -> TestHandoffRedeemsAreRateLimited
    # -> TestPendingCodesAreBounded

  Scenario: leaving the console keeps the user signed in to LuCI
    Given a console session opened from LuCI
    When the user signs out of the console, even after netryx restarted
    Then the console session ends and the LuCI session goes on
    # -> TestLeavingTheConsoleKeepsTheLuCISession
    # -> TestALentSessionStaysLentAcrossARestart

  Scenario: a console nobody uses signs itself out
    Given a console session, from LuCI or from the login form
    When nobody moves the mouse, types or brings the tab into focus for LuCI's session time, an hour by default
    Then the session ends, even though the open tab kept polling the router
    And a session that came from LuCI is let go, so LuCI's own timeout ends it, while netryx's own is ended
    And coming back after that asks for a sign-in, saying why
    # -> TestPollingAloneDoesNotKeepASessionAlive
    # -> TestActivityKeepsTheSessionAlive
    # -> TestActivityAfterTheLimitDoesNotReviveTheSession
    # -> TestAWriteIsActivity
    # -> TestAChangeWithoutTheTokenIsNotActivity
    # -> TestAnIdleLentSessionIsLetGoNotEnded
    # -> TestIdleTimeSurvivesARestart
    # (the page: "each request says how long ago a person last used the page, in whole seconds" in web/src/api.test.ts)

  Scenario: LuCI's page says when the console cannot be opened from the house
    Given the console listens on the router itself only, or is not running
    When the user opens Network console
    Then the page says so and how to reach it through an SSH tunnel, and no code is made
    # -> TestAHandoffSaysWhenTheConsoleIsNotReachable

  Scenario: on the house network the console speaks https
    Given the console listens on a house-side address
    Then it serves https with uhttpd's certificate when the router has one
    And otherwise with a self-signed ECDSA P-256 certificate of its own, made once and kept, its key readable by root only
    And on loopback, where an SSH tunnel arrives, it stays plain http
    # -> TestWithoutUhttpdsPairTheConsoleMakesItsOwn
    # -> TestUhttpdsPairIsPreferred
    # -> TestTheLoopbackTunnelStaysPlain

  Scenario: plain http to the https port is sent to https
    Given the console speaks https on a port
    When plain http arrives on that port from the house
    Then it is sent to https on the same port, for one of the router's own names only
    And with the redirect turned off it is refused with a line saying to use https
    And plain http from the router itself is served, so the SSH tunnel keeps working
    And a connection that says nothing is let go
    And running out of connections pauses the port, never closes it
    # -> TestPlainHTTPOnTheHTTPSPortIsSentToHTTPS
    # -> TestASilentConnectionIsLetGo
    # -> TestRunningOutOfDescriptorsDoesNotCloseThePort
    # -> TestTheInitCommandLineIsParsedAsWritten

  Scenario: a console listening on every address still serves the house only
    Given the console listens on 0.0.0.0 or ::, which takes in the WAN address too
    When a request arrives on an address of the router outside the house, whatever Host it names
    Then it is refused, and the log says at start that the LAN address is the better listen
    # -> TestARequestArrivingOnTheWANAddressIsRefused
