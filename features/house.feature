# The house-wide Apps and Destinations screens, from the decision of
# 2026-11-22: the two greyed tabs of the console work, the way the Clients and
# Overview screens do, from the history the router already keeps.

Feature: the console shows the whole house by app and by destination

  Scenario: every app the house used, with the clients behind it
    Given the house used several apps over the period the owner picked
    When the owner opens Apps
    Then each app is listed with its icon, category, traffic down and up and its share of the house
    And each row says how many clients used it and names the busiest
    # -> TestTheHouseAppsListNamesTheClientsOfEachApp
    # (the row's names and the search: web/src/house.test.ts)

  Scenario: the router and the folded rest are not clients
    Given an app's traffic includes the router's own and the folded rest
    When the Apps list counts its clients
    Then only devices with a hardware address are counted and named first, and the rest is marked as such
    And a row with no client behind it says whose traffic it is
    # -> TestTheHouseAppsCountOnlyClients
    # -> TestARowWithoutClientsSaysWhoseTrafficItIs

  Scenario: a house full of names stays a short answer
    Given the history holds more apps than a page can show
    When the owner opens Apps
    Then the list stops at a hundred apps and each row names at most three clients
    # -> TestTheHouseAppsListIsBounded

  Scenario: one app across the house
    Given an app the house used
    When the owner opens it from the list
    Then the console shows its clients, the countries and networks it reached, the names it asked for, and its traffic over the period
    # -> TestAnAppDetailShowsItsClientsPlacesDomainsAndSeries

  Scenario: an app that is not there
    Given an id the catalog does not know and the period never saw, or text that could not be an id
    When the console asks for it
    Then the answer is not found or refused, and a known app that was quiet is shown with nothing moved
    # -> TestAnAppNobodyKnowsIs404AndAMalformedIdIs400

  Scenario: every country the house reached, and who went there
    Given the house's devices reached several countries and talked to each other
    When the owner opens Destinations
    Then each country is listed with its share of what left the house and the clients that went there
    And the networks and the domains the house reached are listed beside it
    And the house's own traffic is not a destination
    # -> TestTheHouseDestinationsLeaveTheHouseOut

  Scenario: one country across the house
    Given a country the house reached
    When the owner opens it
    Then the console shows the clients that went there, with which apps, to which networks, and when
    # -> TestACountryDetailShowsItsClientsAppsAndNetworks
    # -> TestACountryCodeMustBeTwoLetters

  Scenario: a closed country or app says so where it is listed
    Given a rule blocks a country, an app or the app's category
    When the owner looks at that country or app
    Then the row says it is blocked, for whom when not everyone, or why the rule is not working, and leads to the rule
    And the country or app can be blocked for everyone from its screen until a rule for everyone is in force
    # (rulesForApp, rulesForCountry: web/src/house.test.ts)

  Scenario: the lists come from one pass over the history
    Given the router keeps a day of minutes and a month of hours and days
    When the Apps or Destinations list is asked for the clients of every row
    Then the history is read once, and the answer is the one each row alone would get
    # -> TestPairsAgreeWithTopForEveryKey
    # -> TestPairsHonourTheFilter
    # -> TestPairsRefuseDimensionsKeysDoNotCarry
