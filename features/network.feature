# The Network screen, from the decision of 2026-11-22: the console's last
# greyed tab shows how the router is wired, read only, as stage 4 of the plan
# names it (Wi-Fi, topology, VPN), and says what a router does not have.

Feature: the console shows how the router is wired

  Scenario: the router's networks
    Given the router runs a LAN, a WAN and perhaps more networks
    When the owner opens Network
    Then each network shows its state, protocol, device, addresses, gateway, uptime and traffic
    And the configured WAN, or another network holding a default route, is marked as the internet, a tunnel is not, and loopback is not listed
    # -> TestInterfacesAreReadFromNetifd
    # -> TestTheWANIsTheConfiguredLineAndNotATunnel

  Scenario: the ports and who is behind each
    Given devices are plugged into the house bridge's ports
    When the owner looks at the ports
    Then each wired port shows its link and speed, whether it sits on the hardware switch, and the devices learned behind it by name
    And an access point is not listed as a port
    # -> TestTheViewIsReadFromTheRouter
    # -> TestTheNetworkViewNamesTheDevicesBehindEachPortAndAccessPoint

  Scenario: Wi-Fi radios, networks and clients with their signal
    Given the router has Wi-Fi
    When the owner looks at Wi-Fi
    Then the radios show band, channel and width, each running network its clients with their signal, and a network not running says so
    And no Wi-Fi password is ever read
    # -> TestWirelessConfigNamesRadiosAndNetworks
    # -> TestTheNetworkViewNamesTheDevicesBehindEachPortAndAccessPoint

  Scenario: WireGuard peers
    Given the router has WireGuard peers
    When the owner looks at WireGuard
    Then each peer shows its name, endpoint, allowed addresses, last handshake and traffic
    And no private or preshared key ever reaches the page
    # -> TestWireGuardPeersCarryNamesHandshakesAndTrafficButNoSecret
    # -> TestTheNetworkViewNeverCarriesASecret
    # -> TestWgIsAskedOnceForEveryInterface

  Scenario: port forwards
    Given the firewall leads ports from the internet to devices at home
    When the owner looks at the forwards
    Then each shows its protocol, outside port and the device it leads to by name, and a forward turned off says so
    # -> TestPortForwardsAreTheFirewallsDNATRedirects
    # -> TestTheNetworkViewNamesTheDevicesBehindEachPortAndAccessPoint

  Scenario: what the router does not have is said
    Given a router without Wi-Fi, WireGuard, forwards or the wg tool
    When the owner opens Network
    Then each section says what is missing instead of showing an empty box
    And a config that is there but cannot be read is said so, never taken for a missing feature
    # -> TestWhatTheRouterLacksIsSaid
    # -> TestAConfigThatCannotBeReadIsSaidSo
    # -> TestPackageLinesAreRead
    # -> TestTheNetworkViewSaysWhatTheRouterLacks
    # -> TestWithoutANetworkSourceTheViewIsUnavailable

  Scenario: a page that polls does not load the router
    Given the page asks for the network view every ten seconds
    When it is asked again within a few seconds
    Then the router is not asked again
    And viewers asking at once share one reading, and one who leaves neither waits nor cuts the reading short
    # -> TestTheViewIsCachedForAFewSeconds
    # -> TestViewersShareOneRead
    # -> TestAReadIsNotCutShortByTheViewerLeaving
    # -> TestAWaitingViewerCanLeave

  Scenario: the Network screen says what the backup line carried this month
    Given a router with a second uplink or a mobile modem beside its configured internet line
    When the owner opens the Network screen
    Then it says on how many of the last 30 days the backup line carried traffic, how much, and its share of what the lines carried on those days
    And each day is the difference of two closes of the daily files, unknown when a close is missing, the router was off for part of the day, or a counter restarted
    And a router with no backup line says so and names its line
    # -> TestTheBackupLinesAreTheOtherUplinksAndAModemEvenWhenIdle
    # -> TestCountersAreEachDaysCloseOverTheWindow
    # -> TestALineCarriesTheDifferenceOfTwoCloses
    # -> TestTheBackupLineSaysTheDaysItCarriedAndItsShare
    # -> TestTheLinesAreServedWithTheBackupFoundInTheView
    # (the page: "the backup line says on how many days it carried traffic, how much, and its share of the lines on those days" in web/src/lines.test.ts)
    # (the page: "without a backup line it says so, and names the line there is" in web/src/lines.test.ts)
    # (the page: "the backup line's days are drawn oldest first, a used day apart from a day of checks, an unknown day as no bar" in web/src/lines.test.ts)
