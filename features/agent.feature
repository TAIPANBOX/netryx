Feature: the router agent reads netryx and writes through UCI

  The agent (Hermes, through openwrt-mcp) reads with `netryx ctl` from a
  root-only socket and writes a rule as a UCI section that netryx reads again
  on a reload. The socket cannot be used to write.

  Scenario: the agent reads what the page reads
    Given a running service with a device and a rule
    When the agent runs netryx ctl devices, rules, status or apps for a device
    Then it prints the same JSON the console's API answers
    # -> TestCtlPrintsTheAPIsAnswer

  Scenario: only four forms are accepted
    Given the agent's command is an exec argv form granted in openwrt-mcp
    When it is run with any other word, flag or a malformed address
    Then it exits 1 with its usage and sends nothing
    # -> TestCtlRefusesOtherArguments

  Scenario: a rule the agent wrote wrongly says why
    Given a section the agent wrote with a country that does not exist
    When netryx reads its rules again
    Then the rule is kept inactive and netryx ctl rules shows its reason
    # -> TestAMalformedAgentRuleShowsItsReasonInCtl

  Scenario: the socket is root's alone
    Given the service starts under a permissive umask
    When it opens the socket
    Then the socket is mode 0600 and belongs to the service's user
    # -> TestTheSocketIsRootOnly

  Scenario: the socket offers no way to write
    Given the socket bypasses the login and the Host check
    When a write, the session or any other view is asked for over it
    Then it is refused and nothing reaches uci
    # -> TestTheSocketServesNoWriteRoute

  Scenario: several targets, devices or days in one value
    Given the agent can set an option only to one string
    When it writes target "tiktok youtube", days "sat sun" or two MACs in one value
    Then each word is one target, day or device
    # -> TestOneOptionValueMayHoldAWhitespaceSeparatedList

  Scenario: the agent cannot rename or regroup the owner's devices
    Given a device or group section the agent named agent_something
    When netryx reads its rules
    Then the section is ignored
    # -> TestAgentDeviceAndGroupSectionsAreIgnored

  Scenario: an allowance cannot last longer than four hours
    Given an allowance the agent wrote for more than 240 minutes
    When netryx reads its rules
    Then it is ignored, and one for exactly 240 minutes is kept
    # -> TestAnAllowanceLongerThanFourHoursIsDropped

  Scenario: ctl never prints a cut answer
    Given an answer larger than ctl reads
    When ctl reads it
    Then it exits 1 with an error and prints nothing
    # -> TestCtlRefusesAnAnswerLargerThanItReads

  Scenario: the console's device names come from the owner's sections only
    Given a device section the agent named agent_something with a device's MAC
    When the console reads names and groups
    Then the name and group of that device are unchanged
    # -> TestAnAgentDeviceSectionDoesNotNameADevice

  Scenario: the owner's writes never land in the agent's sections
    Given an agent_ device section holds a device's MAC, and an agent_ group exists
    When the owner renames the device, groups it, or creates or deletes such a group
    Then the write goes to the owner's own section, and a group named agent_ is refused
    # -> TestTheOwnersWritesNeverLandInAgentSections
