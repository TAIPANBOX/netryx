# @decided 2026-09-15: select the packets at the netdev ingress hook, and prove
# it on the live routers without the house losing its internet.

Feature: the handshake is seen before the kernel can hide the flow

  Scenario: an offloaded flow never reaches the forward hook
    Given a router with flow offloading enabled, which is the default on this
      hardware
    When netryx selects packets at the bridge's ingress instead
    Then the first packets of a connection are copied before any flowtable can
      claim it
    # -> TestIngressCopiesHandshakesAndNotTraffic

  Scenario: the hook goes on the ports, not on the bridge
    Given the operator names br-lan, the way every OpenWrt document does
    When netryx installs the ingress chain
    Then it attaches to the devices the bridge is made of, so that it also
      sees traffic bridged between two ports and traffic a flowtable on a
      port would otherwise claim first
    # -> TestABridgeBecomesItsMembers

  Scenario: the monitor never decides anything
    Given the rule sits in the path of every packet entering the house network
    When it matches
    Then it copies and accepts, and carries no verdict that could drop traffic
    # -> TestIngressCopiesHandshakesAndNotTraffic

  Scenario: what is copied is a handshake, not somebody's traffic
    Given a flow that keeps sending for longer than the set's timeout
    When netryx selects packets at the ingress hook
    Then only a packet carrying a ClientHello opens a budget, the budget is
      refreshed rather than recreated, and the flow is never copied again
      once it is spent
    # -> TestIngressCopiesHandshakesAndNotTraffic

  Scenario: the router itself is still watched
    Given the operator chooses the ingress hook
    When netryx installs its rules
    Then the router's own outbound traffic keeps its own chain, because
      nothing the router originates arrives on a bridge port
    # -> TestIngressStillWatchesTheRouterItself

  Scenario: lost copies are not a lost run
    Given more packets arrive than the reader drains, on the busiest minute
    When the kernel reports that it dropped copies
    Then the watcher says so and keeps reading, and it never exits in a way
      that leaves its own table installed
    # -> TestLostCopiesAreNotALostRun

  Scenario: the device is named by the frame itself
    Given a packet that has not been through NAT yet and has no connection
      attached
    When netryx reads it
    Then the sender's hardware address names the device, and an address it
      cannot read leaves the field empty rather than inventing a device
    # -> TestAtIngressTheFrameNamesItsOwnDevice

  Scenario: a run leaves the router as it found it
    Given a watch that installed either the ingress or the forward form
    When it stops, in any way it can stop
    Then both tables are removed, so no chain is left copying packets to a log
      group nobody reads
    # -> TestRemoveTakesBothHooksAway

  Scenario: the router is asked what it does to established flows
    Given a router whose firewall hands established flows to a flowtable
    When netryx checks itself before capturing
    Then it says that selecting in the forward hook would go blind, and names
      the mode that works instead of a package to install
    And a router that has offloading off, or no firewall config to read, is
      not reported as broken
    # -> TestOffloadingIsOnlyAProblemForTheForwardHook

  Scenario: the setting is read where it actually lives
    Given a firewall config whose zones mention offloading and whose defaults
      have it off
    When netryx reads that config
    Then only the defaults decide, and every spelling uci uses for true is
      understood
    # -> TestOnlyTheDefaultsSectionDecides

  Scenario: the check cannot pass where the watcher would fail
    Given the operator runs the check before capturing anything
    When netryx asks the kernel whether its rule would be accepted
    Then it checks the very ruleset the watcher installs, in the mode chosen,
      and the router's own ruleset is left untouched
    # -> TestValidateChecksTheRulesetThatWillBeInstalled

  Scenario: a failure names its own cause
    Given the kernel refuses the rule
    When netryx reports it
    Then it says whether the interface does not exist, nft is missing, the
      permission is wrong, or the kernel build lacks the expression, and it
      never answers a working nftables with "install nftables"
    # -> TestTheRuleFailureSaysWhichFailureItWas
