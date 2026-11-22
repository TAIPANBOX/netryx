# The console, stage 2b, from the request of 2026-11-20: the house's DNS goes
# through the router and its bypasses can be closed, a group of devices gets
# safe search and YouTube's restricted mode, adblock lists are switched from
# the console, and an app can be given so many minutes a day per device.

Feature: the router keeps the house's DNS honest and counts the time an app may take

  Scenario: all of the house's DNS goes through the router
    Given forced DNS is on
    When a device asks any DNS server outside the router, or the router itself
    Then the router's resolver answers it, and the router's own lookups are left alone
    # -> TestForcedDNSRedirectsTheHouseAndSparesTheRouter

  Scenario: DNS over TLS is refused before any exception
    Given blocking DoT is on and a device has a live allowance
    When the device opens port 853
    Then it is refused, the allowance notwithstanding
    # -> TestDoTIsRejectedAsASecurityRule

  Scenario: DNS over HTTPS and VPNs can be blocked like apps
    Given a rule for the DoH category and one for the VPN category
    When a device reaches a known DoH resolver by name or by address, or a VPN's default port
    Then the rule stops it and counts it
    # -> TestVPNPortsAndDoHAddressesShareTheRulesCounter
    # -> TestBlockDoHIsAnImplicitRule
    # -> TestCategoriesIncludeDohAndVpn

  Scenario: a filtered group gets safe search and YouTube's restricted mode
    Given a group with the DNS filter and safe search on
    When one of its devices asks for Google, Bing or YouTube
    Then the router's second resolver answers with the safe hosts, and nothing else of the house's DNS changes
    # -> TestFilteredMembersGoToTheSafeResolverOnlyWhileItAnswers
    # -> TestSafeDNSWritesOnlyItsOwnSectionAndRestartsOnlyItsInstance

  Scenario: a failing filter never leaves the group without DNS
    Given the safe resolver does not answer, or has no safe addresses
    When a device of a filtered group asks a name
    Then it keeps the main resolver and the console says why the filter is down
    # -> TestTheFilteredResolverIsUsedOnlyWhileHealthy
    # -> TestAnUnhealthyResolverSaysWhy
    # -> TestWithoutSafeAddressesTheResolverIsNotHealthy

  Scenario: the router's own DNS settings and other people's pending changes are not touched
    Given another tool keeps uncommitted changes in the DHCP config
    When netryx writes its resolver's section
    Then it commits nothing, so those changes stay as they were and nothing is written to flash
    # -> TestPendingDHCPChangesAreNotCommittedForSomeoneElse

  Scenario: a reboot never leaves a filtered group without DNS
    Given the safe resolver is forgotten by a reboot
    When the router loads its rules at boot, before netryx starts
    Then filtered devices use the main resolver until netryx has the safe one answering, and a dead one is started again
    # -> TestTheBootRulesNeverSendToTheSafeResolver
    # -> TestADeadInstanceIsRestarted

  Scenario: what the safe resolver answers still names the traffic
    Given a filtered device resolves through the safe resolver
    When the answer leaves the router
    Then the console reads it like any answer of the router, and a device cannot forge one
    # -> TestTheSafeResolversAnswersAreTold

  Scenario: adblock lists are switched from the console
    Given adblock-fast is installed
    When the owner switches one of its lists
    Then only that list changes, and the console says adblock-fast restarts the router's DNS
    # -> TestAdblockSourcesAreReadAndSwitchedByPosition
    # -> TestAdblockWritesOnlyEnabledOnASource

  Scenario: an app limit counts minutes and blocks at the limit
    Given a device may use an app for so many minutes a day
    When the app moves at least 64 KB for it in each of those minutes
    Then at the limit that app is blocked for that device and its running connections are cut
    # -> TestAMinuteCountsAtSixtyFourKilobytes
    # -> TestALimitBlocksAtItsMinutesAndCutsThatAppOnly

  Scenario: a limit restarts at the router's midnight
    Given a device spent its minutes
    When local midnight passes, on a day of 23 or 25 hours too
    Then the count is back to zero and the block ends
    # -> TestUsageRestartsAtLocalMidnight
    # -> TestALimitBlocksOnlyTheSpentMembersUntilMidnight

  Scenario: a little more time lifts a spent limit
    Given a device spent its minutes
    When the owner gives it more time
    Then it is let through for that while
    # -> TestAnAllowanceLiftsASpentLimit

  Scenario: a restart keeps the day's count
    Given a device used some of its minutes
    When netryx restarts the same day
    Then the minutes already used still count
    # -> TestUsageSurvivesAGracefulRestart
    # -> TestUsageIsReloadedOnlyOnTheSameDay

  Scenario: only real devices are counted
    Given traffic filed under an address or a service key
    When minutes are counted and the limit is written to the kernel
    Then only hardware addresses appear, so the kernel never refuses the rules
    # -> TestOnlyHardwareAddressesAreCounted
    # -> TestASpentKeyThatIsNotAMACNeverReachesTheScript

  Scenario: the agent can read the minutes but not turn the filters off
    Given the router agent writes its own sections and reads through netryx ctl
    When it writes a filters section or asks for a device's usage
    Then its filters section is ignored, and the usage is printed
    # -> TestTheAgentCannotWriteFilters
    # -> TestCtlPrintsTheAPIsAnswer
