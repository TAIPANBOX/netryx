# Clickable everywhere, from the decision of 2026-11-22: every name in the
# console leads to its own screen, a filter lives in the address so Back and a
# shared link keep it, the tiles and charts open their details, and a detail
# says the path that led to it.

Feature: every name in the console leads somewhere

  Scenario: a name is a link to its screen
    Given a client, an app, a country, a network, a domain, a category, a group or a rule named anywhere in the console
    When the owner clicks the name
    Then its screen opens: a client's card, the app, the country, the network, the domain, the apps of the category, the group or the rule
    And a folded rest, the house itself or an unknown network stays text
    # (the links: "every kind of name leads to its own screen" in web/src/entity.test.ts)

  Scenario: a network's page
    Given a network the house reached
    When the owner opens it
    Then it shows the network's organisation, the countries it was reached in, the clients and the apps that reached it, and a link to its flows
    And every client and app that reached it is counted, not only the busiest
    # -> TestANetworkPageNamesItsCountriesClientsAndApps
    # -> TestHoldersAgreeWithTopPerDeviceAndApp
    # -> TestFlowsCanBeAskedForOneNetwork

  Scenario: a domain's page
    Given a domain the house asked for
    When the owner opens it
    Then it shows the apps it was named for, how it was named, and the clients that asked for it
    And text that could not be a domain is refused
    # -> TestADomainPageNamesItsAppsAndClients

  Scenario: a filter lives in the address
    Given the owner filters Apps by a category or searches Clients or Apps
    When they go elsewhere and press Back, or share the address
    Then the filter is there again
    # (the address: "a filter is written into the address", "the new screens and the filters in the address" in web/src/entity.test.ts)

  Scenario: a stretch of a chart opens its flows
    Given the house's traffic chart
    When the owner clicks a stretch of it
    Then the flows of that stretch of time open
    And the stretch is the one drawn nearest the click
    # -> TestFlowsCanBeAskedForAStretchOfTimeAndANetwork
    # (the stretch: "a click on a stretch of a chart asks the flows of that stretch" in web/src/entity.test.ts,
    #  "a click on a chart picks the point drawn nearest to it" in web/src/geometry.test.ts)

  Scenario: the line's tile opens the line
    Given the WAN, Speed or Line quality tile on the overview
    When the owner clicks it
    Then a card shows the line's state, addresses, uptime, speed and quality, read without the history
    # -> TestTheLineCardAsksOnlyForTheLine

  Scenario: a detail says how the owner got there
    Given the owner went from an app to one of its clients
    When the client's view opens
    Then its breadcrumbs read Apps, the app, then the client, each a link back
    # (the crumbs: "the breadcrumbs name the path the owner took", "a link from a detail carries where it came from" in web/src/entity.test.ts)
