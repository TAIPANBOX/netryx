# Phase 1, from what he asked for on 2026-09-15: a monitor that says which
# device went where, installable by other people on their own OpenWrt router.

Feature: a flow belongs to a device, even behind NAT

  Scenario: the pre-NAT source is recovered from the connection table
    Given the router has NATed a device's connection to the internet
    When netryx reads the kernel's connection table
    Then the flow carries the house address the device really used
    And the address the world saw is kept beside it
    # -> TestParseConntrackFindsThePreNATSource

  Scenario: IPv6 needs no recovery
    Given a device with its own global IPv6 address
    When netryx reads the connection table
    Then the device is visible in the source address and nothing is marked NATed
    # -> TestParseConntrackFindsThePreNATSource
    #
    # @claude 2026-09-15: this scenario is TRUE of one packet and MISLEADING as
    # a rule. A phone rotates through privacy addresses, so "the device is
    # visible in the address" turns one phone into a dozen rows that each
    # vanish in a day. The scenarios below are how it actually works; this one
    # stays as written until he decides how to say it, because the words are
    # his.

  Scenario: a phone that rotates its addresses is still one device
    Given a device that has used several IPv6 addresses and an IPv4 one
    When netryx reads the kernel's neighbour table
    Then all of those addresses map to the one hardware address
    And the device is counted once, not once per address
    # -> TestRotatingIPv6AddressesCollapseToOneDevice

  Scenario: a device that goes quiet is still the same device
    Given the kernel has evicted a neighbour after a minute of silence
    When that device sends again
    Then netryx still knows which device the address belonged to
    # -> TestADeletedNeighbourIsStillRemembered

  Scenario: an entry that binds nothing is not an answer
    Given a neighbour entry in the INCOMPLETE or FAILED state
    When netryx reads it
    Then it is not used to name a device
    # -> TestParseMessage

  Scenario: a device is called what its owner calls it
    Given a DHCP lease that carries the name the device gave
    When netryx builds its device table
    Then the flow is labelled with that name, falling back to the MAC and then
      to the address, so a row is never blank
    # -> TestParseLeasesAndNames
