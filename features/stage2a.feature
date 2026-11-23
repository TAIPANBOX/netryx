# The console, stage 2a, from the request of 2026-11-20: from the page the
# household can stop a country, an app or a category for one device, a group or
# everyone, always or on a schedule, pause it, give a little more time and cut
# a connection that is already running, and none of it is open to a stranger.
# The agent's own scenarios are in agent.feature.

Feature: the household decides what a device may reach, and the router enforces it

  Scenario: a country is blocked for everyone
    Given a rule that blocks a country for everyone
    When a device opens a connection to an address in that country, or one comes in from it
    Then the router drops it, by the device's side of the connection
    # -> TestACountryRuleForEveryoneCutsByCountryAndAnInboundFlowByItsDeviceSide

  Scenario: apps are blocked on one device on a schedule that crosses midnight
    Given a rule for one device with a window from the evening to the early morning
    When the clock passes the window's start and then midnight
    Then the whole window blocks, and the morning part belongs to the day it started
    # -> TestAWindowAcrossMidnightBelongsToTheDayItStarts

  Scenario: a schedule given only as days blocks whole days
    Given a rule with days and no hours
    When a listed day comes
    Then the device is blocked all that local day
    # -> TestDaysWithoutAWindowAreWholeLocalDays

  Scenario: a rule for one device never reaches anyone else
    Given a rule written for a single device
    When another device uses the same app
    Then the other device is not blocked
    # -> TestADeviceRuleNeverAppliesToEveryone

  Scenario: a rule holds when netryx stops and after a reboot
    Given rules that are applied and the address sets they learned
    When netryx is stopped or the router restarts
    Then the rules are still in the kernel, and the learned sets are loaded again at boot
    # -> TestTheScriptNeverDeletesTheTable
    # -> TestSetsAreSavedAndLoadedAtBoot

  Scenario: a live connection is cut when a rule starts
    Given a connection that is running and a rule whose window opens now
    When the window opens
    Then that connection, and only the device's own, is ended
    # -> TestAWindowOpeningCutsWhatItNowCovers
    # -> TestCutIsOnlyForTheDevicesOwnFlows

  Scenario: the router's own connections are never cut
    Given a rule that covers everyone
    When the router itself talks to a blocked place
    Then its connection is left alone
    # -> TestTheRoutersOwnHandshakeIsNeverCut

  Scenario: an allowance ends on its own
    Given a rule and a time-limited allowance for a device
    When the time passes
    Then the device is blocked again and the finished allowance is removed with the next write
    # -> TestAnAllowanceEndsOnItsOwn
    # -> TestAnEndedAllowanceIsRemovedWithTheNextWrite

  Scenario: an allowance lifts only the rule it names
    Given two rules for a device and an allowance for one of them
    When the device reaches what the other rule blocks
    Then it is still blocked
    # -> TestAnAllowanceLiftsOnlyTheRuleItNames

  Scenario: an allowance never lifts a country rule
    Given a country rule and an allowance naming it
    When the device reaches that country
    Then it is still blocked
    # -> TestOrderPutsCountriesBeforeAllowances

  Scenario: an allowance lasts at most four hours
    Given an allowance written for longer
    When the rules are read
    Then it is dropped
    # -> TestAnAllowanceLongerThanFourHoursIsDropped

  Scenario: a client over a link with no hardware address stays blocked
    Given a rule for everyone and a client behind WireGuard or another routed link
    When an allowance exists for the rule
    Then the client is still matched, because it has no ethernet address to be allowed by
    # -> TestAWhoAllRuleWithAnAllowanceStillMatchesWithoutAnEtherCondition

  Scenario: the apps' address sets learn only from the router's own resolver
    Given a device that sends a forged DNS answer or its own idea of a name
    When the collector hands answers to the engine
    Then only the answers the router's resolver gave are told, and the sets stay as they were
    # -> TestOnlyTheRoutersOwnAnswersAreTold
    # -> TestAnAnswerForAHouseOrSharedAddressFillsNothing

  Scenario: a failed apply keeps the previous rules
    Given rules in force and a new ruleset the kernel refuses
    When the reload is applied
    Then nothing changes in the kernel and the answer says so
    # -> TestReloadAppliesOneTransactionAndKeepsThePreviousOnFailure

  Scenario: a wrong rule is inactive with its reason and the others work
    Given a rule with a country that does not exist next to a good rule
    When the rules are read
    Then the wrong one is inactive and says why, and the good one is applied
    # -> TestABadRuleIsInactiveWithItsReasonAndTheRestApply

  Scenario: the owner's changes never rewrite the agent's sections
    Given a rule or device section the agent wrote
    When the owner saves from the console
    Then the agent's section keeps its name, group and every option
    # -> TestTheOwnersWritesNeverLandInAgentSections
    # -> TestAgentDeviceAndGroupSectionsAreIgnored

  Scenario: nothing is visible without the LuCI login
    Given a person with no session
    When they ask for any view except the login form
    Then the answer is 401
    # -> TestEveryViewNeedsALogin
    # -> TestOnlyTheLoginFormIsOpen
    # -> TestTheLoginFormsOwnChunksAreOpen
    # -> TestTheFirstScreensChunksAreReadFromBesideThePage
    # -> TestTheLoginFormLoadsWithoutASession

  Scenario: a change without the grant or the token is refused
    Given a session that may only view, or a write without the session's token
    When a write is sent
    Then it is refused and nothing reaches the config
    # -> TestAWriteNeedsTheGrantAndTheToken
    # -> TestAWriteRouteIsReachableOnlyThroughTheGate

  Scenario: the browser never holds the router's session
    Given a person who logged in
    When the cookie is read
    Then it is an opaque handle and not the rpcd session
    # -> TestTheCookieIsNotTheRPCSession

  Scenario: without rpcd the console only reads, and only from the router
    Given rpcd is not running
    When the console is asked from the router itself or from the house
    Then reads work from loopback and nothing can be changed, and the house gets no door
    # -> TestWithoutRPCLoopbackReadsAndNothingChanges
    # -> TestNoRPCKeepsTheConsoleOnLoopback

  Scenario: metrics leave the router only with the token
    Given a scrape from the house side
    When it carries no token
    Then it is refused, and the token is never on the service's command line
    # -> TestMetricsAreOpenOnLoopbackOnly
    # -> TestTheInitCommandLineIsParsedAsWritten

  Scenario: the console shows the router's time
    Given a router in a zone other than the browser's
    When the console shows a time
    Then it is the router's local time
    # -> TestTheSessionCarriesTheRoutersZone

  Scenario: a data file missing after a restart never opens a block
    Given rules applied with the address table and the app map
    When netryx starts again without one of them
    Then the kernel keeps every rule and set it had, and the rules that need the file say which one is missing
    # -> TestAMissingDataFileNeverOpensWhatWasBlocked
    # -> TestACountryRuleWithoutTheAddressTableSaysWhy

  Scenario: a connection that was cut does not come back
    Given an app rule and a device holding a keep-alive connection to that app
    When the rule cuts the connection and the device sends on it again
    Then the connection stays refused for the next ten minutes
    # -> TestACutKeepsTheDeviceFromThatAddressForTenMinutes
    # -> TestAChangedAppRuleEmptiesItsCutSet

  Scenario: a cut never blocks the rest of a shared front
    Given a rule cut one connection of a device to a front that serves other services too
    When the device opens a new connection to that front, or a device without a hardware address is cut
    Then the new connection takes the normal path, and the cut of the device without an address keeps the others' entries
    # -> TestACutPairIsTheRebornFlowOnly
    # -> TestACutEntryIsOneFlowByProtocolAndPort

  Scenario: a rule that only names can hold says so when names are not seen
    Given the capture or passive DNS is off, or offloading hides the connections
    When an app or category rule is shown
    Then it says it is not applied and what to turn on
    # -> TestAnAppRuleSaysItCannotSeeNamesWhenBlind
    # -> TestNamesAreSeenOnlyWithCaptureAndPassiveDNS

  Scenario: pauses that ended never use up the rules
    Given many pauses that have ended
    When a new rule is written
    Then the ended ones take no place and go with that write
    # -> TestRulesPastTheirEndDoNotCountTowardTheLimit
    # -> TestRulesPastTheirEndAreCleanedAndNotCounted

  Scenario: the household and the agent see what is let through
    Given an allowance for a device, from the console or from the agent
    When the rules are read
    Then the rule lists it with its end and who gave it
    # -> TestStateListsTheLiveAllowancesOfEachRule
    # -> TestListedAllowancesAreTheOnesInForce
    # -> TestRulesListTheirLiveAllowances

  Scenario: a change from the console waits while the agent's change awaits confirmation
    Given the agent applied a change to netryx that is not confirmed yet
    When the household saves from the console
    Then the console asks to wait a minute, and device names in labels.json are never erased
    # -> TestAPendingAgentApplyHoldsTheWrite
    # -> TestAWriteWaitsWhileTheAgentsApplyIsPending
    # -> TestADevicePutLeavesLabelsJsonAlone

  Scenario: a connection between two devices at home is never cut
    Given a rule that covers a device
    When it talks to another device in the house
    Then that connection is left alone
    # -> TestANamedHandshakeToAHomeAddressIsNotCut

  Scenario: a name with an apostrophe does not stop the rules
    Given a section whose value holds an apostrophe, written by uci
    When netryx reads the config
    Then it reads the value as uci does and every rule applies
    # -> TestParseReadsBackslashesAsLibuciDoes

  Scenario: an allowance lifts only what it names, and one that names nothing opens nothing
    Given allowances in the config for every rule, for one rule, and with no rule or an empty one
    When the rules are applied
    Then the one for every rule, written as '*', lifts every rule but a country's, and the one for a rule lifts that rule alone
    And the one with no rule or an empty one opens nothing and the Rules screen lists it with why
    # -> TestAnAllowanceForEveryRuleIsWrittenAsAStar
    # -> TestAnAllowanceWithoutAnExplicitRuleIsNotInForce
    # -> TestAnAllowNeedsAnExplicitRule
    # -> TestAnAllowanceLiftsOnlyTheRuleItNames
    # -> TestAllowancesThatOpenNothingAreListed
    # (the page: "allowances that open nothing are named with why, never trusted" in web/src/allows.test.ts)
