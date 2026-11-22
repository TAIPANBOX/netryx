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
