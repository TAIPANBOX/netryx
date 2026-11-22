# The console, stage 1, from the request of 2026-11-20: a page on the router
# that shows each client's traffic over time, the apps it uses, where those
# apps go, and the connections themselves, and keeps that history.

Feature: the console shows what each client did, by app and by place

  Scenario: a client's traffic is drawn over time, download and upload apart
    Given a client that used two apps over the last hour
    When the console draws its traffic split by app
    Then each step of the chart carries each app's download and upload separately
    # -> TestSeriesSplitsByApp

  Scenario: each app a client uses shows its download and upload
    Given a client that used several apps
    When its card lists the apps
    Then each app carries what it downloaded and what it uploaded
    # -> TestDeviceAppsCarryDomainsAndPlaces

  Scenario: each app shows which countries and organisations it talks to
    Given an app a client reached in more than one place
    When the card opens that app
    Then it lists each country with the organisation behind the addresses
    # -> TestDeviceAppsCarryDomainsAndPlaces

  Scenario: bytes are counted under the app even when the name arrives later
    Given a connection whose name is only learned after it opened
    When its bytes move before the name is known
    Then they are held and counted under the app once the name arrives
    # -> TestDeltasWaitForTheName

  Scenario: traffic with no name is grouped by the organisation it goes to
    Given a connection that carried no name the console can read
    When it is classified
    Then it is grouped by the organisation that owns the address it went to
    # -> TestResolveFallsBackToOrganisation

  Scenario: counts stay right when the router offloads flows
    Given an offloaded connection, whose bytes are only seen by reading the
      connection table again and again
    When it is read several times and then ends
    Then its total is its final counters, never their sum
    # -> TestCountersNeverCountTwice

  Scenario: flows can be searched and filtered
    Given the list of connections
    When it is searched by text and filtered by app, country and direction
    Then only the matching connections are shown
    # -> TestQueryFiltersByTextAppCountryAndDirection

  Scenario: history survives a restart
    Given a day of counted traffic written to the router's flash
    When the service starts again
    Then the same history is shown
    # -> TestHistorySurvivesARestart

  Scenario: stopping the service keeps what it counted
    Given traffic counted in the hour that is still open
    When the service is stopped
    Then that hour is written too, and the next start reads it back
    # -> TestStateIsSavedOnShutdown

  Scenario: closed hours reach the flash while the service runs
    Given traffic counted in an hour that has closed
    When an hour has passed since the last write
    Then that hour is written, so a power cut loses at most the hour in progress
    # -> TestClosedHoursAreWrittenWhileRunning
    # -> TestNothingIsSavedAfterShutdown

  Scenario: a router that starts before its clock is set still gets its history
    Given history written while the clock was right
    And a router that boots with a clock hours behind
    When the clock is set and passes that history
    Then the history is loaded then, not lost
    # -> TestHistoryWrittenBeforeTheClockWasSetIsLoadedOnceItIs

  Scenario: the console runs without the downloaded data
    Given a router without the address table or the app map
    When the console starts
    Then it shows the connections, with the country unknown and the app Unknown
    # -> TestMissingDataFilesDegradeNotFail

  Scenario: with the data, a connection gets its country, organisation and app
    Given the address table and the app map
    When a connection to a known network ends
    Then it carries that network's country and organisation, and its app
    # -> TestDataFilesNameTheFlows

  Scenario: who opened a connection is decided by which end is at home
    Given a router serving a house network, a guest network and an IPv6 prefix
    When a connection is classified
    Then every network the router serves and its own addresses count as home,
      and the internet line's subnet does not
    # -> TestTheRoutersOwnAndLANAddressesAreLocal

  Scenario: the internet tile shows latency only once a probe has replied
    Given the internet line and a probe sent every ten seconds
    When no probe has replied yet
    Then latency is shown as no data rather than zero
    And once one replies, latency, loss and the line's rates are shown
    # -> TestTheWANTileGetsLatencyFromTheProber

  Scenario: by default the probe contacts no third party
    Given no internet target is configured
    When the internet tile is measured
    Then the probe goes to the line's own gateway
    And a configured target replaces it
    # -> TestTheProbeTargetsTheGatewayByDefault

  Scenario: a configured probe target cannot become an option
    Given a target in the config that looks like a command line option
    When the probe runs
    Then the target is refused and the gateway is probed instead
    # -> TestAProbeTargetCannotBeAnOption

  Scenario: a page elsewhere cannot reach the console by rebinding a name
    Given a request that names some other host
    When it reaches the router
    Then it is refused before any route runs, with nothing echoed back
    And the router's own names and addresses still work
    # -> TestAForeignHostIsRefused

  Scenario: a stuck command cannot hold the service
    Given a command that leaves a child holding its output open
    When its deadline passes
    Then the service stops waiting for it soon after, and keeps what it printed
    # -> TestAHungChildIsCutOffAfterItsDeadline

  Scenario: each client shows how it is connected
    Given a client on a cable and a client on Wi-Fi
    When the client list is built
    Then the wired one names its port and the Wi-Fi one its network
    And the router's own addresses are not clients
    # -> TestClientsShowTheirPortOrTheirNetwork

  Scenario: the page is the console and loads nothing from outside the router
    Given the service is running
    When the page and its API are requested
    Then the page carries rules that forbid loading anything from elsewhere
    And the API answers under /api/
    # -> TestTheConsolePageAndItsAPIAreServed
    # -> TestUIHeadersForbidOutsideLoads

  Scenario: each device appears once in the client list
    Given connections from a device whose hardware address only the neighbour
      table or a lease knows, and one from an address nothing knows
    When the client list is built
    Then each device is one row under its hardware address
    And no row without a hardware address is listed
    # -> TestEachDeviceAppearsOnce

  Scenario: a client's rate shows while it moves bytes
    Given a device on a fixed address, known only from the neighbour table
    When it downloads
    Then it is one client, online, with the rate it is downloading at
    # -> TestADeviceKnownOnlyFromTheNeighbourTableHasItsRate
    # -> TestARateFollowsAnAddressLearnedLater

  Scenario: a long download keeps a device online
    Given a device that has made no new connection for an hour
    When a connection of its is still moving bytes, or the kernel finds it reachable
    Then it is shown as online
    # -> TestALongDownloadKeepsADeviceOnline

  Scenario: right after a start or restart the chart has minute resolution and true rates
    Given the service has just started again
    When the last hour or the last day is drawn
    Then the time since the start is drawn minute by minute and the time before
      it from the hours, each point saying how long it covers
    And every speed is taken over the time its point covers
    # -> TestARestartStillAnswersTheLastHourInMinutes
    # -> TestADayAndAWeekAreAnsweredAtTheirOwnSteps
    # -> TestTheSeriesSaysWhichStepItUsed

  Scenario: the console's own access is not a client
    Given the console polled through a tunnel on the router's loopback
    When its connections are counted
    Then they are no client, no traffic and no history
    # -> TestTheConsoleTunnelIsNotAClient

  Scenario: the router's own traffic is the router's
    Given the router itself talking to the internet
    When its connections are counted
    Then they are shown as the router, never as a client
    # -> TestTheRoutersOwnTrafficIsTheRouter

  Scenario: local traffic is not internet traffic
    Given two devices at home talking to each other
    When the internet chart and the countries are drawn
    Then that traffic is left out of both
    And the device's own views show it as the local network
    # -> TestLocalTrafficIsNotWAN

  Scenario: a finished connection stays with the device that made it
    Given a connection that ended while nothing knew its device
    When its address is later given to another device
    Then the finished connection is not shown as that device's
    And bytes a device moved stay that device's after its address moves on
    # -> TestAFinishedFlowIsNotGivenToTheAddressesNextOwner
    # -> TestBytesAreFiledUnderTheDeviceThatMovedThem

  Scenario: a connection that is still open shows its name, app and country
    Given an open connection whose name is known
    When the connection table is read again while it runs
    Then the flows screen shows its name, its app and its country before it ends
    # -> TestALiveFlowCarriesItsNameAppAndCountry

  Scenario: the local network is not counted as a country
    Given a device that talked to one country and to another device at home
    When its countries are counted and listed
    Then it has one country, and the local network is not among them
    # -> TestLANIsNotACountry

  # Stage 1b, from the request of 2026-11-20: every app carries its real icon,
  # and a device card opens on what the device is doing right now.

  Scenario: every app shows its own icon
    Given an app the catalog knows
    When the console draws it
    Then it carries its brand's icon on its brand's colour
    And the icon is drawn again from the brand's shape alone, with nothing
      else from the source file, and an app with no icon gets a letter
    # -> TestTheManifestNamesEveryAppWithItsKindAndColour
    # -> TestIconsAreRegeneratedNotCopied
    # -> TestPathDataWithMarkupIsRefused
    # (the letter tile: web/src/icons.test.ts)

  Scenario: the console never loads anything from outside, and the router fetches a brand's icon once
    Given a brand whose logo is not free to ship with the console
    When its app is shown
    Then the router fetches the brand's icon once, from the brand's own site,
      keeps it and serves it itself
    And only a small picture is kept, never a page or a script, and a site
      that fails is asked again only after a week
    And the page in the browser loads nothing from anywhere but the router
    # -> TestAFaviconIsFetchedOnceAndKept
    # -> TestAFaviconMustBeASmallRasterImageFromTheBrandsOwnSite
    # -> TestAFailureIsNotRetriedForAWeek
    # -> TestTheFetcherNeverConnectsToPrivateAddresses
    # -> TestFetchedIconsAreServedOnlyFromTheStateDir
    # -> TestUIHeadersForbidOutsideLoads

  Scenario: fetching icons is a setting, and the setting reaches the service
    Given the setting that lets the router fetch icons is turned off
    When the service is started from the router's configuration
    Then no brand's site is contacted, and those apps show a letter
    And every other setting written in the configuration reaches the service too
    # -> TestIconFetchingCanBeTurnedOff
    # -> TestTheInitCommandLineIsParsedAsWritten

  Scenario: a router that boots before its internet line is up still gets its icons
    Given a router that has just started and cannot reach the internet yet
    When it tries to fetch the brands' icons
    Then it waits a little after the start, gives up the round after a few
      network errors in a row, and holds nothing against the brands' sites,
      so the next round asks them again
    And each site gets ten seconds, two seconds apart, once a day
    # -> TestABootWithoutTheInternetCostsNoIcons
    # -> TestANetworkErrorIsNotTheSitesFailure
    # -> TestAnUnansweredSiteIsNotTheSitesFailure
    # -> TestTheIconLoopKeepsItsPace
    # -> TestDueSaysWhetherASiteWouldBeAsked

  Scenario: opening a device shows what it is doing right now
    Given a device moving traffic through several apps
    When its card is opened
    Then it shows its rate now, its active apps, its connections and where
      they go, over the last five minutes
    # -> TestTheLiveViewShowsAppsConnectionsAndPlaces
    # -> TestTheLiveViewIsBounded
    # -> TestTheLiveRingKeepsFiveMinutesPerDevice

  Scenario: the router works harder only while someone watches
    Given nobody is looking at a device right now
    When its card is opened
    Then the router reads its connections every two seconds at once
    And when the card is closed it goes back to the usual pace within about
      ten seconds
    # -> TestSyncSlowsDownWhenNobodyWatches
    # -> TestALivePollIsReported

  Scenario: a page left open across an upgrade learns of it
    Given the console is open in a tab while the router is upgraded
    When the page next hears from the router
    Then it says the router now serves a newer console and offers a reload
    # -> TestEveryAnswerNamesTheBuildServed
    # -> TestTheBuildIsReadFromBesideThePage
    # (the page: "a page left open across an upgrade is told the router serves a newer one" and
    # "a newer build seen before anyone listens is still told to the first listener" in web/src/api.test.ts)
