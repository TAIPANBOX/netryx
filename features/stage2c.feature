# The console, stage 2c, from the request of 2026-11-20: a device such as a
# camera can be kept away from the rest of the house, a device never seen
# before gets the internet and nothing of the house until it is approved, and
# the owner hears about a new device, a try towards a blocked country and an
# unusual upload, in the console and through the bot Hermes already uses.

Feature: the router keeps IoT and unknown devices away from the house and tells the owner

  Scenario: an isolated device reaches only the internet and the router's basic services
    Given a device marked isolated
    When it tries the router's web page or SSH, or another device of the house
    Then it is dropped, and its DNS, DHCP, NTP and IPv6 neighbour discovery still work
    # -> TestAnIsolatedDeviceReachesOnlyTheRoutersServices
    # -> TestTheBridgeTableDropsFramesBetweenAnIsolatedDeviceAndTheHouse
    # -> TestTheRulesKeepIsolatedDevicesFromEveryHouseNetwork
    # -> TestAChangeOfTheHouseNetworksRecompiles

  Scenario: isolation is a security rule
    Given an isolated device with a live allowance
    When the rules are compiled
    Then the isolation comes before every allowance and holds without netryx after a reboot
    # -> TestIsolationComesBeforeEveryAllowance
    # -> TestIsolationIsInTheBootRules

  Scenario: the console says how far the isolation reaches
    Given an isolated device on Wi-Fi or on a hardware switch port
    When the owner opens its card
    Then it says whether its Wi-Fi neighbours or wired neighbours are still reachable, and what to do about it
    # -> TestAWiFiDeviceSaysWhetherItsSSIDIsolatesClients
    # -> TestAWiredDeviceOnAHardwareSwitchSaysWhatIsNotCovered
    # -> TestASoftwareBridgePortIsFullyIsolated
    # -> TestTheAPISeesEachIsolatedOrHeldDevice
    # -> TestAPortOnAHardwareSwitchIsToldApart
    # -> TestSeveralDevicesBehindOnePortAreSaidNotToBeIsolatedFromEachOther
    # -> TestTheAPICountsDevicesBehindOnePort
    # -> TestTheConsoleSaysHeldOnlyWhileTheKernelHolds
    # -> TestAListedDeviceTheKernelDoesNotHoldIsNotShownHeld

  Scenario: the devices of the house on the day this is installed are approved
    Given the router knows some devices when quarantine first runs
    When those devices, or ones asleep that day, come back
    Then none of them is held
    # -> TestTheDevicesKnownAtInstallAreApproved
    # -> TestADeviceAsleepOnInstallDayIsNotQuarantined
    # -> TestQuarantineStartsWhenTheGraceEnds
    # -> TestTheQuarantineSeesTheInventoryAndTheWeeksHistory
    # -> TestTheHistoryFeedsTheBaselineAndNeverTheTicks
    # -> TestADeviceNamedInTheConfigIsInTheBaseline
    # -> TestLabelledDevicesAreKnown

  Scenario: a device never seen before is held from its first frame
    Given the quarantine is on and its first day is over
    When a device with an unknown MAC joins
    Then it gets the internet and nothing of the house, appears in New, and the owner is told once
    # -> TestQuarantineHoldsAnUnknownSourceFromItsFirstFrame
    # -> TestADeviceNeverSeenBeforeIsQuarantined
    # -> TestANewDeviceRaisesOneEvent
    # -> TestTheNewGroupIsBuiltInAndReadOnly
    # -> TestANewcomerWithNoTimeIsStillListed
    # -> TestTheQuarantineSeesOnlyTheHouse
    # -> TestAPendingDeviceGoneForAWeekDropsOff

  Scenario: one tap approves a held device
    Given a device in quarantine
    When the owner approves it, with or without a group
    Then it reaches the house and leaves New
    # -> TestAnApprovedDeviceLeavesQuarantine
    # -> TestAnApprovedMACIsNeverPending
    # -> TestADeviceCanBeIsolatedAndApproved
    # -> TestTheDeviceInUseCannotIsolateItself

  Scenario: a broken memory never locks the house out, nor lets a held device in
    Given the list of known devices is damaged, or a flood of made-up MACs arrives
    When netryx starts or looks again
    Then the house keeps its access, a held device stays held, the list stays bounded and the owner is told once
    # -> TestADamagedBaselineNeverQuarantinesTheHouse
    # -> TestARebuiltBaselineNeverApprovesAHeldDevice
    # -> TestAFloodOfNewMACsIsBounded
    # -> TestAnEmptyApprovedSetNeverHoldsTheHouse
    # -> TestATunnelIsNeverQuarantined
    # -> TestAHeldDeviceStaysHeldThroughARebuiltBaseline
    # -> TestAnUnreadableBaselineIsTriedAgainAndTold
    # -> TestADamagedBaselineThatCannotBeWrittenIsToldAsOff
    # -> TestTheBaselineWaitsForTheClock
    # -> TestAGoodBaselineIsReadBeforeTheClockIsSet
    # -> TestAClockSetBackNeverReopensTheGrace
    # -> TestAFloodOfNewcomersIsToldOnce
    # -> TestAFullListMakesRoomForANewDevice

  Scenario: a phone with a new address is named as a hint, not a fact
    Given a held device whose randomised MAC carries the hostname of a known phone that is away
    When the owner looks at it
    Then the console says it may be that phone, from the name the device gives itself
    # -> TestARotatingMACNamesTheLikelyDevice
    # -> TestALikelySameDeviceIsNamedOnlyWhenItCouldBeTheSame

  Scenario: a try towards a blocked country is told once a day
    Given a country rule and that alert on
    When a device tries that country again and again
    Then the owner hears once that day which device tried which country
    # -> TestACountryRuleLogsTriesOnlyWithAlertsOn
    # -> TestATryCopyIsHandedOverAndNotCounted
    # -> TestATryNamesTheDeviceAndTheCountry
    # -> TestEachKindOfAlertIsRateLimited
    # -> TestTheRulesCopyTriesOnlyWhileTheCaptureReads
    # -> TestTheEngineSaysWhichAlertsAreOn

  Scenario: an unusual upload is told
    Given a device with a week of history
    When it uploads in an hour more than four times its usual maximum and at least 200 MB
    Then the owner is told once that day
    # -> TestAnUnusualUploadNeedsFourTimesTheWeeksMaxAndTwoHundredMB
    # -> TestUnusualUploadsAreFoundInTheHistory

  Scenario: alerts reach the owner through Hermes's bot, and only the owner
    Given Hermes's Telegram bot with a token only root can read
    When an alert is raised
    Then netryx sends it to the owner's chat with sendMessage only, and the token never shows anywhere
    # -> TestOnlySendMessageIsCalledWithTheOwnersChat
    # -> TestAnErrorNeverCarriesTheToken
    # -> TestATokenFileOthersCanReadDisablesTelegram
    # -> TestSeveralAllowedUsersNeedAHomeChannel

  Scenario: when Telegram cannot be used the console still has every alert
    Given no Hermes, a dead Telegram, or a flood of alerts
    When alerts are raised
    Then they stay in the console with the reason, the serious ones go first, and the engine is never held
    # -> TestWithoutHermesAlertsStayInTheConsole
    # -> TestAlertsAreDeliveredOrMarkedWithWhy
    # -> TestAFloodNeverBuriesTheSeriousAlerts
    # -> TestTelegramIsBoundedPerHour
    # -> TestAlertsSurviveARestart
    # -> TestAnUnchangedReasonIsNotWrittenAgain
    # -> TestNewDevicesNeverPushOutTheSeriousAlerts
    # -> TestASecondFloodLaterIsToldAgain

  Scenario: the agent can read the alerts and change none of this
    Given Hermes asks netryx through openwrt-mcp
    When it reads alerts or writes an agent section
    Then it reads them, and cannot silence alerts, open the quarantine, approve or isolate a device
    # -> TestCtlAlertsIsReadOnly
    # -> TestTheAgentCannotSilenceAlertsOrOpenQuarantine
    # -> TestQuarantineTurnedOffHoldsNobody
