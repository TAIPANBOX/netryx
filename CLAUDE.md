# netryx: what must stay true

Each line is an invariant and what holds it. A rule with nothing behind it is
a wish, so anything here without a `(gate:)` or `(test:)` is marked as not
enforced, and that is a debt rather than a decision.

## The promise to the household

- **Only the start of a connection is ever copied, and only when it can be
  named.** Selection is by signature, never by novelty: a TLS record opening a
  ClientHello, or a QUIC long header on 443.
  (test: `TestIngressCopiesHandshakesAndNotTraffic`)
  (@measured 2026-09-15: 9 packets and 2388 bytes of 6507 packets and 21.43 MB)
- **A flow's budget is refreshed, never recreated.** `add` leaves the timeout
  at the first packet's clock, so the entry dies under a live flow and the
  next packet opens a new budget, for ever.
  (test: same; @measured: `add` copied 32 packets of one flow, `update` 8)
- **Every set is bounded**, and so is every table this keeps in memory. The
  resolver's answers and the flows' own names are kept newest first: a repeat
  answer replaces the name, and the least recently answered goes first.
  (test: `TestTheTableCannotGrowWithoutLimit`, `TestOneFlowCannotSpendTheRoutersMemory`,
  `TestTheResolverMemoryKeepsTheNewestAnswers`, `TestTheFlowNameMemoryForgetsTheOldestFirst`)
- **Reading the resolver is a setting**, and turning it off removes both rules.
  (test: `TestTheResolverIsReadOnBothSides`)
- **The page fetches nothing from outside the router**, and a device's own name
  cannot bring markup onto it. `/` is the console page (the page itself is open
  so the login can be drawn; every API behind it needs a session), served with a CSP
  that allows only the router itself and lets no page frame it
  (`frame-ancestors 'none'`). (test: `TestUIHeadersForbidOutsideLoads`,
  `TestTheConsolePageAndItsAPIAreServed`; gate: `scripts/ui-offline.sh`; the
  old page, no longer served at `/`: `TestThePageEscapesWhatDevicesCallThemselvesAndFetchesNothing`)

## The promise about rules

- **Rules outlive the process.** The ruleset is one nft table that the init
  script and a stop never delete, and the address sets apps and countries
  learned are saved and loaded again at boot.
  (test: `TestTheScriptNeverDeletesTheTable`, `TestSetsAreSavedAndLoadedAtBoot`)
- **One transaction or none.** A reload is applied as a single nft
  transaction; a ruleset the kernel refuses leaves the previous one in force
  and the answer says so. (test: `TestReloadAppliesOneTransactionAndKeepsThePreviousOnFailure`,
  `TestARuleNftRefusedStillSaysSoInTheAnswer`)
- **A wrong rule is inactive with its reason, and the others apply.**
  (test: `TestABadRuleIsInactiveWithItsReasonAndTheRestApply`,
  `TestAMalformedAgentRuleShowsItsReasonInCtl`)
- **A country rule is never lifted by an allowance**, and an allowance lifts
  only the rule it names, for at most four hours. A client with no ethernet
  address (WireGuard, a routed link) stays matched by a rule for everyone.
  (test: `TestOrderPutsCountriesBeforeAllowances`, `TestAnAllowanceLiftsOnlyTheRuleItNames`,
  `TestAnAllowanceLongerThanFourHoursIsDropped`,
  `TestAWhoAllRuleWithAnAllowanceStillMatchesWithoutAnEtherCondition`)
- **A window belongs to the day it starts**, hours in UTC and days in the
  kernel's frame, recompiled when the clock, the zone or DST moves.
  (test: `TestAWindowAcrossMidnightBelongsToTheDayItStarts`, `TestDaysWithoutAWindowAreWholeLocalDays`,
  `TestAClockJumpRecompiles`, `TestADSTTransitionRecompiles`)
- **A cut selects by the device's hardware address** and only that device's
  own flows; the router's own connections are never cut, nor a connection
  whose far end is at home. A handshake is the device its frame came from,
  the shared answer only when the copy carries no hardware address. A flow
  the flow table has not placed yet (unnamed and inside its 10 s name wait)
  takes its remote address's country from the address table, so a country
  rule cuts it at the write, not a tick later.
  (test: `TestCutIsOnlyForTheDevicesOwnFlows`, `TestACountryRuleCutsAFlowNotYetPlacedAtTheWrite`, `TestCutEndsOneConnectionByTheSideTheDeviceIsOn`,
  `TestTheRoutersOwnHandshakeIsNeverCut`, `TestAWindowOpeningCutsWhatItNowCovers`,
  `TestANamedHandshakeToAHomeAddressIsNotCut`, `TestAHandshakeIsTheDeviceItsFrameCameFrom`)
- **A cut connection stays cut, and only that connection.** conntrack picks
  a cut connection up again on its next packet (tcp_loose), on the same
  port, so a cut by an app or category rule puts the device's hardware
  address, the remote address, the protocol (TCP or UDP only) and the
  device's own port into that rule's 10-minute set (`cut_<id>_v4/_v6`, 1024
  entries), and the rule's lines reject that flow; a new connection to the same shared front takes the
  normal path, and a device without a hardware address adds no entry. An
  allowance or the rule's window lifts it with the rule, and a change of
  what the rule blocks empties it. A cut by the console's button puts the
  same key into the owner's set (`cut_v4/_v6`, no rule's, declared by every
  ruleset and the boot copy, never pruned) before the entry is deleted, in
  its own nft transaction, so the reborn flow meets the reject (measured on
  the router with offloading off, `docs/flint2-measured.md` section 8; under
  offloading the hold is the same set, held by
  `TestAButtonCutStaysCut` and by the rule cut's measurement in section 2);
  its lines sit after the country and DoT lines and before the
  blanket allowance's return and every rule's own allowance chain
  (test: `TestTheOwnersCutsComeBeforeEveryAllowance`). A button cut netryx
  could not hold (nft refused the element, the set is full at 1024, no
  ruleset applied yet, no hardware address) is still deleted, and the answer
  says `held: false` with the reason, which the page shows in place of
  "Cut." (test: `TestACutThatIsNotHeldSaysSo`, `TestACutThatCannotBeMadeIsNotHeld`, `TestACutAnswerSaysWhetherItIsHeld`;
  the page: "a cut the router could not hold says so" in `web/src/live.test.ts`).
  For a flow the device answers (a forwarded port, the device the server),
  the device's own port is the service port, so the key keeps that remote
  address from that service port of the device, from any of its ports, for
  10 minutes; other remote addresses still reach it.
  (test: `TestACutKeepsTheDeviceFromThatAddressForTenMinutes`, `TestACutPairIsTheRebornFlowOnly`,
  `TestACutEntryIsOneFlowByProtocolAndPort`,
  `TestAChangedAppRuleEmptiesItsCutSet`, `TestAButtonCutStaysCut`,
  `TestTheCutButtonKeepsTheFlowCut`)
  (@measured the keep-alive script inlined in `docs/stage2-measured.md`
  section 4, netryx-lab
  2026-11-21: before, the entry came back after every cut and every request
  got 200; after, it never came back and the phone got 000 while the laptop
  got 200; with the port in the key, a new connection of the phone to
  facebook on the same front address was answered, 404)
- **A missing data file never opens a block.** A rule that needs the address
  table or the app map this process lacks makes the apply a refusal: the
  kernel keeps its ruleset and sets, rules.nft is not rewritten, and the rule
  says which file is missing. (test: `TestAMissingDataFileNeverOpensWhatWasBlocked`,
  `TestACountryRuleWithoutTheAddressTableSaysWhy`)
  (@measured US rule for the fakehouse tv, address table moved away and
  netryx restarted in netryx-lab, 2026-11-21: tv still 000, the 4 cc_us
  lines and the set kept, ctl `applied:false` with the reason)
- **A rule held only by names says when names cannot be seen**: with the
  capture or passive DNS off, or forward mode under flow offloading, an app
  or category rule whose apps have no organisation ranges is not applied and
  says so, naming each cause there is (capture off, passive_dns off, forward
  mode under flow offloading) with what to do about it. fw4's offload setting is read again at every reload and engine
  tick, so offloading turned on or off under the running service shows on
  the rule within one tick (15 s). (test: `TestAnAppRuleSaysItCannotSeeNamesWhenBlind`,
  `TestNamesAreSeenOnlyWithCaptureAndPassiveDNS`, `TestTheEngineLooksAgainWhetherNamesCanBeSeen`,
  `TestTheOffloadSettingIsReadAgain`, `TestTheBlindReasonNamesItsCause`)
- **An ended rule takes no place.** A rule whose until has passed counts
  toward none of the 200 and is deleted with the next write, as an ended
  allowance is. (test: `TestRulesPastTheirEndDoNotCountTowardTheLimit`,
  `TestRulesPastTheirEndAreCleanedAndNotCounted`)
- **Every rule lists its live allowances**: whose, until when, and whether
  the agent or the owner gave it, from what is in force, never from a
  config the apply refused; the page shows them on the rule and the device.
  (test: `TestStateListsTheLiveAllowancesOfEachRule`, `TestListedAllowancesAreTheOnesInForce`,
  `TestRulesListTheirLiveAllowances`;
  the page: "a rule's live allowances say until when and whose" in `web/src/rules.test.ts`)
- **A console write never races the agent's rollback**: while openwrt-mcp
  holds an unconfirmed apply of netryx, or its record cannot be read or
  parsed, a write answers 409; labels.json is
  never rewritten by a write, a UCI name only shadows it.
  (test: `TestAPendingAgentApplyHoldsTheWrite`, `TestAWriteWaitsWhileTheAgentsApplyIsPending`,
  `TestADevicePutLeavesLabelsJsonAlone`)
- **The config is read as libuci reads it**: a backslash outside single
  quotes takes the next character, so an apostrophe written as `'\''` does not
  stop the rules. (test: `TestParseReadsBackslashesAsLibuciDoes`)
- **App sets learn only from the router's own resolver**; a device's forged
  answer or name adds nothing. (test: `TestOnlyTheRoutersOwnAnswersAreTold`,
  `TestANamedHandshakeAddsOnlyWhatAnAnswerOrTheOrgVouchesFor`,
  `TestAnAnswerForAHouseOrSharedAddressFillsNothing`)
- **The agent and the console write the same UCI**, and the owner's writes
  never land in the agent's sections. (test: `TestARuleWrittenThroughTheAPIIsTheBatchTheAgentWouldWrite`,
  `TestTheOwnersWritesNeverLandInAgentSections`, `TestAgentDeviceAndGroupSectionsAreIgnored`)
- **The enforcement itself on a router** (a country stops a real connection, a
  window edge turns a rule on during a run, a rule survives a kill and a
  reboot): not enforced by a test, measured in the VM, see
  `docs/stage2-measured.md` and the live run of stage 2a. It was measured
  again on the Flint 2 (GL-MT6000, OpenWrt 25.12.6) with flow offloading off,
  with the software flowtable, and with the hardware flowtable setting on
  (Wi-Fi flows forwarded by the CPU, WED off, so no flow reached the PPE),
  with Wi-Fi clients and through one reboot, see
  `docs/flint2-measured.md` sections 2 to 4 and 8.

## The promise about time limits

- **A limit counts minutes of real use.** A minute counts for a device and an
  app when the app moved at least 64 KB for it in that minute, up and down
  together; a minute counts once it has closed, a late delta for a minute left
  behind is not counted, and only hardware addresses are counted, so an
  address or a service key never reaches the kernel's `spent_<id>` set.
  (test: `TestAMinuteCountsAtSixtyFourKilobytes`, `TestOnlyHardwareAddressesAreCounted`,
  `TestASpentKeyThatIsNotAMACNeverReachesTheScript`)
- **At the limit only that app is blocked, until the router's midnight.** A
  member whose minutes for the rule's apps reach its minutes is put in
  `spent_<id>` with a timeout to the next local midnight (by the calendar, so
  a 23 or 25 hour day is right), its flows of those apps are cut, an allowance
  for the rule lets it through, and the count restarts at local midnight.
  (test: `TestALimitBlocksAtItsMinutesAndCutsThatAppOnly`, `TestALimitBlocksOnlyTheSpentMembersUntilMidnight`,
  `TestUsageRestartsAtLocalMidnight`, `TestAnAllowanceLiftsASpentLimit`)
- **A graceful stop keeps the day's count**; a start reloads it only on the
  same local day, and a power cut gives the day's time back.
  (test: `TestUsageSurvivesAGracefulRestart`, `TestUsageIsReloadedOnlyOnTheSameDay`)
- **The console shows what blocks**: a device's usage is one row per limit
  rule with the sum of its apps' minutes, the number the block is decided on,
  and lists only the apps that were used.
  (test: `TestALimitShowsItsTotalAndTheAppsInUse`, `TestUsageIsPerDevice`;
  the page: "a limit's usage names the apps that were used" in `web/src/rules.test.ts`)

## The promise about DNS

- **Forced DNS sends the house's DNS to the router**, by the house-side
  interfaces only: a query to any address but the router's own is redirected
  to its resolver, and the router's own lookups are untouched. The chain
  `rules_dns` is declared and flushed on every apply, so turning it off
  removes it. (test: `TestForcedDNSRedirectsTheHouseAndSparesTheRouter`)
- **DoT (and DNS over QUIC on udp 853) is a security rule**, before every
  allowance. DoH and VPN are categories like any app's; `block_doh` is the
  implicit rule `f_doh`, and rule names starting with `f_` are kept for it.
  An `f_` rule sits with the security rules and no allowance lifts it. A DoH
  resolver's address is matched only on 443 and 853, so a device with 8.8.8.8
  written in it keeps plain DNS when forced DNS is off.
  (test: `TestDoTIsRejectedAsASecurityRule`, `TestBlockDoHIsAnImplicitRule`,
  `TestVPNPortsAndDoHAddressesShareTheRulesCounter`, `TestNamesStartingWithFAreKeptForFilters`,
  `TestNoAllowanceLiftsTheDoHFilter`, `TestBlockingDoHByAddressLeavesPlainDNSAlone`)
- **A filtered group is sent to the safe resolver only while it answers.** The
  safe resolver is netryx's own dnsmasq instance `dhcp.netryx_safe` on port
  553 (below 1024: only root can take it): netryx writes only that section,
  as uncommitted uci changes that the dnsmasq init reads (never a commit, so
  nothing reaches flash and nobody else's pending dhcp change, such as
  adblock-fast's, is committed with it), always with one
  `no-dhcp-interface` line per device it listens on (without it the instance
  takes the house's DHCP pools), restarts and stops only that instance, never
  the main resolver, answers no HTTPS or SVCB records there (their address
  hints would walk around the safe hosts), and is healthy only while its probe
  answers and every safe host has an address. Otherwise filtered devices keep
  the main resolver and the console says why. A reboot forgets the section,
  so rules.nft, loaded at boot, never redirects to it. The section never
  becomes the router's own resolver (`localuse 0`), keeps the house's names
  (`rebind_protection 0`, its upstream is the main resolver), sends every
  Google search domain to SafeSearch and answers iCloud Private Relay's names
  NXDOMAIN; while a device is sent there it also loses DoT and DoH (the
  implicit rule `f_dohf`). A dead probe writes the section again before the
  restart, since anyone's `uci revert dhcp` takes it away. The resolver is
  looked at by its own goroutine, each safe host lookup bounded to 3 s, so it
  never holds the engine's apply.
  (test: `TestSafeDNSWritesOnlyItsOwnSectionAndRestartsOnlyItsInstance`,
  `TestEveryGoogleSearchDomainIsSafe`, `TestPrivateRelayIsTurnedOff`,
  `TestFilteredDevicesCannotWalkAroundTheFilter`, `TestASectionRemovedByOthersIsWrittenAgain`,
  `TestTurningOffStopsAnInstanceThatNeverAnswered`, `TestTheSafeResolverNeverHoldsTheEngine`,
  `TestEachSafeHostLookupHasAShortDeadline`,
  `TestPendingDHCPChangesAreNotCommittedForSomeoneElse`, `TestTheBootRulesNeverSendToTheSafeResolver`,
  `TestADeadInstanceIsRestarted`, `TestWithoutSafeAddressesTheResolverIsNotHealthy`,
  `TestFilteredMembersGoToTheSafeResolverOnlyWhileItAnswers`, `TestTheFilteredResolverIsUsedOnlyWhileHealthy`;
  the inherited DHCP pool and the redirect measured in the VM, docs/stage2b-measured.md)
- **The safe resolver's answers name traffic like the main one's**: the
  capture copies answers from port 53 and 553 on the router's output hook, and
  believes them only by that rule's prefix. (test: `TestTheSafeResolversAnswersAreTold`)
- **The filters are the owner's.** Only the section named `filters` is read,
  so an agent's section cannot turn them off.
  (test: `TestTheAgentCannotWriteFilters`, `TestFiltersAreWrittenAsTheirOwnSection`)
- **adblock-fast is switched, never installed**: only a source's `enabled`
  (and adblock-fast's own `enabled` when a source is turned on), addressed by
  its place and refused when its url there changed or pending changes exist;
  its restart, which restarts the router's DNS, runs in the background and the
  console says so. (test: `TestAdblockSourcesAreReadAndSwitchedByPosition`, `TestAdblockWritesOnlyEnabledOnASource`)

## The promise about isolation

- **An isolated device reaches only the internet and the router's DNS
  (53, and 553 after a redirect), DHCP, NTP and IPv6 neighbour discovery.**
  The inet table drops the rest of what it sends to the router (chain
  `iso_in`) and to every house network (`iso_fwd`, from the networks
  `IsLocal` uses, the private ranges when they cannot be read, recompiled
  when they change); a bridge table `bridge netryx_iso` drops frames between
  it and any other device on the house bridges, where frames never reach the
  inet hooks. A bridge port's `isolated` flag is not used: it isolates a port
  only from other isolated ports (measured).
  (test: `TestAnIsolatedDeviceReachesOnlyTheRoutersServices`,
  `TestTheBridgeTableDropsFramesBetweenAnIsolatedDeviceAndTheHouse`,
  `TestTheRulesKeepIsolatedDevicesFromEveryHouseNetwork`, `TestAChangeOfTheHouseNetworksRecompiles`;
  measured docs/stage2c-measured.md sections 1 to 3, and on the Flint 2 with
  real Wi-Fi clients and the switch ports, docs/flint2-measured.md section 3)
- **Isolation is a security rule**: before every allowance, never lifted, in
  the boot copy, its chains always declared and flushed so turning it off
  removes it, the set bounded at 4096.
  (test: `TestIsolationComesBeforeEveryAllowance`, `TestIsolationIsInTheBootRules`,
  `TestNoIsolatedDeviceMeansNoIsolationLines`, `TestTheIsolatedSetIsBounded`)
- **The console says how far it reaches**: a Wi-Fi device is isolated from
  its SSID neighbours only when every wifi-iface of that SSID isolates clients
  (read from `/etc/config/wireless`); a wired device on a hardware switch port
  (one with a `phys_switch_id`) is said not to be isolated from its wired
  neighbours; several devices learned on one wired port (an unmanaged switch,
  a second access point) are said not to be isolated from each other.
  netryx never changes the Wi-Fi or the switch.
  (test: `TestAWiFiDeviceSaysWhetherItsSSIDIsolatesClients`,
  `TestAWiredDeviceOnAHardwareSwitchSaysWhatIsNotCovered`, `TestASoftwareBridgePortIsFullyIsolated`,
  `TestAPortOnAHardwareSwitchIsToldApart`, `TestTheAPISeesEachIsolatedOrHeldDevice`,
  `TestSeveralDevicesBehindOnePortAreSaidNotToBeIsolatedFromEachOther`, `TestTheAPICountsDevicesBehindOnePort`)

## The promise about quarantine

- **A device never seen before is held from its first frame**: every
  Ethernet source on the house interfaces that is not in `approved` (the
  baseline plus `approved '1'` devices) gets the isolation above, in both
  tables; no set grows with newcomers. A tunnel (no Ethernet header) is never
  held, and an empty approved set never holds anyone.
  (test: `TestQuarantineHoldsAnUnknownSourceFromItsFirstFrame`, `TestATunnelIsNeverQuarantined`,
  `TestAnEmptyApprovedSetNeverHoldsTheHouse`, `TestQuarantineHoldsInTheBootRules`;
  measured docs/stage2c-measured.md section 3, and on the Flint 2 with a
  newcomer on real Wi-Fi and through a reboot, docs/flint2-measured.md section 4)
- **The house of install day is approved**: the baseline is the inventory,
  the week's history, the devices named in the config and in `labels.json`
  when quarantine first runs, and for 24 hours a device
  seen for the first time joins it; the quarantine holds from the first tick
  after that day. (test: `TestADeviceNamedInTheConfigIsInTheBaseline`, `TestLabelledDevicesAreKnown`,
  `TestTheDevicesKnownAtInstallAreApproved`,
  `TestADeviceAsleepOnInstallDayIsNotQuarantined`, `TestQuarantineStartsWhenTheGraceEnds`,
  `TestTheQuarantineSeesTheInventoryAndTheWeeksHistory`)
- **Only the house is watched**: each tick sees the inventory's devices on a
  house port or Wi-Fi or with a house address (not link-local); the week's
  history feeds only the baseline; a device with no last-seen time is seen
  now. (test: `TestTheQuarantineSeesOnlyTheHouse`, `TestTheHistoryFeedsTheBaselineAndNeverTheTicks`,
  `TestANewcomerWithNoTimeIsStillListed`; found in the live run, docs/stage2c-measured.md section 7)
- **A newcomer is listed in New and told once; one approval lets it in.**
  The console's list is bounded at 1024 (a device gone 7 days drops off) and
  an overflow is told once; New is built in and never written.
  (test: `TestADeviceNeverSeenBeforeIsQuarantined`, `TestANewDeviceRaisesOneEvent`,
  `TestAnApprovedDeviceLeavesQuarantine`, `TestAnApprovedMACIsNeverPending`,
  `TestAFloodOfNewMACsIsBounded`, `TestAPendingDeviceGoneForAWeekDropsOff`,
  `TestTheNewGroupIsBuiltInAndReadOnly`, `TestADeviceCanBeIsolatedAndApproved`)
- **A damaged baseline never locks the house out, nor lets a held device
  in**: it is rebuilt with a new 24-hour grace and told, the MACs held since
  (`held-macs.json`) are never part of a rebuild or a grace and are isolated
  by name through that grace; an unreadable or unwritable baseline is tried
  again at each tick and told as quarantine_off; no baseline is taken before
  the clock is set (2026-01-01), a good one is read whatever the clock says,
  and a clock set back never reopens the grace.
  (test: `TestADamagedBaselineNeverQuarantinesTheHouse`, `TestARebuiltBaselineNeverApprovesAHeldDevice`,
  `TestAHeldDeviceStaysHeldThroughARebuiltBaseline`, `TestAnUnreadableBaselineIsTriedAgainAndTold`,
  `TestADamagedBaselineThatCannotBeWrittenIsToldAsOff`, `TestTheBaselineWaitsForTheClock`,
  `TestAGoodBaselineIsReadBeforeTheClockIsSet`, `TestAClockSetBackNeverReopensTheGrace`)
- **The console shows held only what the kernel holds**, and says what the
  quarantine does now (holds, first day until when, or off with the reason).
  (test: `TestTheConsoleSaysHeldOnlyWhileTheKernelHolds`, `TestAListedDeviceTheKernelDoesNotHoldIsNotShownHeld`;
  the page: "the quarantine says what it does now" in `web/src/security.test.ts`)
- **A flood is one alert**: more than five newcomers in one look are one
  new_devices alert with the count (repeated at most hourly); a full list
  makes room from a device unseen for an hour.
  (test: `TestAFloodOfNewcomersIsToldOnce`, `TestAFullListMakesRoomForANewDevice`, `TestASecondFloodLaterIsToldAgain`)
- **The device in use cannot isolate itself** (409): it would lose the
  console, LuCI and SSH. A new phone that is the owner's only device cannot
  approve itself either: approve it from another device or turn the
  quarantine off. (test: `TestTheDeviceInUseCannotIsolateItself`)
- **A likely-same device is a hint**: named only for a randomised MAC never
  seen together with the known device of its hostname, and shown as coming
  from the name the device gives itself.
  (test: `TestARotatingMACNamesTheLikelyDevice`, `TestALikelySameDeviceIsNamedOnlyWhenItCouldBeTheSame`;
  the page: "a quarantined device with a known hostname names the likely device" in `web/src/security.test.ts`)
- **The agent cannot open it**: only the sections named `alerts` and
  `quarantine` are read, and agent device sections are ignored, so the agent
  cannot silence alerts, open the quarantine, approve or isolate a device; it
  reads alerts with `netryx ctl alerts`.
  (test: `TestTheAgentCannotSilenceAlertsOrOpenQuarantine`, `TestCtlAlertsIsReadOnly`)

## The promise about alerts

- **Each kind is told at most so often**: a new device once ever, a
  blocked-country try once per device and country a local day, an unusual
  upload once per device a day; at most 200 alerts kept, surviving a restart.
  (test: `TestEachKindOfAlertIsRateLimited`, `TestTheAlertStoreIsBounded`, `TestAlertsSurviveARestart`)
- **A try towards a blocked country is copied by the kernel**, rate limited,
  to the capture's log group on a line of its own before the reject, only
  while that alert is on and the capture reads; the collector hands it over
  and counts it as nothing else.
  (test: `TestACountryRuleLogsTriesOnlyWithAlertsOn`, `TestATryCopyIsHandedOverAndNotCounted`,
  `TestATryNamesTheDeviceAndTheCountry`, `TestTheRulesCopyTriesOnlyWhileTheCaptureReads`;
  measured docs/stage2c-measured.md section 4, and on the Flint 2,
  docs/flint2-measured.md sections 2 and 5)
- **An unusual upload is more than four times the week's hourly maximum and
  at least 200 MB in the hour that just closed**, for a device with a day of
  history. (test: `TestAnUnusualUploadNeedsFourTimesTheWeeksMaxAndTwoHundredMB`,
  `TestUnusualUploadsAreFoundInTheHistory`)
- **Telegram goes through Hermes's bot to the owner only**: Hermes's settings
  are read from `/etc/config/hermes`, where its package installs them
  (test: `TestTheBotIsReadFromTheFileThePackageInstalls`); the token is read
  from Hermes's `token_file` only when sending, refused if anyone but root can
  read it, never in an error or a log; only `sendMessage` is called, to
  `home_channel` or the single allowed user; at most 20 messages an hour.
  (test: `TestOnlySendMessageIsCalledWithTheOwnersChat`, `TestAnErrorNeverCarriesTheToken`,
  `TestATokenFileOthersCanReadDisablesTelegram`, `TestSeveralAllowedUsersNeedAHomeChannel`,
  `TestTelegramIsBoundedPerHour`;
  sent through the real test bot on the Flint 2, docs/flint2-measured.md section 5)
- **With the console chosen, the alerts say whether Hermes's bot was found**,
  so the owner knows what choosing Telegram would do.
  (test: `TestTheConsoleChannelSaysWhetherTheBotWasFound`)
- **An alert Telegram cannot take stays in the console with the reason**:
  sending runs on its own goroutine with a 10 s deadline, a failure is tried
  again after 10 minutes, three times; the serious kinds go first, a local
  refusal is not a try, new-device alerts are the first to go when the list
  is full, and an unchanged reason writes nothing to flash. (test: `TestWithoutHermesAlertsStayInTheConsole`,
  `TestAlertsAreDeliveredOrMarkedWithWhy`, `TestAFloodNeverBuriesTheSeriousAlerts`,
  `TestAnUnchangedReasonIsNotWrittenAgain`, `TestNewDevicesNeverPushOutTheSeriousAlerts`;
  the page: "Telegram's problem is shown beside the alerts" in `web/src/security.test.ts`)
- **Nothing is sent, and no upload alert is raised, before the owner's
  settings are read**: the delivery rounds and the hourly upload check wait
  until the engine has read the config once, so the defaults (channel
  telegram) never reach a configured bot. (test: `TestNoAlertIsSentBeforeTheSettingsAreRead`,
  `TestNoUploadAlertIsRaisedBeforeTheSettingsAreRead`, `TestTheAlertSettingsAreInForceOnlyOnceRead`)

## The promise about login

- **Every view needs a session**; only the login form is open: `/`, `/app.js`,
  `/style.css`, the icons, and exactly the chunks the page's script imports
  statically, which `web/build.sh` lists in `first-screen.txt` beside the
  page (a chunk loaded on demand stays closed), so the form loads before
  anyone signs in. (test: `TestEveryViewNeedsALogin`, `TestOnlyTheLoginFormIsOpen`,
  `TestTheLoginFormsOwnChunksAreOpen`, `TestTheFirstScreensChunksAreReadFromBesideThePage`,
  `TestTheLoginFormLoadsWithoutASession`; gate: `scripts/login-assets.sh`, run by
  `web/build.sh`, with its cases in `scripts/gates-have-teeth.sh`; the first open
  in a browser: `scripts/lab/login-first-open.mjs`)
- **Writes need the grant and the session's token**, and a write route is
  reachable only through that gate.
  (test: `TestAWriteNeedsTheGrantAndTheToken`, `TestAWriteRouteIsReachableOnlyThroughTheGate`)
- **The cookie never carries the rpcd session**, only an opaque handle, and a
  slow or failed login leaves no session. (test: `TestTheCookieIsNotTheRPCSession`,
  `TestASlowLoginLeavesNoSession`, `TestAnRPCFailureIsNeverALogin`)
- **Sessions survive a restart of netryx, in RAM only**: the table of
  handles and the CSRF secret are kept in `/tmp/netryx/sessions.json` (gone
  with a reboot), written whole (a temporary file renamed over it), 0600 in a
  0700 directory; read back only when the file and its directory belong to
  netryx's own uid and no one else may write them (nor read the file), and
  the file is plain, whole and well formed, otherwise ignored with the reason
  logged. A logout and a session rpcd has let go leave the file, written in
  the same critical section as the table; a write that fails removes the file
  (fail closed), but only from netryx's own directory, never through a link or
  in a directory others can write; a handle twice loads once; rpcd still decides on every
  request. (test: `TestASessionSurvivesARestart`, `TestAFailedWriteLeavesNoStaleSessions`,
  `TestAFailedWriteRemovesNothingOutsideItsOwnDirectory`, `TestAFailedWriteRemovesNothingInADirectoryOthersCanWrite`,
  `TestTheSessionsFileIsPrivateAndWrittenWhole`, `TestASessionsFileOthersCouldTouchIsNotLoaded`,
  `TestEndedSessionsLeaveTheFile`, `TestOnlyWellFormedSessionsAreLoaded`; @decided
  2026-11-22: sessions are kept across a restart of netryx)
- **A LuCI user opens the console with a code, once, and LuCI stays the only
  sign-in**: LuCI's Network console page calls the ubus object `luci.netryx`
  (rpcd plugin `/usr/libexec/rpcd/luci.netryx`, running `netryx rpcd-luci`;
  named apart from `netryx`, so a grant for `netryx.*` never reaches it; ACL
  `luci-app-netryx` grants that one call), whose only method `handoff` mints
  a 128-bit code for the caller's own rpcd session (put in by uhttpd; a
  session sent in the arguments is refused by rpcd, measured) and the user
  rpcd says it is, and to the nonce the page put in the browser's
  `netryx_handoff` cookie (Max-Age 60), so the code opens only in that browser;
  a wrong or missing nonce spends the code, and every redeem clears the
  cookie. Only the SHA-256 of the code and of the nonce are kept, as a 0600 file in
  `/tmp/netryx/luci` (0700), at most 32 pending; it is good for 30 s after it
  was made and not before, for one redeem (the rename takes it), never
  logged, and taken only as JSON by `POST /api/login/luci`, limited as logins
  are. The console then asks rpcd that the session is alive and may view; a
  session it opens this way is lent: signing out or being pushed out never
  ends LuCI's session, and it stays lent across a restart. The page reads the
  code from the fragment only (`#luci=<32 hex>`) and replaces the address and
  its history entry before redeeming. A console on loopback only gets no code.
  (test: `TestASignedInLuCIUserOpensTheConsoleWithACode`, `TestACodeOpensOneSessionOnly`,
  `TestAnExpiredCodeOpensNothing`, `TestACodeForADeadOrForeignSessionOpensNothing`,
  `TestACodeForAUserWithoutTheViewGrantOpensNothing`, `TestAHandedOffViewerCannotWrite`,
  `TestLeavingTheConsoleKeepsTheLuCISession`, `TestTheCodeIsNeverLoggedAndOnlyTakenAsJSON`,
  `TestHandoffRedeemsAreRateLimited`, `TestALentSessionStaysLentAcrossARestart`,
  `TestACodeSentToAnotherBrowserOpensNothing`, the
  `internal/handoff` tests, `TestAHandoffIsMintedForTheCallersOwnSession`,
  `TestAHandoffWithoutAGoodSessionMintsNothing`, `TestAHandoffSaysWhenTheConsoleIsNotReachable`;
  the page: "a code from LuCI is read from the address fragment only" in `web/src/api.test.ts`;
  gate: `scripts/package-sane.sh` requires the plugin (0755), the ACL as shipped, the menu
  entry and the page; @measured netryx-lab 2026-11-22: LuCI login, the menu, the code
  redeemed, the address left at `#/`, the same code 401, LuCI's session alive after the
  console's logout; @decided 2026-11-22: LuCI stays the only authorisation, no second login)
- **On a house-side address the console speaks https**: with uhttpd's
  certificate when both files exist, otherwise with its own self-signed
  ECDSA P-256 certificate made once in `/etc/netryx` (key 0600, kept across
  restarts and sysupgrades). One port answers both: a TLS handshake is the
  console; plain http from loopback (the SSH tunnel) is served as it is; plain
  http from the house gets a 308 to https on the same port, for the router's
  own names only (`http_redirect '0'`: a 400 saying to use https); a
  connection silent for 5 s is let go; an accept error short of the listener
  closing (out of descriptors) is a pause, never the end of the port. No HSTS. Serve writes how it listens to
  `/tmp/netryx/console.json` for LuCI's page.
  (test: `TestWithoutUhttpdsPairTheConsoleMakesItsOwn`, `TestUhttpdsPairIsPreferred`,
  `TestTheLoopbackTunnelStaysPlain`, `TestPlainHTTPOnTheHTTPSPortIsSentToHTTPS`,
  `TestASilentConnectionIsLetGo`, `TestRunningOutOfDescriptorsDoesNotCloseThePort`; @measured netryx-lab 2026-11-22: own certificate
  made, https on :8788, plain http from the router 200, to the LAN address 308, a
  foreign Host 421)
- **A session nobody uses ends**: the page sends the seconds since a person
  last used it (`X-Netryx-Activity`: mouse, key, focus, the tab shown) with
  every request, a change counts as use only with the session's token, and a session with no use for
  LuCI's session time (`IdleLimit`) is ended before rpcd is asked anything,
  answering 401 `idle`: netryx's own rpcd session is destroyed, a lent one
  only let go, so LuCI's timeout ends it once nothing touches it. Polling
  alone moves nothing, the last use is kept across a restart, and use reported
  after the limit revives nothing. (test: `TestPollingAloneDoesNotKeepASessionAlive`,
  `TestActivityKeepsTheSessionAlive`, `TestActivityAfterTheLimitDoesNotReviveTheSession`,
  `TestAWriteIsActivity`, `TestAChangeWithoutTheTokenIsNotActivity`, `TestAnIdleLentSessionIsLetGoNotEnded`, `TestIdleTimeSurvivesARestart`;
  the page: "each request says how long ago a person last used the page" in `web/src/api.test.ts`)
- **A session lasts as long as LuCI's**: the login asks rpcd for
  `luci.sauth.sessiontime` (an hour when unset or not between five minutes and
  a day), not rpcd's five minutes. (test: `TestASessionLastsTheSessionTimeAsked`,
  `TestTheSessionsLastAsLongAsLuCIs`)
- **Logins are limited**: per address in the house, per /64 outside, in a
  bounded table. (test: `TestLoginIsRateLimited`, `TestHouseIPv6LoginsAreCountedPerAddress`,
  `TestAnOutsideIPv6NetworkSharesItsAttempts`, `TestTheLoginTableIsBounded`)
- **Without rpcd the console reads from loopback and changes nothing**, and
  the house gets no door. (test: `TestWithoutRPCLoopbackReadsAndNothingChanges`,
  `TestNoRPCKeepsTheConsoleOnLoopback`)
- **Metrics leave loopback only with the token**, and the token is never on the
  command line. (test: `TestMetricsAreOpenOnLoopbackOnly`, `TestTheInitCommandLineIsParsedAsWritten`)
- **The agent reads over a root-only socket that serves no write.**
  (test: `TestTheSocketIsRootOnly`, `TestTheSocketServesNoWriteRoute`)
- **The agent reads over ubus, and only reads**: the rpcd plugin
  `/usr/libexec/rpcd/netryx` registers the object `netryx` with the six reads
  of `netryx ctl` (status, devices, rules, alerts, apps and usage, the last
  two taking only a MAC) and nothing else; anything else is an error object
  before the socket is asked, a service that cannot answer is an error object,
  never an empty answer, and the console's rpcd ACL grants no call of the
  object. The read grant is openwrt-mcp's `ubus_call` with `netryx.*`, never
  `exec`, which the hermes-agent package never grants its client. `devices`
  and `rules` come keyed by MAC and by id, since openwrt-mcp cuts every list
  of an answer over 8 KiB to 16 entries and keeps every key of an object; an
  answer over 256 KiB is an error object, never a message ubusd drops.
  (test: `TestTheUbusObjectOffersTheSixReadsOnly`, `TestAUbusCallAnswersWhatCtlReads`,
  `TestTheDevicesAndRulesComeKeyedSoOpenwrtMcpCutsNoRow`, `TestAnAnswerLargerThanUbusCarriesIsAnError`,
  `TestAUbusCallRefusesAnythingElseBeforeAsking`, `TestAUbusCallSaysWhenTheServiceCannotAnswer`,
  `TestTheConsolesACLGrantsNoCallOfTheUbusObject`; gate: `scripts/package-sane.sh`
  requires the plugin, mode 0755, running `netryx rpcd`)
- **The console shows the router's time.** (test: `TestTheSessionCarriesTheRoutersZone`,
  `times are in the zone the router names` in `web/src/format.test.ts`)
- **The login says when the password would cross the house unencrypted**
  (the page served over http). (the page: "the login warns when the password
  would cross the house network in the clear" in `web/src/rules.test.ts`)

## The promise to the operator

- **A run leaves the router as it found it**: both tables removed on every exit
  path, including a receive error. (test: `TestRemoveTakesBothHooksAway`, `TestLostCopiesAreNotALostRun`)
- **The monitor never goes quiet without saying so.** Lost copies are counted
  from the kernel's numbering, and a kernel that does not number them says so
  once. (test: `TestLostCopiesAreCountedExactly`, `TestTheSequenceNumberIsReadFromTheAttributeTheKernelUses`)
- **What is installed stays attached.** The kernel is asked what the chain is
  attached to, in both the forms it prints. (test: `TestAMemberThatComesBackIsWatchedAgain`, `TestTheKernelIsAskedWhatIsAttached`)
- **check asks the kernel, not itself**, dry-running the exact ruleset each
  mode would install, and its advice names the cause rather than a package.
  (test: `TestValidateChecksTheRulesetThatWillBeInstalled`, `TestTheRuleFailureSaysWhichFailureItWas`)

## The promise to whoever scrapes it

- **One series per device, never per destination.** (test: `TestPrometheusKeepsCardinalityPerDevice`)
- **A falling figure is never a counter.** Open connections are gauges;
  finished ones are counters. (test: `TestOpenConnectionVolumesAreGaugesNotCounters`)

## The promise about the console's numbers

- **A byte is counted once.** A delta is the counter minus the last one seen;
  a dump that races a Destroy, a second Destroy, and a flow first met in a
  dump add nothing that was already counted.
  (test: `TestCountersNeverCountTwice`, `TestADumpThatRacesADestroyDoesNotBringTheFlowBack`,
  `TestADestroyForAnUnseenFlowCountsItWholeOnce`, `TestAFlowFirstSeenInADumpCountsFromThere`)
- **A byte is never lost to waiting for a name.** Bytes held before a flow is
  named come out when it is named, when the wait ends, when it is destroyed,
  and when the table pushes it out. (test: `TestDeltasWaitForTheName`,
  `TestHeldBytesAreNotLostWhenAFlowEndsUnnamed`, `TestTheLiveTableIsBounded`)
- **Bytes a pushed-out flow moves while out of the table are lost, and
  counted as lost.** A full table evicts the least recently moving flow in
  constant time and journals it as evicted; if it returns in a dump it is
  counted from there, and the gap goes to `LostBytes()`, never to a delta.
  (test: `TestEvictionIsConstantTime`, `TestAnEvictedFlowIsMarkedInTheJournal`,
  `TestAnEvictedFlowThatReturnsIsCountedFromWhereItCameBack`,
  `TestADestroyForAnEvictedFlowAddsOnlyToLost`)
- **A reused key is a new flow**, named afresh and counted as a flow of its
  own, whether a NEW says so, a later dump still holds it, or its counter
  falls. (test: `TestAKeyReusedAfterDestroyIsANewFlow`,
  `TestAFinishedKeyBackInALaterDumpIsANewFlow`, `TestAFlowUnderAReusedIDIsNamedAfresh`)
- **Up is what the device sent**, whichever side opened the flow.
  (test: `TestAnInboundFlowIsCountedFromTheDevicesSide`)
- **Every flow is counted once**, even one that moved nothing.
  (test: `TestAFlowThatMovedNothingIsStillCountedOnce`)
- **Loopback is no flow**: a connection with either end on 127.0.0.0/8 or
  ::1 never reaches the rollup, a rate or the journal, so the console's own
  tunnel is not a client. Nor is one with an unspecified, limited broadcast or
  multicast end (a DHCP discover, mDNS): it has no device, and is no row.
  (test: `TestLoopbackIsNotTraffic`, `TestTheConsoleTunnelIsNotAClient`,
  `TestBroadcastAndMulticastAreNotTraffic`)
- **Traffic with both ends at home is LAN, not WAN**: it carries the country
  `LAN`, no organisation and no address lookup, and keeps its app; the WAN
  chart and the countries leave it out, a device's own views keep it.
  (test: `TestALocalFlowIsMarkedLAN`, `TestLocalTrafficIsNotWAN`, `TestLANIsNotACountry`)
- **A dump row is a reading, never a NEW**: the kernel answers a dump with
  CT_NEW messages, and the table reads every dump row as an update, so a live
  flow is named, placed and given its app as soon as it can be, not when it
  ends. (test: `TestALiveFlowCarriesItsNameAppAndCountry`)
- **Bytes belong to the device that held the address while they moved.** A
  flow's device is asked again while it is live and unknown, so bytes and
  live rows land on the hardware address as soon as the resolver knows it;
  nothing filed is translated later, and a finished flow's journal row keeps
  what was known when it ended, so an address given to another device never
  takes the old traffic. (test: `TestARateFollowsAnAddressLearnedLater`,
  `TestBytesAreFiledUnderTheDeviceThatMovedThem`, `TestAFinishedFlowIsNotGivenToTheAddressesNextOwner`)
- **A flow's rate is its own**: the bytes it moved since its own previous
  sample over the time between the two, so a flow that opened inside an
  interval is not diluted by the part it did not exist for; a flow first met,
  one that moved nothing and one that ended have rate 0.
  (test: `TestAFlowKnowsItsCurrentRate`)
- **The live ring is bounded**: one point per Sync per device with live
  flows and a known MAC (an address nobody has named gets no ring, so a flood
  of them cannot push out the device being watched), its rate per app (a flow
  waiting for its name under `unknown`), at most 150 points, each saying its
  step; at most 512 devices, the one live longest ago going first; a device
  with no live flow for 5 minutes is let go; the router and loopback are no
  device, and flows' `RouterKey` is the collector's. A point is a list of at
  most 8 apps, the busiest, the rest folded into `other` with no rate lost,
  so a full ring is at most 34864 bytes (`maxRingBytes`, counted from the
  types' sizes; app ids are the catalog's shared strings) and 512 of them at
  most 17.0 MiB. The live view reads one device under one spelling of its MAC
  for its flows, ring and rate, never the whole table, answers a known MAC
  only, with at most 50 connections, busiest first, and reports each poll.
  (test: `TestTheLiveRingKeepsFiveMinutesPerDevice`, `TestTheLiveRingIsBoundedInDevices`,
  `TestOnlyKnownDevicesGetALiveRing`, `TestAFloodOfUnknownAddressesCannotEvictAWatchedDevice`,
  `TestALivePointIsBoundedInBytes`, `TestLiveOfIsOneDevicesFlows`,
  `TestTheFlowTableAndTheCollectorNameTheRouterAlike`,
  `TestTheLiveViewIsBounded`, `TestTheLiveViewShowsAppsConnectionsAndPlaces`,
  `TestTheLiveViewReadsOneDeviceByOneKey`, `TestALivePollIsReported`)

## The promise about the console's history

- **The rollup adds up to the deltas it took.** In every ring and both
  marginals the rows together equal the sum of the deltas taken, and a flow is
  counted once however many deltas it sends. Each device's own total holds
  while its bucket is under the key cap; in a flood the excess folds into
  `other`. A delta for an hour that has closed is not taken but counted in
  `Late()`, so nothing is shown that a restart would forget.
  (test: `TestTotalsEqualTheSumOfDeltas`, `TestFlowsAreCountedOncePerFlow`,
  `TestMarginalsAreBoundedInTotalUnderAFlood`, `TestALateDeltaForAClosedHourIsCountedAsLate`)
- **A series is answered at its own step wherever that ring is complete, and
  every point says the step it covers.** A step under an hour reads the
  minutes from the store's first delta and inside their ring, a step under a
  day the hours inside theirs; before that the next ring answers at its own
  step, and the coarse bucket the finer points begin inside holds its total
  less theirs, so nothing is counted twice. The API sends each point's
  `stepSec` and the page divides by it, never by the range's usual step.
  Top lists are read from the finest ring that reaches back to them.
  (test: `TestSeriesChoosesTheFinestRingThatCovers`, `TestARestartStillAnswersTheLastHourInMinutes`,
  `TestADayAndAWeekAreAnsweredAtTheirOwnSteps`, `TestTheSeriesSaysWhichStepItUsed`;
  the page: "a point is divided by the seconds it covers" in `web/src/geometry.test.ts`)
- **Every table the rollup keeps is bounded, and bounding loses no byte.**
  An hour or day names at most 5000 keys and a minute 1000; past that a new key
  folds into its device's `other`, and past twice that into one shared
  `other`, so an hour holds at most 10001 keys and a minute 2001. A marginal
  keeps `TopPerDevice` rows a device and pushes the smallest into `other`, and
  all its rows in one bucket are held to the key cap plus one, so a flood of
  made-up MACs folds into zero rows; each distinct
  string is stored once in a table of at most four times the cap, which lets go
  of what the rings no longer hold; a minute that has closed has its map
  rebuilt at its exact size, and an hour or day that has closed, or is loaded
  closed, is packed (varint rows, its strings numbered in its own table),
  unchanged in content, and unpacked again before it takes more; a series is
  never longer than its ring; a loaded file is held to the loading store's
  bounds.
  (test: `TestKeysPastTheCapFoldIntoOther`, `TestAMinuteHoldsAtMostAThousandKeys`,
  `TestTopDomainsPerDeviceAreBounded`,
  `TestMarginalsAreBoundedInTotalUnderAFlood`, `TestStringsAreStoredOnce`,
  `TestTheInternTableIsBounded`, `TestClosingABucketCompactsWithoutChangingIt`,
  `TestAPackedBucketHoldsEverythingItsMapsHeld`, `TestSeriesIsBoundedAndRefusesNonsense`,
  `TestALoadedFileIsHeldToTheBoundsOfTheStoreThatLoadsIt`)
- **A ring forgets what it cannot hold, and counts it.** A reused slot never
  adds an old bucket to a new one. A delta whose hour has closed, which takes
  in every delta older than the rings, goes to `Late()`; `Dropped()` counts only
  a delta of a still-open hour that is older than the minutes reach (it is kept
  by the hours and days). A stamp more than a minute ahead of the clock is
  counted now instead of turning the rings. A bucket closes at exactly its end
  plus a minute, for `Add` and both saves alike.
  (test: `TestOldBucketsFallOffTheRing`, `TestALateDeltaForAClosedHourIsCountedAsLate`,
  `TestADeltaFromTheFutureLandsInTheNewestBucket`, `TestABucketClosesAMinuteAfterItEnds`)
- **For each app of a device the console knows where it went and how each
  domain was named**: the organisation marginal is keyed by app, country and
  network with its organisation, read as organisations (`asn`) or as places
  (`place`, one row per country and network), and each domain row keeps the
  source of its newest name. Version 1 files still load, without app,
  country or source. (test: `TestPlacesOfAnAppCarryCountryAndOrganisation`,
  `TestDomainsKeepHowTheyWereNamed`, `TestVersion1FilesStillLoad`,
  `TestADeltaSaysHowItsFlowWasNamed`)
- **Flash is written once per closed hour and once per closed day**, and an
  hour at most once more at a graceful stop. A bucket closes a minute after it
  ends, is written to its own file by rename, and its final file is never
  rewritten, by either save; a stop writes the hours still filling, marked
  open, a restart keeps filling them, and they are written once more, final,
  when they close. An open file the store has not read is merged into RAM
  before it is written over, never lost. The open day is never written: a
  start rebuilds it from its hours. Files the rings no longer reach are
  deleted. Callers serialise the two saves.
  (test: `TestOnlyClosedBucketsAreWrittenAndEachOnce`, `TestFilesPastRetentionAreDeleted`,
  `TestAShutdownMidHourLosesNothing`, `TestALoadedOpenHourIsNotMistakenForClosed`,
  `TestSaveOpenNeverRewritesAFinalFile`, `TestAnUnloadedOpenHourIsMergedNotOverwritten`)
  (@measured `scripts/forge.sh go test -tags heavyprofile -run TestHeavyProfile -v ./internal/rollup/`
  2026-11-20 on forge, 30 busy devices: 3406 KB written a day, 100.9 MB of heap
  with every ring full and closed buckets compacted, over the 64 MB budget; the
  sizing is decided on the real house's cardinality)
- **The history store fits its memory budget**: under the heavy profile, with
  every ring full, its live heap stays under 24 MB (`NETRYX_HEAP_BUDGET_MB`).
  (gate: `scripts/memory-budget.sh`, run on forge as
  `scripts/forge.sh sh scripts/memory-budget.sh`; @measured that command
  2026-11-23 on forge: 17.9 MB with closed hours and days packed, against
  100.9 MB when they were maps, 3406 KB written a day, unchanged; the budget
  is 24 MB, under the 64 MB of resident memory set for a 256 MB router. The
  price is on reads: on forge the heaviest, the top networks over 7 days,
  takes 69 ms against 57 ms, the top domains over 7 days 26 ms against 20)
- **History survives a restart, and one bad file never empties the store.** A
  damaged, misnamed, other-version or later-than-the-clock file is skipped and
  named, and every other file loads; a day is read from its own file only if
  that file is final, otherwise its hours rebuild it, so a power cut after a
  restart loses no part of the day. Files set aside as later than the clock
  load on a later `LoadDir` once the clock has passed them, and every file
  reaches RAM at most once, by a load or a save.
  (test: `TestHistorySurvivesARestart`, `TestOneCorruptFileDoesNotEmptyTheStore`,
  `TestAPowerCutAfterARestartLosesNoPartOfTheDay`, `TestAFileFromTheFutureIsNotLoaded`,
  `TestFilesSkippedBeforeTheClockWasSetAreLoadedLater`, `TestASecondLoadDirAddsNothingTwice`)
- **A restart never reads as a quiet house, nor as a fast one.** Minutes are
  not saved, so they answer only from the store's first delta on, whatever
  older stamps arrive; before that the hours do, at an hour's step.
  (test: `TestAfterARestartMinuteQuestionsAreAnsweredFromHours`,
  `TestTheMinutesStartWhenTheStoreDoes`, `TestARestartStillAnswersTheLastHourInMinutes`)

## The promise about the house's apps and destinations

- **Apps lists what the house used, Destinations where it went**, over the
  period picked: each app with its category, bytes and share of the house,
  each country with its share of what left the house, the networks (country,
  AS, organisation) and the domains, and for each app or country how many
  clients and which three moved the most. The house's own traffic is not a
  destination; only hardware addresses count as clients. The lists stop at 100
  apps, 100 countries, 50 networks and 50 domains, a detail at 50 clients.
  (test: `TestTheHouseAppsListNamesTheClientsOfEachApp`, `TestTheHouseAppsCountOnlyClients`,
  `TestTheHouseAppsListIsBounded`, `TestTheHouseDestinationsLeaveTheHouseOut`)
- **One app or one country is its clients, places and traffic**: an app's
  clients, countries with networks, domains and series; a country's clients,
  apps, networks and series. An id that cannot be one is a 400, an app the
  catalog does not know and the period never saw a 404, and a country is two
  letters. (test: `TestAnAppDetailShowsItsClientsPlacesDomainsAndSeries`,
  `TestAnAppNobodyKnowsIs404AndAMalformedIdIs400`,
  `TestACountryDetailShowsItsClientsAppsAndNetworks`, `TestACountryCodeMustBeTwoLetters`)
- **The clients of every row come from one pass over the history**:
  `Pairs` answers for each app or country what `Top` would answer for it
  alone, so a list never reads the history once per row.
  (test: `TestPairsAgreeWithTopForEveryKey`, `TestPairsHonourTheFilter`,
  `TestPairsRefuseDimensionsKeysDoNotCarry`)
- **A row says which rules close it**: an app is closed by an app rule
  naming it or a category rule naming its category, a country by a country
  rule naming it; each tag says for whom when not everyone and its state when
  not in force, and leads to Rules. The block button stays until an enabled
  rule for everyone closes it: one device's or a group's rule, or one turned
  off or ended, does not. (the page: "an app's rules are the ones that name it
  or its category", "a country's rules are the country rules that name it",
  "only an enabled rule for everyone closes it for the house", "a rule's tag
  says for whom and in what state" in `web/src/house.test.ts`)
- **A row names clients before anything else**, and a row with no client
  names whose traffic it is (the router, addresses without a hardware
  address); a detail counts every client even when it lists the busiest 50.
  (test: `TestTheHouseAppsCountOnlyClients`, `TestARowWithoutClientsSaysWhoseTrafficItIs`,
  `TestAnAppDetailShowsItsClientsPlacesDomainsAndSeries`)

## The promise about clickable names

- **Every name is a link to its screen**, the folded rest and the house
  itself excepted: a client's card, an app, a country, a network
  (`#/asn/<n>`), a domain (`#/domain/<name>`), a category (Apps filtered), a
  group (`#/group/<id>`), a rule (`#/rules?rule=`); a filter is in the address,
  and a link from a detail carries `from` for the breadcrumbs. (the page:
  `web/src/entity.test.ts`)
- **A network's and a domain's page** read the history in one pass over the
  marginals (`Holders`), which answers for every device, app and country what
  `Top` would answer under its own filter, so no client or app is left out; a
  domain that could not be one is a 400. (test: `TestHoldersAgreeWithTopPerDeviceAndApp`,
  `TestANetworkPageNamesItsCountriesClientsAndApps`, `TestADomainPageNamesItsAppsAndClients`)
- **The line's card reads only the line** (`/api/wan`), never the history.
  (test: `TestTheLineCardAsksOnlyForTheLine`)
- **The flows can be asked for a stretch of time and a network** (`from`,
  `to` in RFC 3339, `asn`), malformed ones refused.
  (test: `TestFlowsCanBeAskedForAStretchOfTimeAndANetwork`, `TestFlowsCanBeAskedForOneNetwork`)

## The promise about Client 360

- **A client's name opens its card beside it**: several cards can be open
  (at most six, the oldest closing first), the newer in front; opening one that
  is open brings it forward; a card is dragged by its header but never off the
  screen and closes with its X or Esc (the front one); a click with a modifier
  follows the link. (the page: `web/src/windows.test.ts`)
- **The full view at #/client/<mac>** shows identity, where it is connected
  (with the router's wiring), the connection map (device, apps, domains,
  networks, countries over the window, every name a link), now, history, rules
  and limits with where each comes from, and security; each part has its own
  empty and error state. (the page: `web/src/client360.test.ts`, `web/src/connmap.test.ts`;
  the layout itself is not enforced by a test, seen in the lab)

## The promise about calm live figures

- **Now is the average over a sliding window** (1, 3 or 10 minutes, 3 by
  default), from the rollup's minutes: `Recent` sums the minutes from
  now-window, to the minute, and divides by the time they cover, which after a
  restart starts at the store's first delta. Clients, Apps, Destinations and a
  client's apps carry it as `recent`; a window other than 1m, 3m or 10m is a 400.
  (test: `TestRecentSumsTheMinutesOfTheWindow`, `TestRecentCoversOnlyWhatTheStoreSaw`,
  `TestRecentReadsOnlyTheDimensionsKeysCarry`, `TestTheNowColumnsAreTheWindowsAverage`,
  `TestTheWindowIsOneThreeOrTenMinutes`)
- **The live flows rank by what they moved over the window**: each live flow
  keeps its bytes of the last 11 minutes (176 bytes a flow, 1.4 MB for a full
  table), and a live read with a window returns the busiest over it, with those
  bytes, as one page. (test: `TestLiveFlowsCanBeReadByRecentVolume`,
  `TestOldMinutesFallOutOfTheWindow`, `TestASlotReusedLaterHoldsNothingOld`, `TestAReusedKeyStartsItsWindowEmpty`, `TestLiveFlowsAreAskedByTheWindow`)
- **Rows hold still**: they move at most every 30 s and only when two rows
  shown in the wrong order differ by more than 12%; a new row takes its place
  at once; any column sorts either way and the choice is kept per table (the
  live flows and the journal apart); Pause stops every poll but the first after
  a screen opens or its question changes. (the page: `web/src/steady.test.ts`;
  the glide itself is not enforced by a test, measured in the lab)

## The promise about seeing things in place

- **A list row's last hour is one pass over the minutes**: `Sparks` answers
  every client's or app's hour in steps of two minutes at once, a step is
  bytes down and up together, and the steps before the store's first minute
  are said (`known`), never drawn as a quiet house; a row that moved nothing
  has its hour, all zero; the folded row has none.
  (test: `TestSparksAreEachKeysMinutesInSteps`, `TestAnEmptyStoreKnowsNoStep`,
  `TestTheListsCarryAnHourOfSparkline`; the page: "a list's hour is drawn only
  from where the router began counting" in `web/src/live.test.ts`)

- **Anything named can be found by typing**: Cmd+K, Ctrl+K or "/" outside a
  text field opens a search over the clients, apps, categories, countries,
  networks, domains, groups and rules the page can read, by name or by an
  address, MAC, hostname or AS number; the whole name first, then its start, a
  word's start, inside it, then what else identifies it; at most 12; folded
  rows and unknowns are never things. (the page: `web/src/search.test.ts`)

- **A name shows a card after 400 ms of a resting mouse**: the thing is read
  from the link's own address (a screen that is not one thing has none), its
  facts from that thing's own read over the last day, kept 30 s; a click on the
  card keeps it until its X, Esc, a click elsewhere or another screen; touch
  shows none. (the page: `web/src/hover.test.ts`; the timing itself is not
  enforced by a test, seen in the lab)

- **A thing's view says where it sits**: a chain of chips names the path the
  owner took (`from`) or the screen it belongs under, then the thing, then what
  it belongs to (an app's category, a network's countries, a domain's apps, a
  client's group), each a link; folded rows and unknowns are no parent.
  (the page: "a thing's view names what it belongs to, each a link" in
  `web/src/entity.test.ts`)

- **Two or three clients compare side by side** at `#/compare?mac=..`: at
  most three, each MAC once, nothing that is not a MAC; a fourth tick is
  refused rather than pushing one out unseen. (the page: `web/src/compare.test.ts`)

- **A person is whose a device is, and nothing more**: a `person` section
  (`name`, `list mac`) the console writes through the same gate as groups
  (grant and token), the whole section at a time; MACs are read as the model
  reads them, a device is one person's at most (a second is refused and named;
  a config written by hand with a device twice gives it to the first), at most
  32 people of 32 devices, and deleting a person leaves devices, groups and
  rules as they were. (test: `TestAPersonIsWrittenAndReadBack`,
  `TestADeviceBelongsToOnePerson`, `TestDeletingAPersonLeavesDevicesAndRules`,
  `TestAPersonSurvivesARestart`, `TestAWriteRouteIsReachableOnlyThroughTheGate`;
  @decided 2026-11-22: a person is kept in UCI, written only by the owner's action)
- **The last hour of minutes survives a restart of netryx, in RAM only**: a
  graceful stop writes it to `/tmp/netryx/minutes.json` through
  `internal/ramfile` (the same private, whole-file, owner-checked writes as
  `sessions.json`), the start reads it before any delta and takes the file
  away; only minutes inside the kept hour of the new clock are taken, none
  ahead of it, each held to the minute's key cap, and the hours take nothing
  from it; a reboot empties it and the lists say since when they count.
  (test: `TestTheMinutesSurviveARestart`, `TestLoadedMinutesKeepToTheRingAndTheClock`,
  `TestALoadedMinuteIsHeldToTheKeyCap`, `TestTheMinutesAreKeptInRAMAcrossARestart`;
  the file checks: the `internal/auth` session store tests, now over `internal/ramfile`)
- **Compare's Now is the window's average**, with the window switch, right now
  beside it; its busiest app, group and countries are links. (the page:
  "the comparison lines up what tells devices apart, each name a link" in
  `web/src/compare.test.ts`)

## The promise about the charts

- **A stretch picked on a chart is what the whole screen reads**: the address
  carries `span=<from>~<to>` (whole RFC 3339 UTC times, from before to), the
  API takes `from` and `to` in place of the period for the series and every
  list, reads up to just before `to`, at the step of the shortest range that
  holds the stretch, and refuses (400, the history unread) a missing half, a
  time that is not one, an empty or backwards stretch, or one starting more
  than 30 days back; a stretch past now ends at now. The live tiles keep
  judging online and new devices by now.
  (test: `TestASpanReadsOnlyItsStretch`, `TestAWiderSpanTakesTheStepOfTheRangeThatHoldsIt`,
  `TestASpanMustBeAStretchOfTheLastMonth`; the page: `web/src/span.test.ts`)
- **The mouse reads a chart without asking the router**: the crosshair names
  the visible layers that moved at that step, busiest first, with the total
  and the upload; the moment is shared with every chart of the screen, which
  draws it only inside its own time; the legend hides, shows and isolates
  layers and never hides the last; alerts are marks at their moment; "What
  made this spike?" picks the busiest step as the stretch.
  (the page: `web/src/cursor.test.ts`; the drawing itself is not enforced by a
  test, seen in the lab)

## The promise about what is new for a device

- **New is used since the start of yesterday and in none of the 29 days
  before**: the apps and countries a device used since 00:00 UTC yesterday
  (read from the hour ring) that no day bucket from the 29 days before holds
  for that device, each with the hour it was first seen and its bytes since;
  the catch-alls (`other`, Unknown, `--`) and the house's own traffic (`LAN`)
  are not news, and nothing new is written to flash. A device the router has
  known for less than seven days is too new to tell, and says so; how long it
  has known it is the earlier of the inventory's first sight, which restarts
  with the service, and the history's (`FirstSeen`), which does not.
  (test: `TestNewKeysAreWhatADeviceUsedSinceTheSplitAndNeverBefore`,
  `TestADeviceSaysWhatIsNewForIt`, `TestADeviceSeenForLessThanAWeekIsTooNewToTell`,
  `TestFirstSeenIsTheFirstHourTheHistoryHoldsForTheDevice`, `TestWhatIsNewSurvivesARestartThatResetTheInventory`;
  the page: "what is new for a device says when it cannot tell" in `web/src/client360.test.ts`)

## The promise about a device's usual week

- **Usual is the median of each hour of the router's week, from the daily
  files**: `GET /api/devices/{mac}/usual` places every counted hour of the 30
  days since the hour the device was first seen in the weekday and hour of the
  router's zone (by the hour's middle, so DST and half-hour zones place it
  right), and answers each slot's median of down and up together, null with
  fewer than two samples; an hour the router was not counting and a day with
  no file are no sample, a counted quiet hour is a zero. Fewer than seven
  counted days is too new, said so, with no grid. Beside it, the hour that
  just closed, from the history, null when the service did not see all of it;
  the page says both sides and of how many days, and a slot with too few days
  is an outline, never a quiet fill. Nothing new is written.
  (test: `TestTheUsualIsTheMedianOfEachHourOfTheWeekInTheRoutersZone`,
  `TestTheUsualStartsWhenTheDeviceWasFirstSeen`, `TestAnHourIsPlacedByItsMiddleInTheRoutersZone`,
  `TestADeviceAnswersItsUsualWeekAndTheLastHour`, `TestADeviceWithLessThanAWeekOfDaysHasNoUsualYet`,
  `TestTheUsualWeekIsReadInTheRoutersZone`; the page: "the usual says both sides" and
  "the week grid runs Monday to Sunday" in `web/src/rhythm.test.ts`)

## The promise about a device's rhythm

- **Yesterday is set against the device's own median day**: `GET
  /api/devices/{mac}/rhythm` reads the 30 days of daily files since the hour
  the device was first seen (the earlier of the inventory's and the files'),
  a day whole only when every hour of it was counted; the median of the whole
  days, yesterday's bytes and the multiple (to two places), refused (null)
  when the device has fewer than seven whole days (`too_new`), yesterday was
  not whole, or the median day is nothing; `no_data` with no counted day. The
  trend is the last seven days' mean whole day against the seven before
  (rising at a quarter more, falling at a fifth less, unknown with fewer than
  four whole days in either). Drift: yesterday's hours, in the router's zone,
  that moved at least 1 MiB while the usual week's median for that slot is
  nothing. The page puts the sentence first with both sides named, a bar a
  day with yesterday marked, a partly counted day faint and a day with
  nothing counted for the device an outline, never a quiet bar; while too new
  it shows no median day nor busiest share. Nothing new is written.
  (test: `TestRhythmComparesYesterdayWithTheMedianDay`, `TestRhythmRefusesAComparisonItCannotMake`,
  `TestRhythmTrendIsTheLastWeekAgainstTheOneBefore`, `TestRhythmFindsActivityInHoursItUsuallySleeps`,
  `TestADeviceAnswersItsRhythm`; the page: the rhythm tests in `web/src/rhythm.test.ts`)

## The promise about interventions

- **Every intervention on a device is journalled once, in one file appended
  to**: `<state>/interventions.jsonl` (0600, a file others could read made
  private when opened, kept by sysupgrade with `/etc/netryx/`), one JSON line
  each: an allowance in force (by the owner or the agent, with its rule and
  until), a limit spent (until midnight), a device let out of quarantine,
  isolated or released (the owner's, told after the engine's first apply,
  which only learns how things stand), a connection a rule cut (with what the
  rule holds and who wrote it, at most once an hour a device and rule) and a
  connection the owner cut with the button (with whether it was held). The
  same allowance until the same time and the same limit spent until the same
  midnight are one line, across restarts. The newest 1000 of the last 30
  days are kept; the file is written whole again (a temporary file renamed
  over it) only at twice that. Every line is checked when read: a damaged
  one, an unknown kind or an overlong field is skipped and counted, and a
  line a power cut tore is never glued to the next. `GET
  /api/devices/{mac}/interventions` lists a device's (and its group's) newest
  first, at most 100, with since when the journal counts.
  (test: the `internal/journal` tests, `TestTheEngineTellsTheAllowancesLimitsAndBlocksItApplies`,
  `TestTheEngineTellsApprovalsAndIsolationAfterItsFirstApply`, `TestADeviceAnswersItsInterventions`,
  `TestAButtonCutIsJournalled`, `TestTheJournalTakesWhatTheEngineApplies`;
  the page: `web/src/interventions.test.ts`; @decided 2026-11-23: an append-only
  file on flash, 1000 entries or 30 days)
- **The journal fits the flash**: under 50 interventions a day for 30 days,
  each append counted as a 4 KiB page and each rewrite at its size, it writes
  at most `NETRYX_JOURNAL_BUDGET_KB` (256) a day. (gate: `scripts/journal-budget.sh`,
  run as `scripts/forge.sh sh scripts/journal-budget.sh`; @measured that command
  2026-11-23 on forge: 200.3 KB a day)

## The promise about the daily file

- **One file per closed UTC day, written once and never again**:
  `<state>/daily/YYYY-MM-DD.day`, made from the hour ring by the first history
  tick after the day closed (a minute past midnight, the tick that writes the
  closed day of the history), through a temporary file renamed over its name.
  A day already held, or with any file under its name (a damaged one too), is
  never written; a day is taken only while no history file is set aside as
  later than the clock, only once the clock is past 2026-01-01, only if the
  hour ring still reaches its first hour, and only if the router counted at
  least one hour of it. (test: `TestADayIsWrittenOnceWhenItClosesAndNeverRewritten`,
  `TestTheDailyFileIsWrittenByTheFirstTickAfterTheDayCloses`,
  `TestTheDailyFileWaitsForHistorySetAsideAsLaterThanTheClock`,
  `TestADayIsAnsweredOnlyOnceClosedAndWholeInTheRing`)
- **What it holds**: for every hardware address of the day (the router's own
  traffic, the catch-all and unnamed addresses are not devices; the busiest
  512, with the number left out), 24 hourly totals down and up as uint32 KiB
  (the unit is in the body: 1024), rounded up so a byte is never a quiet hour
  and held at the top past 4 TiB an hour rather than wrapping; the 24-bit mask
  of the hours the hour ring holds; and, in yesterday's file only, every rule's
  hit counter (`hits_<id>`, by id) and every netifd network's rx/tx bytes as
  the Network screen reads them, with the time they were read. No domain,
  nothing per destination.
  (test: `TestADayKeepsOnlyHardwareAddressesAndAtMostTheBusiest512`,
  `TestAnHourOverFourGiBDoesNotWrap`, `TestRuleHitsAndInterfaceCountersRoundTrip`,
  `TestTheHitsAreEveryRulesCounterByID`)
- **A router switched off is never read as a quiet house**: an hour outside
  the mask is unknown, a counted quiet hour is zero, a day with no file is
  unknown, and a restart mid-day keeps every hour the ring takes back from the
  history files. (test: `TestHoursTheRouterWasOffAreNotInTheMask`,
  `TestADayWrittenAfterAMidDayRestartHoldsEveryHourTheRingHolds`)
- **A file is checked whole before it is believed**: the packfile frame
  (magic `NRXDAILY`, version 1, SHA-256 at its end), then the unit, the date
  against the name, every count against the bytes left and its bound, the
  order of devices and names, no bytes in an hour outside the mask, counters
  read after the close, nothing after the end. A damaged, cut-short,
  other-version or misnamed file is skipped and named in the log, its day
  unknown, and every other file loads; only the newest 30 are read.
  (test: `TestADamagedFileIsSkippedAndNamedAndTheRestLoad`, `TestAFileThatCannotBeADayIsRefused`)
- **A month is kept**: files of days more than 30 days before today are
  deleted, with any temporary file a power cut left, after every write; a
  clock before 2026-01-01 deletes nothing. (test: `TestFilesPastThirtyDaysAreDeleted`)
- **One read, behind the login**: `GET /api/devices/{mac}/days` answers the 30
  days before today oldest first, in bytes, an unknown day with no hours and
  an hour outside the mask as null; an unknown device is a 404, a malformed
  MAC a 400. (test: `TestADeviceAnswersItsDaysWithUnknownDaysAsUnknown`,
  `TestEveryViewNeedsALogin`)
- **The daily file fits a small router**: under the profile of 64 busy
  devices a day's file is at most `NETRYX_DAILY_BUDGET_KB` (16) and the 30
  days loaded at most `NETRYX_DAILY_HEAP_BUDGET_KB` (1024) of heap.
  (gate: `scripts/flash-budget.sh`, run as
  `FORGE_DIR=netryx-daily scripts/forge.sh sh scripts/flash-budget.sh`;
  @measured that command and
  `scripts/forge.sh go test -count=1 -tags heavyprofile -run '^TestDailyProfile$' -v ./internal/daily/`
  2026-11-23 on forge: 13305 bytes a day for 64 devices, 6573 for 30, 436.0 KB
  of heap for the 30 days loaded)

## The promise about what reaches a device

- **A client's view says what reaches it from the internet**: the enabled
  port forwards of the firewall that lead to its address, the UPnP mappings it
  opened for itself (miniupnpd's `/var/run/miniupnpd.leases`, read only, at
  most as many as forwards, descriptions without control characters), and the
  connections opened from outside to it in the last 24 hours with their
  countries (the house's own traffic left out); with none of these it says
  nothing from the internet reaches it. netryx changes no forward or mapping.
  (test: `TestUPnPMappingsAreReadFromTheLeaseFile`, `TestTheViewCarriesUPnPMappingsOrSaysThereAreNone`,
  `TestAUPnPMappingNamesItsDevice`; the page: "what reaches a device from the internet" in
  `web/src/client360.test.ts`)

## The promise about the router's agent

- **A question goes to the agent by its contract, with the router's own
  summary and nothing else**: POST `http://127.0.0.1:<hermes.api.port>/v1/ask`
  only while `hermes.api.enabled` is '1' in `/etc/config/hermes`, the key read
  from `hermes.api.key_file` for every question (a plain file, no link, nobody
  but its owner may read it, root's when netryx runs as root, at least 32
  characters), sent only in the Authorization header and never to the page, a
  log or an error. The body is the question (1 to 2000 characters) and the
  summary of the stretch the router computed (its UTC times, the busiest
  clients, apps, countries and connections with bytes), cut to 8000
  characters and to a body of 16384 bytes, never inside a character. Every
  name in it that anyone can write (a device's, a site's, a network's) is in
  quotes, at most 64 characters, with control and format characters and line
  breaks made spaces, under a first line saying quoted names are data.
  (test: the `internal/askagent` tests, `TestAQuestionCarriesASummaryOfTheStretch`,
  `TestNamesInTheSummaryCannotPassForTheRoutersOwnLines`)
- **The agent's answer and refusals reach the person in its words**: a 200
  carries the answer, its cost and what is left; every refusal of the contract
  (400, 401, 404, 405, 409, 413, 429, 502, 503, 504) comes back at status 200
  with ok false, its code, message and retry time, so the agent's 401 never
  reads as a lost console session; a refused connection, questions off or a
  key that cannot be read is "agent not enabled on this router" with why.
  (test: `TestEveryRefusalIsPassedOnInTheAgentsWords`, `TestARefusedConnectionIsAnAgentNotEnabled`,
  `TestTheAgentIsOffUnlessHermesTurnsItOn`, `TestARefusalOfTheAgentReachesThePageInItsWords`,
  `TestTheAgentSaysWhetherItCanBeAsked`, `TestAQuestionNeedsAStretch`)
- **Only a session that may write asks**: `POST /api/agent/ask` is behind the
  write grant and the CSRF token; the button shows only with a stretch picked
  and that right. (test: `TestAWriteRouteIsReachableOnlyThroughTheGate`, `TestAViewerCannotAskTheAgent`; the page:
  `web/src/ask.test.ts`; @measured netryx-lab 2026-11-23 against
  `scripts/lab/fake-hermes-api`: the answer with its footer, all eight
  refusals in the agent's words, the agent down and the questions off)

## The promise about the Network screen

- **Network shows how the router is wired, and only reads it**: netifd's
  networks (loopback left out, the one with a default route marked as the
  internet) with sysfs counters, the house bridges' wired ports (radios are
  not ports) with link, speed and the hardware switch, the devices the
  inventory learned behind each port and access point, Wi-Fi radios and
  networks from `/etc/config/wireless` and hostapd, WireGuard from the
  network config and one `wg show all dump`, and the firewall's DNAT
  redirects. No name from a config is passed to a program.
  (test: `TestInterfacesAreReadFromNetifd`, `TestTheViewIsReadFromTheRouter`,
  `TestWirelessConfigNamesRadiosAndNetworks`, `TestPortForwardsAreTheFirewallsDNATRedirects`,
  `TestWgIsAskedOnceForEveryInterface`, `TestTheNetworkViewNamesTheDevicesBehindEachPortAndAccessPoint`)
- **No secret reaches the page**: a WireGuard private or preshared key and a
  Wi-Fi key are never read into the view, though `wg` prints the first two.
  (test: `TestWireGuardPeersCarryNamesHandshakesAndTrafficButNoSecret`, `TestTheNetworkViewNeverCarriesASecret`)
- **What the router lacks is said**: no Wi-Fi radio, Wi-Fi configured but no
  access point running, no WireGuard, no wg tool, no forwards, netifd not
  answering; a console without the source answers 503.
  (test: `TestWhatTheRouterLacksIsSaid`, `TestTheNetworkViewSaysWhatTheRouterLacks`,
  `TestWithoutANetworkSourceTheViewIsUnavailable`)
- **The router is asked at most every 5 s** however often the page polls,
  and one reading is bounded to 5 s, under its own deadline: viewers asking
  at once share one reading, one who leaves stops waiting and never leaves a
  cut-short reading in the cache. (test: `TestTheViewIsCachedForAFewSeconds`,
  `TestViewersShareOneRead`, `TestAReadIsNotCutShortByTheViewerLeaving`, `TestAWaitingViewerCanLeave`)
- **A config that cannot be read is said so**, never shown as a router
  without Wi-Fi, WireGuard or forwards; a `package` line reads as libuci
  reads it. (test: `TestAConfigThatCannotBeReadIsSaidSo`, `TestPackageLinesAreRead`)
- **The internet line is the configured WAN**, up or down, or another
  network with a default route; a tunnel's default route is not.
  (test: `TestTheWANIsTheConfiguredLineAndNotATunnel`)

- **The backup line's month is read from the daily files' closes**: the
  primary line is the configured WAN and every network on its device; a
  backup line is any other network holding a default route (never a tunnel)
  or a mobile modem's (qmi, mbim, ncm, modemmanager, 3g, directip, wwan) even
  while idle, one network per device. `GET /api/lines` gives each day what
  each carried, the close's counters less the close before, unknown when a
  close is missing or was not read at the close, the router was off for part
  of the day, or a counter fell; the days the backup carried at least 32 MiB
  (more than its checks move), all it carried, its share of what the lines
  carried on those days and its busiest day. Nothing new is written.
  (test: `TestTheBackupLinesAreTheOtherUplinksAndAModemEvenWhenIdle`, `TestCountersAreEachDaysCloseOverTheWindow`,
  `TestALineCarriesTheDifferenceOfTwoCloses`, `TestTheBackupLineSaysTheDaysItCarriedAndItsShare`,
  `TestTheLinesAreServedWithTheBackupFoundInTheView`; the page: `web/src/lines.test.ts`)

## The promise about the fixed tables

- **The address table and the vendor list cost the heap nothing**: the
  package ships them as packs (`ip2asn.pack`, `oui.pack`) that `build.sh`
  makes with `tools/datapack`, which reads each back and compares it with its
  source before writing it; the service maps them read-only, never unmaps
  them, and its lookups allocate nothing. Measured live heap after opening
  both: at most `NETRYX_FIXED_BUDGET_MB` (2).
  (test: `TestTheFixedTablesFitTheirBudget`, heavyprofile, with NETRYX_DATA;
  `TestAPackLookupDoesNotAllocate`, `TestAVendorLookupDoesNotAllocate`;
  @measured `NETRYX_DATA=$HOME/netryx-geo/data go test -tags heavyprofile -run TestTheFixedTablesFitTheirBudget -v ./internal/enrich/`
  2026-11-23 on forge: 720 bytes with the packs, 26.30 MB with the text files)
- **A pack answers exactly as the text file did**, for every address and
  hardware address, and the country rules get the same ranges.
  (test: `TestThePackAnswersAsTheLoaderForEveryAddress`, `TestTheVendorPackAnswersAsTheListForEveryPrefix`,
  with NETRYX_DATA: every range's ends and their neighbours, two million random
  addresses, every country's ranges, every registration and its neighbours;
  `TestAPackAnswersAsTheTableItWasWrittenFrom`, `TestAVendorPackAnswersAsTheList`)
- **A pack is checked whole before it answers**: magic, version, the SHA-256
  at its end, every count against the file's size, every index and offset, and
  the order of its records. One that fails is refused with the reason and the
  service runs as it does without the file (no country, no maker; a country
  rule says the address table is missing). A path ending in `.pack` is never
  read as text. (test: `TestADamagedPackIsRefusedWithItsReason`,
  `TestADamagedVendorPackIsRefusedWithItsReason`, `TestADamagedDataPackDegradesLikeAMissingOne`)
- **An install configured before the packs keeps its countries and makers**:
  a configured text file is loaded the old way; the old default path, gone
  after the upgrade, reads the pack beside it.
  (test: `TestOpenGeoReadsAPackAndTheOldFile`, `TestOpenOUIReadsAPackAndTheOldFile`,
  `TestAnOldConfigsTextPathFindsThePack`)

- **The service's resident memory in the lab**: @measured netryx-lab
  2026-11-23 (6 devices, memory_limit 160MiB, `grep -E 'VmRSS|RssAnon|RssFile'
  /proc/$(pidof netryx)/status` 25 s after a restart): with the text files
  loaded 86.5 MB resident, 76.1 MB of it anonymous; with the packs mapped
  50.7 MB, 25.6 MB anonymous and 25.1 MB file pages the kernel may reclaim.
  The target is 64 MB resident under the heavy profile on a 256 MB router.
## The promise about the running console

- **The console runs without its data files.** A missing or unreadable
  address table, app map or vendor list is logged, and the connections are
  still counted and shown, with the country `--` and the app Unknown; with
  the files present a connection gets its country, organisation and app.
  (test: `TestMissingDataFilesDegradeNotFail`, `TestDataFilesNameTheFlows`)
- **A stop loses nothing that was counted.** On SIGTERM or SIGINT the
  history is written, the open hour included, and only then is the rule
  removed, and nothing is written after that; while running, each hour is
  written by the first minute tick after it closes, so a power cut loses at
  most the hour in progress. (test: `TestStateIsSavedOnShutdown`,
  `TestClosedHoursAreWrittenWhileRunning`, `TestNothingIsSavedAfterShutdown`;
  the order inside serve's signal handler is not enforced by a test)
- **History written before the clock was set is loaded once it is.** Files
  later than the clock are set aside at start, and the minute tick loads them
  as soon as the clock passes them; the same error is logged once, not every
  minute. (test: `TestHistoryWrittenBeforeTheClockWasSetIsLoadedOnceItIs`,
  `TestHistoryErrorsAreLoggedWhenTheyChange`) Every load and save of the
  history runs under one mutex. (not enforced by a test)
- **Home is every network the router serves, and its own addresses.** The
  networks are read from all interfaces except those holding a default route
  or reported by ubus as the WAN device, so a guest bridge and the global
  IPv6 prefix are home and the ISP's side of the WAN subnet is not; they are
  re-read every 10 s. (test: `TestTheRoutersOwnAndLANAddressesAreLocal`)
- **Latency is no data until a probe has replied**, never 0 ms; the WAN tile
  takes latency, jitter and loss from one `ping -c 1 -W 1 --` every 10 s, IPv6
  from the wan6 interface, and the rates from the line's counters.
  (test: `TestTheWANTileGetsLatencyFromTheProber`)
- **By default the probe contacts no third party**: it goes to the WAN's
  default gateway from ubus; a configured `ping_target` replaces it, and is
  taken only as an address or a host name, never as an option.
  (test: `TestTheProbeTargetsTheGatewayByDefault`, `TestAProbeTargetCannotBeAnOption`)
- **Only the router's own names reach the console.** Every route sits behind a
  Host check: localhost, loopback, the listen address, the router's own
  house-side addresses, and its own host name alone or in its local domain
  (`flint2`, `flint2.lan`; case-insensitive, any port, a trailing dot as for
  localhost) pass, anything else (a rebound name, `evil.lan`, the WAN address)
  gets 421 with no detail. The host name is read from
  `/proc/sys/kernel/hostname` and the domain from `dhcp.@dnsmasq[0].domain`
  (`lan` when unset), every 10 s with the interfaces; a host name that is not
  one DNS label admits nothing. (test: `TestAForeignHostIsRefused`,
  `TestTheRoutersOwnNameReachesTheConsole`; @decided 2026-11-22: the router's own
  host name and its local-domain form are admitted; a page elsewhere cannot send
  that Host without controlling the house's resolver)
- **A request that arrived outside the house is refused**: on 0.0.0.0 or ::
  the console listens on the WAN address too, and the Host header is the
  client's to write, so the address the connection was made to must be
  loopback, the listen address or one of the router's house-side addresses,
  or the answer is 421; serve logs the warning at start. The default listen is
  loopback. (test: `TestARequestArrivingOnTheWANAddressIsRefused`)
- **A command cannot flood or hold the service**: what ubus or ping prints is
  kept up to 1 MiB and the rest drained, and a child left holding the output
  pipe is cut off 2 s after the command exits or its deadline passes, with
  what it printed before kept. (test: `TestExecRunCapsItsOutput`,
  `TestAHungChildIsCutOffAfterItsDeadline`)
- **Byte accounting is switched on by the init script and left on** after
  the service stops (`net.netfilter.nf_conntrack_acct=1`); the service logs
  when it finds it off. (not enforced by a test)
- **The heap has a soft limit from the config**: the init passes
  `memory_limit` (default 160MiB) as `GOMEMLIMIT` in procd's one env with
  the scrape token; empty sets none. Accepted is a whole number above zero
  with B or KiB (at most 15 digits), MiB (at most 12) or GiB (at most 9), so
  the runtime's int64 of bytes never overflows; anything else is logged and
  sets none.
  (test: `TestTheMemoryLimitReachesTheServiceAsGOMEMLIMIT`;
  measured on the Flint 2, docs/flint2-measured.md sections 6 and 8: the
  all-countries apply peaked at 241.9 MB without the limit and 158.0 MB with it)
- **Speed now is the line's average over the window** (1, 3 or 10 minutes),
  from every 10 s reading of the WAN counters (at most 64 kept, a clock set
  back or another device starting afresh), each weighted by the seconds it
  covers; the last reading is shown small beside it, and before any reading
  the tile says it is the last 10 s. (test: `TestTheWindowAveragesTheLineOverItsSpan`,
  `TestTheWindowKeepsTenMinutesAtMost`, `TestAClockSetBackStartsTheWindowAgain`,
  `TestTheSpeedTileIsTheWindowsAverage`, `TestTheWANTileAveragesTheLineOverTheWindow`;
  the page: "Speed now is the line's average over the window" in `web/src/view.test.ts`)
- **One answer to "whose is this address"**, shared by the flow table, the
  finished totals and the client list: the router's own addresses are the
  device `router`, else the hardware address the capture saw, else the
  neighbour table's, else a DHCP lease's. Only a source at home that is not
  the router is a client; the client list never has a row without a hardware
  address, and lists a device while its flows move bytes. The API marks
  every history key that is not a MAC (`other`, `router`) as a service row.
  (test: `TestADeviceKnownOnlyFromTheNeighbourTableHasItsRate`,
  `TestTheRoutersOwnTrafficIsTheRouter`, `TestFinishedCountsOnlyTheHousesOwnDevices`,
  `TestEachDeviceAppearsOnce`)
- **Online is seen, moving or reachable**: last seen within 5 min, a live
  flow that moved bytes within 5 min, or REACHABLE in the neighbour table.
  "New in 24 h" is null until the service has run 24 h. Connection events
  the kernel could not deliver are logged when their count changes and
  shown as `lostEvents`. The app `unknown` is always named Unknown, never
  after one flow's organisation. (test: `TestALongDownloadKeepsADeviceOnline`,
  `TestAReachableNeighbourIsMarked`, `TestNewDevicesWaitForADayOfRunning`,
  `TestLostEventsAreLoggedWhenTheyChange`, `TestLostConnectionEventsAreShown`,
  `TestUnknownIsNeverNamedAfterOneFlow`)
- **A write from the console is the batch the agent writes.** POST and PUT
  of a rule or an allowance write the whole section (`Replace`, so an option
  left out is gone) through `uci -q batch` with the text on stdin, after the
  model has read the would-be config and found the rule valid; an invalid rule
  is a 400 and nothing is written. An allowance always carries a `rule` option,
  written `'*'` for every rule (uci deletes an option set to an empty value,
  and the model drops an allowance without it; an empty value written by
  hand reads as every rule too), and a cut names a live flow of that MAC and no
  other. (test: `TestARuleWrittenThroughTheAPIIsTheBatchTheAgentWouldWrite`,
  `TestPUTWritesTheWholeSectionSoAnOmittedOptionIsGone`,
  `TestAnInvalidRuleIsRefusedBeforeItIsWritten`, `TestAnAllowanceEndsOnItsOwn`,
  `TestAnAllowanceForEveryRuleIsWrittenAsAStar`,
  `TestCutIsOnlyForTheDevicesOwnFlows`, `TestTheConsoleWritesTheBatchOnStdinThenReloads`,
  `TestAWriteRouteIsReachableOnlyThroughTheGate`)
- **A device's group is shown by its name**, on the clients list and the
  device's card, and found by it in the search; a group the list does not know
  keeps its id. (the page: "a device's group is shown by its name, not its id"
  in `web/src/format.test.ts`)
- **A client shows how it is connected**: a wired one names the bridge port
  its address was learned on (read from `brforward`, no tool needed), a Wi-Fi
  one its network's name from hostapd, and the bridge's own addresses are
  not clients. (test: `TestClientsShowTheirPortOrTheirNetwork`)

- **A page left open across an upgrade learns of it**: build.sh writes the
  page's build (a digest of its sources) into the bundle and beside it, every
  API answer names the build served (`X-Netryx-Build`), and a page that sees
  another offers a reload; on the login, where nothing is lost (a restart ends
  the session), it reloads itself, once for each build. (test: `TestEveryAnswerNamesTheBuildServed`,
  `TestTheBuildIsReadFromBesideThePage`; the page: "a page left open across an
  upgrade is told the router serves a newer one" in `web/src/api.test.ts`)

## The promise about icons and the live view

- **Icons never make the page load from outside.** The console serves them
  itself under `/icons/`, behind the Host check: exactly `manifest.json` and
  `<id>.svg` come from the pack built into the binary, every other
  `/icons/<id>` (ids may carry a dot, never `..`) is a favicon read from
  `<state>/icons` and from nowhere else, typed by its stored kind, with
  `nosniff`, `Content-Security-Policy: default-src 'none'` and a day's
  caching; anything else is 404. (test: `TestFetchedIconsAreServedOnlyFromTheStateDir`,
  `TestUIHeadersForbidOutsideLoads`)
- **Only the router fetches, and only favicons.** Only for apps whose meta
  icon is `fav`, only while `fetch_icons` is on (default on, the config says
  what it does), from one goroutine, sequentially, 10 s per site and 2 s
  between sites actually asked (a kept icon or a failure that holds costs no
  request and no pause), a first pass 2 minutes after the start once the
  clock reads past 2026-01-01 (re-checked every minute), then daily.
  https on the default port only, to the brand's host or a subdomain, at most
  3 redirects, public addresses only, at most 64 KiB, PNG, ICO, JPEG, GIF or
  WebP by magic bytes, never SVG; a site's failure is retried after 7 days.
  A network error (no answer from the resolver, no route, no answer in time)
  is not the site's failure and is not recorded, and 3 in a row end the pass;
  a name that does not exist, a refused connection, and a site that took
  the connection and then said nothing are the site's. So
  a router booting before its line is up holds nothing against the brands.
  (test: `TestAFaviconIsFetchedOnceAndKept`, `TestAFaviconMustBeASmallRasterImageFromTheBrandsOwnSite`,
  `TestAFailureIsNotRetriedForAWeek`, `TestHostileIdsAndDomainsAreRefused`,
  `TestTheFetcherNeverConnectsToPrivateAddresses`, `TestOpenNeverReadsOutsideTheIconDir`,
  `TestANetworkErrorIsNotTheSitesFailure`, `TestAnUnansweredSiteIsNotTheSitesFailure`,
  `TestASiteThatConnectsAndThenSaysNothingIsItsOwnFailure`,
  `TestDueSaysWhetherASiteWouldBeAsked`, `TestIconFetchingCanBeTurnedOff`,
  `TestTheIconLoopKeepsItsPace`, `TestABootWithoutTheInternetCostsNoIcons`)
- **The bundled icons are regenerated, never copied**: each SVG is written
  afresh from the Simple Icons path data, which must match
  `^[MmLlHhVvCcSsQqTtAaZz0-9.,\s+-]+$`, and a slug missing from Simple Icons
  is an error. (test: `TestIconsAreRegeneratedNotCopied`,
  `TestPathDataWithMarkupIsRefused`, `TestAMissingSlugIsAnError`)
- **The router works harder only while someone watches.** One goroutine takes
  every dump; it waits 2 s while a live poll came in within the last 10 s and
  the configured `-sync` (5 s) otherwise, and a `-sync` below 2 s is never
  slowed. The first poll after nobody watched wakes the loop, so live mode
  starts at once. Any viewer on the house side that passes the Host check can
  keep the 2 s pace by polling; that is deliberate: the dump is one global,
  bounded read whoever asks, and a viewer cannot make it heavier.
  (test: `TestSyncSlowsDownWhenNobodyWatches`, `TestALivePollIsReported`)
- **Every setting in the config reaches the service as written.** The init
  script passes each option as `-name=value` (a boolean's value given as the
  next word would end the flag parse and drop every option after it), and
  serve refuses a command line with words left over, naming them.
  (test: `TestTheInitCommandLineIsParsedAsWritten`, which runs the init script
  with procd and uci stood in, parses its command line with serve's flags, and
  requires every option named in `netryx.config` to reach it, `log_group`
  as `-group` included)
- **The icons' licence travels with them**: `build.sh` ships
  `apps-meta.json` and writes the Simple Icons CC0 notice into `NOTICE`
  whenever the pack or the meta is in the package. (not enforced by a gate)

## The promise about the package

- **A package is a package**: the tree carries the service, its config, the
  binary and the three data files the config names, each what it claims to be
  (`oui.pack` and `ip2asn.pack` each passing `tools/datapack -verify` of its
  own kind, a non-empty `appmap.tsv` with the v2fly MIT licence text in
  `NOTICE`); every mode
  is set by `build.sh` (binary and init script 0755, nothing group- or
  world-writable); and the file is not smaller than a quarter of the binary.
  (gate: `scripts/package-sane.sh`)
- **The binary matches the architecture the package claims.** (gate: `scripts/elf-arch.sh`)
- **History survives a sysupgrade**: the package lists `/etc/netryx/` in
  `/lib/upgrade/keep.d/netryx`. (gate: `scripts/package-sane.sh`)
- **Removing the package removes the rules table, an upgrade never does**:
  apk's pre-deinstall and opkg's prerm delete `inet netryx_rules`; the
  scripts that also run on upgrade do not name it. (gate: `scripts/package-sane.sh`)
- **Removal takes the safe resolver's section away and commits nobody's
  dhcp changes**: it reverts its own changes and cuts a committed copy out of
  `/etc/config/dhcp` through a `mktemp` file beside it and one `mv`, never
  `uci commit` and never a fixed name under /tmp. (gate: `scripts/package-sane.sh`,
  which runs both removal scripts against a copy)
- **Isolation's bridge table has its module and goes with the package**: both
  packages depend on `kmod-nft-bridge`, removal deletes `bridge netryx_iso`,
  and the scripts that also run on upgrade do not name it.
  (gate: `scripts/package-sane.sh`)
- **A filtered device in a zone whose input is REJECT still reaches the safe
  resolver**: the package ships the fw4 include
  `/usr/share/nftables.d/chain-pre/input/netryx.nft`, accepting exactly DNS
  redirected to 553 (`ct status dnat`), and the install reloads fw4.
  (gate: `scripts/package-sane.sh`; measured docs/stage2b-measured.md section 7)
- **The first screen stays small, whatever is added**: the page is split into
  chunks by esbuild, every screen past the overview and the clients list
  loaded when it is first opened (`web/src/lazy.tsx`); the first screen
  (`app.js` and every chunk it imports statically, transitively) is held to
  60 KB gzipped, each chunk to 24 KB and all files together to 160 KB. A chunk
  that cannot be loaded (an upgrade under an open page) says so and offers a
  reload. (gate: `scripts/bundle-budget.sh`, run by `web/build.sh`, with its
  cases in `scripts/gates-have-teeth.sh`)
- **A package never ships the "not built" page by accident.** `build.sh`
  builds the page first where node is installed and refuses to package
  without `internal/web/dist/index.html` unless `NETRYX_ALLOW_NO_UI=1`.
  (not enforced by a gate: proven by hand, see the Task 9 report)
- **The data files' licences travel with them**: `build.sh` writes
  `/usr/share/netryx/NOTICE` beside them and refuses to ship `appmap.tsv`
  without the v2fly MIT licence text. (not enforced by a gate)
- **An upgrade restarts what it upgrades.** apk runs `post-upgrade`, not
  `post-install`, when replacing a package. (not enforced: proven by hand on
  the Brume 2026-09-15, pid 9853 -> 9985)
- **Build output never enters the repository.** (not enforced: `.gitignore`
  only, and fourteen files had already been committed before it existed)

## The promise about the console's writes

- **Every change carries the session's token and none is repeated after a
  401.** A write sends `X-Netryx-CSRF`; a 401 from any route shows the login and
  the person presses the button again. (test: `a write carries the session's token and a JSON body`,
  `a 401 sends the page to the login and the write is not retried` in `web/src/api.test.ts`)
- **After a login the console returns to the screen the 401 interrupted**, and
  only to a place on its own page. (test: `after a 401 the login returns to the screen it interrupted`
  in `web/src/rules.test.ts`)
- **A rule reads as what, who and when, and says why it is not working.**
  (test: `a rule reads as what, who and when`, `a rule's state says why it is not working`)
- **Write controls are drawn only for a session that may write**, and the page
  says so when rpcd is down. (not enforced by a test: shown in the lab,
  `no-rpc-desk.png`)

## The promise about language

- **Everything a person reads from netryx is English**: the console page, every
  error, problem and note the service returns into it, Telegram alerts,
  `netryx ctl` and the command line's output. Strings are written where they
  are used, with one term per concept (quarantine, isolated, allowance, time
  limit); there is no translation layer yet. Tests may carry names in any
  script, since a household's own names pass through untouched.
  (gate: `scripts/english-only.sh`, over the sources, the test and lab
  scripts, the package files and the documents; @decided 2026-11-21: the
  console is English first, a second language may come later)

## How work is done here

- **A published screenshot never carries the lab line's private values**:
  `scripts/lab/console-shots.mjs` reads `~/netryx-shots.deny` (kept off the
  repository) and saves no page whose text holds any of them, nor anything
  without the file or with it empty. (not enforced by a gate: @measured
  2026-11-22 on forge, a deny list naming a value on the overview, an empty
  list and no list each stopped the run before the first image)

- Every scenario in `features/` names a test, and every named test exists.
  (gate: `scripts/scenarios-bound.sh`)
- Every gate goes red on its own planted fault, stays green on a good tree, and
  refuses to report success when its subject is missing.
  (gate: `scripts/gates-have-teeth.sh`)
- A new test is run against the broken code first. Where a defect is subtle,
  the fault is planted back and the test required to fail; the commit says
  which mutants were caught and which survived, and why.
