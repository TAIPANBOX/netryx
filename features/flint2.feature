# The console on a real router, the Flint 2 stage, from the request of
# 2026-11-21: the console runs on a GL-MT6000 where Hermes's test bot is
# already configured, so nothing may reach Telegram unless the owner chose it.

Feature: the console behaves on a real router

  Scenario: alerts wait for the owner's settings
    Given an unsent alert and a router where Hermes's Telegram bot is configured
    When the delivery round runs before the alert settings are read
    Then nothing is sent and the alert stays unsent
    And on the console channel still nothing is sent, and on the telegram channel one send
    # -> TestNoAlertIsSentBeforeTheSettingsAreRead

  Scenario: the upload check waits for the owner's settings too
    Given a device whose last hour of upload is unusual
    When the hourly check runs before the alert settings are read
    Then no upload alert is raised, and one is raised once they are read with uploads on
    # -> TestNoUploadAlertIsRaisedBeforeTheSettingsAreRead
    # -> TestTheAlertSettingsAreInForceOnlyOnceRead

  Scenario: a connection cut from the console stays cut
    Given a device's live connection on the console, and the cut button pressed
    When the device's next packet would take the connection up again on the same port
    Then that flow is refused for ten minutes, and a new connection of the device to the same address is answered
    And a device without a hardware address is only cut, nothing is added for it
    # -> TestAButtonCutStaysCut
    # -> TestTheCutButtonKeepsTheFlowCut

  Scenario: no allowance lets a connection the owner cut back in
    Given a device with an allowance, and a connection of it cut from the console
    When the device's next packet of that connection reaches the router
    Then it is refused before any allowance is looked at
    # -> TestTheOwnersCutsComeBeforeEveryAllowance

  Scenario: a cut the router could not hold says so
    Given the router cannot keep a cut connection from coming back (its set is full, or nft refuses it)
    When the cut button is pressed
    Then the connection is still ended, and the page says it may come back and why, instead of a plain "Cut."
    # -> TestACutThatIsNotHeldSaysSo
    # -> TestACutAnswerSaysWhetherItIsHeld

  Scenario: turning flow offloading on under a running console is noticed
    Given netryx in forward mode, started with flow offloading off, and an app rule held only by names
    When fw4 is reloaded with flow offloading on
    Then within one engine tick the rule says it cannot see names, and it is applied again once offloading is off
    # -> TestTheEngineLooksAgainWhetherNamesCanBeSeen
    # -> TestTheOffloadSettingIsReadAgain

  Scenario: a rule that cannot see names says why
    Given an app rule held only by names, and capture and passive_dns both on
    When netryx runs in forward mode under flow offloading
    Then the rule says forward mode under flow offloading is the cause, and to set mode to ingress or turn offloading off
    And with capture or passive_dns off it names that option instead, and every cause when there are several
    # -> TestTheBlindReasonNamesItsCause
    # -> TestAnAppRuleSaysItCannotSeeNamesWhenBlind

  Scenario: a new country rule cuts a download that has only just started
    Given a device's download from a blocked country, started a few seconds ago and not named yet
    When a rule blocking that country for the device is written
    Then the download is cut at the write, not at the engine's next tick
    And a young flow to another country, and one with both ends at home, are left alone
    # -> TestACountryRuleCutsAFlowNotYetPlacedAtTheWrite

  Scenario: the console finds the bot where Hermes installs its settings
    Given Hermes installed from its package, with its Telegram bot configured in /etc/config/hermes
    When netryx looks for the bot to send an alert
    Then it reads that file and finds the bot, and the console no longer says Hermes is not installed
    # -> TestTheBotIsReadFromTheFileThePackageInstalls

  Scenario: the console answers to the router's own host name
    Given a router named flint2 whose local domain is lan
    When the console is asked for with Host flint2, flint2.lan or FLINT2.LAN:8788
    Then it answers, and evil.lan, flint2.evil.com and any other name still get 421
    # -> TestTheRoutersOwnNameReachesTheConsole
