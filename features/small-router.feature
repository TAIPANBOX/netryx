# netryx on a 512 MB router (the Beryl AX), from the request of 2026-11-26:
# an apply the router cannot hold is refused before nft runs, with the reason,
# and the rules in force stay as they were.

Feature: netryx on a router with 512 MB
  A router with little memory stays the owner's: a rule set it cannot hold
  is refused with the reason, before the OOM killer can choose a process to
  end, and what the router already enforces stays in force.

  Scenario: a rule set that would not fit is refused before nft runs
    Given a router enforcing some country rules and too little memory for more
    When the owner adds a rule that loads new country sets
    Then nft is not run, the kernel keeps the rules it had, and the rule says it was not applied for want of memory
    # -> TestAnApplyThatWouldNotFitIsRefusedBeforeNft
    # -> TestARefusalClearsOnceTheMemoryIsThere
    # -> TestARefusalIsLoggedOnceWhileItHolds

  Scenario: a rule set that fits is applied as before
    Given a router with the memory for the new sets and the margin
    When the owner adds a rule that loads new country sets
    Then the rule is applied
    # -> TestAnApplyThatFitsGoesAhead

  Scenario: a starved router can always shed rules
    Given a router enforcing rules with almost no memory left
    When the owner deletes a rule or adds one that loads no country set
    Then the change is applied, since it holds no more than the router already held
    # -> TestAChangeThatAddsNoCountrySetIsNotHeldBack
    # -> TestAnAppRuleIsNotHeldBackOnAStarvedRouter

  Scenario: the need is counted from what nft holds
    Given the ruleset the kernel holds, the script and the processor
    When netryx counts what an apply needs
    Then it counts nft over both, 22 bytes a byte on aarch64 and 16 on x86-64, the new sets at 7, and keeps 64 MiB free
    # -> TestTheApplyNeedIsCountedFromTheKernelsRulesAndTheScript
    # -> TestTheMarginIsKeptOnBothSidesOfTheLine
    # -> TestTheLineIsDrawnAtTheCountedNeed
    # -> TestTheRulesHeldAtStartAreReadFromRulesNft
    # -> TestMemAvailableIsReadFromMeminfo
    # -> TestTheRoutersMeminfoIsRead

  Scenario: a router whose memory cannot be read applies as before
    Given a router without /proc/meminfo
    When the rules change
    Then they are applied, and the log says the apply was not checked
    # -> TestUnreadableMemoryDoesNotHoldTheRulesBack

  Scenario: a restart without the memory keeps the kernel's country sets
    Given a router enforcing country rules, too short of memory to load their sets again
    When netryx restarts
    Then the country sets stay as the kernel holds them, the rules apply, and the log says the sets were kept
    # -> TestARestartWithoutTheMemoryKeepsTheKernelsCountrySets

  Scenario: country sets kept from the kernel are loaded again once the memory is there
    Given a rule enforced with country sets kept as the kernel held them
    When the console shows it
    Then it is applied, with a note that its ranges were kept from the kernel
    And once the memory is there the next retry loads the sets again and the note goes, without any change of the rules
    And until then a kept country never holds the other rules back
    # -> TestAKeptCountryRuleSaysItsRangesAreFromBefore
    # -> TestKeptCountrySetsAreFilledOnceTheMemoryIsThere
    # -> TestAStaleCountryNeverHoldsTheRestBack
    # -> TestARulesNoteReachesThePage

  Scenario: the memory limit follows the router's RAM unless the config names one
    Given a config without a memory limit
    When netryx starts on a router with at most 768 MiB of memory, or more, or one whose memory cannot be read
    Then the limit is 128MiB, 160MiB and 160MiB, and a limit written in the config, an empty one included, always wins
    # -> TestTheMemoryLimitFollowsTheRoutersMemory
    # -> TestTheMemoryLimitReachesTheServiceAsGOMEMLIMIT
