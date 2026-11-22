# Seeing more about a thing from wherever it is named, from the decision of
# 2026-11-22: each row says how its last hour went, any thing can be found by
# typing its name, a name shows a short card before it is opened, and a thing's
# view names what it belongs to and what belongs to it.

Feature: every thing in the console can be found and seen in place

  Scenario: each row of Clients and Apps shows its last hour
    Given the client list or the apps list
    When the owner looks at a row
    Then it shows a small line of that row's traffic over the last hour
    And the minutes the router had not counted yet are left blank, not drawn as quiet
    # -> TestSparksAreEachKeysMinutesInSteps
    # -> TestAnEmptyStoreKnowsNoStep
    # -> TestTheListsCarryAnHourOfSparkline
    # (the line: "a list's hour is drawn only from where the router began counting" in web/src/live.test.ts)

  Scenario: any thing can be found by typing
    Given any screen of the console
    When the owner presses Cmd+K, Ctrl+K or "/" outside a text field and types part of a name
    Then the clients, apps, categories, countries, networks, domains, groups and rules that match are listed, best match first
    And an address, a MAC, a hostname or an AS number finds its thing too
    And a folded row or an unknown is never listed
    # (the search: "typing a name finds the thing, best match first",
    #  "an address, a MAC, a hostname or an AS number finds its thing too",
    #  "a rule is found by what it names, and folded rows are not things",
    #  "the list is short and says what kind each thing is" in web/src/search.test.ts)

  Scenario: a name shows a short card before it is opened
    Given a name of a client, app, category, country, network, domain, group or rule
    When the owner rests the mouse on it for a moment
    Then a small card shows a few facts about it over the last day and a link to open it
    And a click on the card keeps it open until it is closed, Esc is pressed or the owner moves elsewhere
    # (the card: "a link's address says which thing its card shows, and where to read it",
    #  "a card says the few things worth knowing before opening it" in web/src/hover.test.ts)

  Scenario: a thing's view says where it sits and what it belongs to
    Given the view of a client, app, country, network, domain or group
    When it opens
    Then a chain of chips names the path the owner took to it, or the screen it belongs under, and the thing itself
    And chips name what it belongs to: an app's category, a network's countries, a domain's apps, a client's group, each a link
    And the view lists what belongs to it: a group's devices, an app's clients, places and domains, a network's clients and apps
    # (the chips: "a thing's view names what it belongs to, each a link",
    #  "the breadcrumbs name the path the owner took" in web/src/entity.test.ts)
    # -> TestANetworkPageNamesItsCountriesClientsAndApps
    # -> TestAnAppDetailShowsItsClientsPlacesDomainsAndSeries

  Scenario: two or three clients are compared side by side
    Given the client list
    When the owner ticks two or three clients and presses Compare
    Then their state, speed now, last day, busiest app, countries, group, connection and last hour stand side by side
    And the comparison has its own address that can be shared, and a fourth client cannot be ticked
    # (the comparison: "two or three clients are compared side by side, from a shareable address",
    #  "picking clients to compare stops at three and a second pick takes one back",
    #  "the comparison lines up what tells devices apart" in web/src/compare.test.ts)

  Scenario: a person and the devices that are theirs
    Given the owner adds a person and ticks the devices that are theirs
    When the owner opens one of those devices, searches the person's name or rests the mouse on it
    Then the device says whose it is, and the person's page lists their devices and their traffic
    And a device is one person's at most: giving it to a second person is refused and names the first
    And deleting a person leaves the devices, their groups and every rule as they were
    And the person is kept in the router's config, so a restart reads it back
    # -> TestAPersonIsWrittenAndReadBack
    # -> TestADeviceBelongsToOnePerson
    # -> TestDeletingAPersonLeavesDevicesAndRules
    # -> TestAPersonSurvivesARestart
    # -> TestAWriteRouteIsReachableOnlyThroughTheGate
    # (the page: "a person is a thing of its own: its page, its devices' chip, its route" in web/src/entity.test.ts,
    #  "a new person gets an id from the name, or a free one", "a device names its person, if it has one" in web/src/people.test.ts)

  Scenario: the last hour goes on across a restart of netryx
    Given netryx has counted the house for a while
    When netryx is restarted or upgraded, but the router is not rebooted
    Then the rows' last hour and the Now columns go on from where they were
    And only a reboot of the router starts them afresh, and then they say since when they count
    # -> TestTheMinutesSurviveARestart
    # -> TestLoadedMinutesKeepToTheRingAndTheClock
    # -> TestALoadedMinuteIsHeldToTheKeyCap
    # -> TestTheMinutesAreKeptInRAMAcrossARestart
