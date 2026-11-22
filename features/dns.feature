# @decided 2026-09-15: read the questions devices ask the router's own
# resolver. It is the one naming that still works when the handshake hides the
# destination, and it is a setting, because a list of names is a list of
# interests.

Feature: the resolver names what the handshake will not

  Scenario: a question is a destination, said out loud
    Given a device asks the router's resolver where a name lives
    When netryx reads that question
    Then the name is recorded for that device, marked as learned from the
      resolver rather than from the handshake
    # -> TestAQuestionNamesTheDeviceIsAsking

  Scenario: an answer gives an address a name
    Given the resolver answers with one or more addresses
    When netryx reads that answer
    Then those addresses carry the name that was asked for, including when
      the answer refers back to the question instead of repeating it
    # -> TestAnAnswerGivesTheAddressesAName

  Scenario: an encrypted handshake still lands on a name
    Given a device whose browser hides the destination inside the handshake
    When that device had asked the resolver for the same place a moment before
    Then the flow is named from the resolver's answer, and it is still counted
      as a handshake that hid its name
    # -> TestTheResolverNamesWhatTheHandshakeHides

  Scenario: a name learned twice says so
    Given a name that appears both in a question and in a handshake
    When netryx records it
    Then the row says it was seen both ways
    # -> TestANameSeenBothWaysSaysSo

  Scenario: a name that did not resolve is not a name
    Given an answer that says the name does not exist, or a message that is
      not a question at all
    When netryx reads it
    Then nothing is recorded
    # -> TestOnlyUsefulAnswersCount

  Scenario: these bytes come off somebody's network
    Given a message that is truncated, or points at itself, or claims more
      answers than it carries, or is not DNS at all
    When netryx reads it
    Then it is refused, the monitor keeps running, and no name is invented
    # -> TestHostileMessagesAreRefusedNotGuessed

  Scenario: both sides of the conversation are read
    Given the questions arrive at the bridge and the answers leave the router
    When netryx installs its rules
    Then each side has its own rule with its own bound, and turning the
      setting off removes both
    # -> TestTheResolverIsReadOnBothSides

  Scenario: the resolver cannot fill the router's memory
    Given more answers than the table is allowed to hold
    When netryx keeps reading
    Then it stops at the cap and says how much it refused
    # -> TestTheNameTableIsCappedToo
