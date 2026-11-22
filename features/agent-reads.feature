# The router agent reads netryx, from the decision of 2026-11-22: Hermes may
# read what netryx knows through openwrt-mcp, and only read it; it does so over
# ubus, since the agent's client is never given a way to run programs.

Feature: the router agent reads netryx over ubus and changes nothing through it

  Scenario: the agent reads the six things the console shows
    Given netryx is running and the agent may call the ubus object netryx
    When it asks for the status, the devices, the rules, the alerts, or a device's apps or time limits
    Then it gets the same answer the console's page reads
    # -> TestAUbusCallAnswersWhatCtlReads
    # -> TestTheUbusObjectOffersTheSixReadsOnly

  Scenario: a big house reaches the agent whole
    Given a house with more devices or rules than openwrt-mcp keeps of a list
    When the agent reads the devices or the rules
    Then every one is there, under its own address or name, and an answer too big for ubus is refused with the reason
    # -> TestTheDevicesAndRulesComeKeyedSoOpenwrtMcpCutsNoRow
    # -> TestAnAnswerLargerThanUbusCarriesIsAnError

  Scenario: nothing but those reads goes through
    Given the agent calls the object with another method, extra arguments or a malformed address
    When netryx looks at the call
    Then it refuses with the reason before asking the service anything
    # -> TestAUbusCallRefusesAnythingElseBeforeAsking

  Scenario: a service that cannot answer is not an empty house
    Given netryx is stopped, or the device asked about is unknown
    When the agent reads
    Then it is told why, and never sees an empty list it would take as the truth
    # -> TestAUbusCallSaysWhenTheServiceCannotAnswer

  Scenario: a login to the console does not reach the agent's reads
    Given someone has a LuCI login with the console's rights
    When they try to call the ubus object netryx
    Then the console's rights do not include it
    # -> TestTheConsolesACLGrantsNoCallOfTheUbusObject
