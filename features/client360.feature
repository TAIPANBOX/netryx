# Client 360, from the decision of 2026-11-22: a device's card that opens and
# shows the whole picture: where it is connected and where its connections
# go, with a full view behind it, after the agent view of another console.

Feature: a client's name opens its card, and its card opens its full view

  Scenario: a click on a client's name opens a card beside it
    Given a client's name anywhere in the console
    When the owner clicks it
    Then a card opens beside it with who the device is, where it is connected, what it does now and links to its full view and its flows
    And a click with a modifier key still follows the link
    # (the card's place: "a card opens beside what was clicked, inside the window" in web/src/windows.test.ts)

  Scenario: several cards for comparing
    Given a card is open
    When the owner opens another client's card
    Then both stay open, the newer in front, and opening one that is open brings it forward instead of a copy
    And a card can be dragged but never off the screen, and closes with its X or with Esc, the one in front first
    # (the cards: "several cards stay open together", "opening a card that is open brings it forward",
    #  "a click brings a card forward; X closes it; Esc closes the top one", "a card is dragged but never off the screen",
    #  "the number of open cards is bounded", "Esc closes a card, but not while the owner types in a field",
    #  "a card is never wider than the window" in web/src/windows.test.ts)

  Scenario: the full view shows the whole picture
    Given the owner opens a client's full view
    When it loads
    Then it shows who the device is, where it is connected, a map of its connections, what it does now, its history, the rules and limits on it and its security
    And each part says when it has nothing or when the router did not answer it, without blanking the rest
    # (where it is connected: "where a device is connected" in web/src/client360.test.ts)

  Scenario: the map of where the connections go
    Given the device's live flows over the window
    When the owner looks at the map
    Then it runs from the device to its apps, the domains, the networks and the countries, banded by what moved, the busiest few in each column
    And every name on it leads to that app, domain, network or country
    # (the map: "the map runs device, apps, domains, networks, countries", "every node leads somewhere",
    #  "each column keeps its busiest few and folds the rest",
    #  "a name that carries under 3% of the window folds into the rest" in web/src/connmap.test.ts)

  Scenario: the rules and limits on the device, and where they come from
    Given rules for the device, for its group and for everyone
    When the owner looks at its rules
    Then each says whether it is the device's own, its group's or everyone's, how often it blocked, and its state
    # (the rules: "the rules on a device and where each comes from" in web/src/client360.test.ts)

  Scenario: the owner renames the device or moves it to a group from its view
    Given the owner may make changes
    When they rename it or pick another group
    Then only what changed is sent, and a blank name changes nothing
    # (the change: "a rename or a new group sends only what changed" in web/src/client360.test.ts)

  Scenario: the full view's speed is steady
    Given the full view's Now
    When the owner reads its speed
    Then it leads with the average over the window picked and shows the last seconds beside it
    # (the tile: "the Speed tile leads with the window's average" in web/src/live.test.ts)

  Scenario: the rows hold still under the pointer
    Given a live table whose order would move
    When the pointer is over it
    Then nothing moves under it, so a click lands on the row that was there
    # (the order: "while the pointer is over the table the order holds" in web/src/steady.test.ts)

  Scenario: a client's view says what is new for it
    Given a client the router has known for a week or more
    When the owner opens its full view
    Then it lists the apps and countries the client used since yesterday and in none of the 29 days before, each with when it was first seen
    And a client known for less than a week says it is too new to tell
    And a restart of the service does not make a client known for weeks too new again
    # -> TestNewKeysAreWhatADeviceUsedSinceTheSplitAndNeverBefore
    # -> TestADeviceSaysWhatIsNewForIt
    # -> TestADeviceSeenForLessThanAWeekIsTooNewToTell
    # -> TestFirstSeenIsTheFirstHourTheHistoryHoldsForTheDevice
    # -> TestWhatIsNewSurvivesARestartThatResetTheInventory
    # (the page: "what is new for a device says when it cannot tell, when nothing is new, and since when" in web/src/client360.test.ts)

  Scenario: a client's view says what reaches it from the internet
    Given a client with a port forwarded to it, a UPnP mapping it opened, or connections from outside
    When the owner opens its full view
    Then the security part lists each forward and mapping and how many connections came from the internet in the last 24 hours, from which countries
    And a client nothing reaches says so
    # -> TestUPnPMappingsAreReadFromTheLeaseFile
    # -> TestTheViewCarriesUPnPMappingsOrSaysThereAreNone
    # -> TestAUPnPMappingNamesItsDevice
    # (the page: "what reaches a device from the internet: forwards, UPnP mappings and connections from outside" in web/src/client360.test.ts)

  Scenario: a client's view shows its usual week and sets the last hour beside it
    Given a client the router has counted on seven days or more of the last 30
    When the owner opens its full view
    Then a grid of the week, in the router's zone, shades each hour of each weekday by the median of what the client moved then
    And a sentence gives both sides: what the hour that just closed moved, and what that hour of the week usually moves, of how many days
    And an hour the router was not counting is no sample, and a side it cannot know is said, never read as zero
    And a client counted on fewer than seven days says it has no usual week yet
    # -> TestTheUsualIsTheMedianOfEachHourOfTheWeekInTheRoutersZone
    # -> TestTheUsualStartsWhenTheDeviceWasFirstSeen
    # -> TestAnHourIsPlacedByItsMiddleInTheRoutersZone
    # -> TestADeviceAnswersItsUsualWeekAndTheLastHour
    # -> TestADeviceWithLessThanAWeekOfDaysHasNoUsualYet
    # -> TestTheUsualWeekIsReadInTheRoutersZone
    # (the page: "the usual says both sides: what the last hour moved and what that hour of the week usually moves" in web/src/client360.test.ts)
    # (the page: "the usual says when a side is missing, never reading it as zero" in web/src/client360.test.ts)
    # (the page: "the week grid runs Monday to Sunday, shades by the busiest slot, keeps unknown slots apart and marks the hour that closed" in web/src/client360.test.ts)
