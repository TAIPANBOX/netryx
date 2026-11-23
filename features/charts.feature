# Reading the traffic charts, from the decision of 2026-11-22 on the console's
# next steps: the mouse tells what each part of a chart moved at that moment,
# every chart of a screen points at the same moment, a stretch dragged on a
# chart becomes what the whole screen shows, the legend hides and isolates
# layers, alerts sit where they happened, and the busiest moment can be opened
# with one click.

Feature: the traffic charts can be read and explored in place

  Scenario: the mouse over a chart says what moved at that moment
    Given a traffic chart over a period
    When the owner points at a moment on it
    Then a line marks that moment and a box names the step it covers
    And the box lists each visible layer that moved, busiest first, with its rate
    And it gives the download together and the upload
    # (the chart: "the crosshair names every visible layer's rate at its column, busiest first, and the upload" in web/src/cursor.test.ts)

  Scenario: every chart on the screen points at the same moment
    Given a screen with more than one traffic chart, such as a client's live and history charts
    When the owner points at a moment on one of them
    Then the others draw a dashed line at the same moment, when it is inside their time
    # (the chart: "a time shared from another chart lands on the column that holds it, or on none" in web/src/cursor.test.ts)

  Scenario: a stretch dragged on a chart becomes what the whole screen shows
    Given a traffic chart on the overview, a client, or an app
    When the owner drags across a stretch of it
    Then the address names that stretch, so Back returns to the whole period and a link carries it
    And the chart and every list on the screen are read for that stretch only
    And the stretch is drawn at the step of the shortest period that holds it
    And the header shows the stretch with a button back to the whole period
    # -> TestASpanReadsOnlyItsStretch
    # -> TestAWiderSpanTakesTheStepOfTheRangeThatHoldsIt
    # (the address: "a stretch is read from the address, and nothing else is one",
    #  "a stretch is written into the address beside what is there, and taken out again",
    #  "the API is asked for the stretch when there is one, otherwise the period",
    #  "a stretch is drawn at the step of the shortest period that holds it, as the router reads it",
    #  "a brushed stretch runs from the first column it covers to the end of the last" in web/src/span.test.ts;
    #  "a stretch picked in the past ends its axis at its last step, not at now" in web/src/geometry.test.ts)

  Scenario: a stretch the router cannot answer is refused
    Given a request for a stretch
    When from or to is missing or not a time, the stretch is empty or backwards, starts more than 30 days ago or lies wholly in the future
    Then the answer is a 400 that says what from and to must be, and the history is not read
    And a stretch that runs past now ends at now
    # -> TestASpanMustBeAStretchOfTheLastMonth

  Scenario: the legend hides, shows and isolates layers
    Given a chart with several layers and its legend
    When the owner clicks a layer's colour, it is hidden or shown again
    And a double click shows that layer alone, and a second double click shows them all
    And pointing at an entry fades the other layers
    And the last visible layer is never hidden by a click
    # (the legend: "a legend click hides or shows one layer, a double click shows that layer alone or all again" in web/src/cursor.test.ts)

  Scenario: alerts are marked where they happened
    Given alerts raised inside the chart's time, for the house or for this client
    When the owner looks at the chart
    Then each alert is a mark on the time axis at its moment, and pointing at it reads the alert
    # (the marks: "events are marked where they happened on the time axis, and only those inside it" in web/src/cursor.test.ts)

  Scenario: the busiest moment can be opened with one click
    Given a chart that moved anything
    When the owner presses "What made this spike?"
    Then the screen shows the busiest step of the chart as its stretch, with who and what moved in it
    # (the spike: "the spike is the busiest column, the first of equals, none on a quiet chart" in web/src/cursor.test.ts)

  Scenario: the owner can ask the router's agent about a stretch
    Given a stretch picked on a chart, by a session that may change things
    And Hermes on the router has its questions turned on
    When the owner presses "Ask the router's agent" and asks
    Then the agent gets the question and a short summary the router computed of the stretch, never its key
    And the answer opens in a card that says a model wrote it, what it cost and what is left
    # -> TestAQuestionCarriesASummaryOfTheStretch
    # -> TestAQuestionCarriesTheKeyAndNothingButTheQuestionAndContext
    # -> TestTheKeyIsReadForEveryQuestion
    # -> TestAQuestionIsHeldToTheContractBeforeItIsSent
    # -> TestAQuestionNeedsAStretch
    # -> TestAViewerCannotAskTheAgent
    # (the page: "an answer says it is the model's, what it cost and what is left" in web/src/ask.test.ts)

  Scenario: a question the agent refuses, or cannot take, says why
    Given the agent refuses (busy, the hour's questions used, the day's budget spent, a timeout, a failure) or is not enabled
    Then the card shows the agent's own words, and when to try again where it says
    And with the questions off or the agent not running the chart says the agent is not enabled on this router
    # -> TestEveryRefusalIsPassedOnInTheAgentsWords
    # -> TestARefusalOfTheAgentReachesThePageInItsWords
    # -> TestARefusedConnectionIsAnAgentNotEnabled
    # -> TestTheAgentIsOffUnlessHermesTurnsItOn
    # -> TestTheAgentSaysWhetherItCanBeAsked
    # (the page: "a refusal says when to try again, in minutes past a minute" in web/src/ask.test.ts)
