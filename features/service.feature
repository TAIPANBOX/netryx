# @decided 2026-09-15: the service the package installs must run the capture
# itself, and it must not go blind when the network is reloaded.

Feature: the installed service is the thing that was tested

  Scenario: the service names services, not just volumes
    Given netryx is running as the router's own service
    When a device starts a connection
    Then the service itself reads the handshake and publishes which service
      was asked for, beside the volume the connection table counts
    # -> TestADeviceIsTheMACAndTheNamesItAsksFor

  Scenario: one device, not one row per address
    Given a device that uses an IPv4 address and two IPv6 addresses
    When the capture and the connection table are joined
    Then the volumes add up into a single row for that hardware address
    And a device the connection table has no open flow for is still listed
    # -> TestOneDeviceNotOneRowPerAddress

  Scenario: a name the client encrypted is counted, never guessed
    Given a handshake that hides its destination behind encryption
    When netryx reads it
    Then it counts one hidden name and records no destination, because the
      name left in the clear is a decoy shared by every such site
    # -> TestAnEncryptedNameIsItsOwnNumber

  Scenario: a house cannot fill the router's memory
    Given more devices and more destinations than the caps allow
    When netryx keeps counting
    Then the table stops at its caps and says how much it refused
    # -> TestTheTableCannotGrowWithoutLimit

  Scenario: a destination name never becomes a metric label
    Given a device that asked for many different services
    When the metrics are scraped
    Then the number of series grows with devices and never with destinations
    # -> TestHandshakesAreCountersAndNamesAreNotLabels

  Scenario: a reloaded network does not make the monitor go quiet
    Given the rule is attached to the ports a bridge is made of
    When a port disappears and comes back, as it does on a Wi-Fi reload
    Then netryx notices that the kernel dropped that attachment, reinstalls
      the rule and says so in the log
    # -> TestAMemberThatComesBackIsWatchedAgain

  Scenario: the kernel is the authority on what is attached
    Given the rule was installed successfully a minute ago
    When netryx checks whether it is still attached
    Then it reads the answer from the kernel rather than from its own memory,
      in both the forms the kernel prints it
    # -> TestTheKernelIsAskedWhatIsAttached

  Scenario: a bridge with no ports is not something to install on
    Given a bridge whose ports are all down, as during a reload
    When netryx supervises its rule
    Then it waits and says why, rather than installing an empty rule or
      attaching to the bridge device and calling that success
    # -> TestAnEmptyOrBrokenBridgeIsNotReinstalledOverAndOver

  Scenario: a request carries nothing it does not declare
    Given netryx asks the kernel for the neighbour table or the connections
    When it builds that request
    Then the message is exactly its header plus the fixed structure, so the
      kernel has no trailing bytes to read as attributes and no reason to
      write a warning into the router's log
    # -> TestTheDumpRequestCarriesNothingItDoesNotDeclare

  Scenario: a person opens the page and reads it
    Given netryx is serving
    When somebody opens it in a browser
    Then they see a row per device with what it asked for, how much it moved,
      and how much of it could not be named, and the page fetches nothing
      from anywhere outside the router
    # -> TestThePageEscapesWhatDevicesCallThemselvesAndFetchesNothing

  Scenario: a device cannot bring its own markup onto that page
    Given a device whose DHCP name is written to look like markup
    When the page is drawn
    Then the name appears as text and nothing of it is executed
    # -> TestThePageEscapesWhatDevicesCallThemselvesAndFetchesNothing

  Scenario: an empty page explains itself
    Given nothing has been seen yet, or the kernel is not counting bytes
    When the page is drawn
    Then it says so, instead of showing an empty table or a column of zeros
    # -> TestAnEmptyPageSaysSoRatherThanLookingBroken

  Scenario: the router can report on itself
    Given traffic that has no hardware address, such as the router's own
    When the capture and the connection table are joined
    Then those rows keep what was learned about them rather than being dropped
    # -> TestADeviceWithNoHardwareAddressIsStillADevice

  Scenario: ordinary traffic is not counted as a handshake
    Given the router's own chain copies the first packets of every connection
    When those packets are not the start of anything nameable
    Then they are not counted, so "could not be named" stays a fact about the
      network rather than about which rule copied the packet
    # -> TestOnlyTheStartOfSomethingCountsAsAHandshake

  Scenario: a device that finished its download is not forgotten
    Given the connection table can only say what is open at this moment
    When a connection ends
    Then the kernel's final counters for it are added to that device, so the
      volumes answer "who used the line" rather than "who is using it right
      now", and a device that made no readable handshake still gets a row
    # -> TestAFinishedConnectionIsNotAForgottenOne

  Scenario: a monitor that could not keep up says so
    Given more packets are selected than the reader can take
    When the kernel drops copies
    Then netryx counts exactly how many from the kernel's own numbering, and
      publishes it, so a quiet dashboard is never mistaken for a quiet house
    # -> TestLostCopiesAreCountedExactly

  Scenario: and it says whether it can see losses at all
    Given a kernel that does not number the copies it sends
    When netryx starts reading
    Then it says so in the log, because two different zeros look the same
    # -> TestTheSequenceNumberIsReadFromTheAttributeTheKernelUses

  Scenario: a handshake that does not fit in one packet is still a name
    Given a browser whose ClientHello spans two segments, as modern ones do
    When both arrive
    Then they are joined and the name is read, counted as one handshake and
      not two
    # -> TestAHandshakeSplitInTwoIsStillAName

  Scenario: half a handshake is not held for ever
    Given a first half whose second half never arrives
    When the waiting time passes
    Then it is let go and counted as one that could not be named
    # -> TestHalvesNobodyFinishesAreLetGo

  Scenario: one flow cannot spend the router's memory
    Given a flow that keeps sending without ever completing its handshake
    When netryx holds what it has
    Then what is held for that flow stays bounded
    # -> TestOneFlowCannotSpendTheRoutersMemory

  Scenario: a house of devices, each answered for separately
    Given a router with several devices behind it, each with its own hardware
      address and its own name from DHCP
    When every device visits a different set of services
    Then each one is a row of its own, named as it called itself, carrying
      what it asked for and nothing another device asked for
    # -> TestADeviceIsTheMACAndTheNamesItAsksFor
    # run live: test/house/house.sh, 8 devices, no traces left behind
