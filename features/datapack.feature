# The two fixed tables: the address table and the vendor list held 21 MB and
# 5 MB of the service's heap on a router that may have 256 MB in all, so the
# package ships them as binary packs the service maps read-only.

Feature: the address table and the vendor list cost the service no heap

  Scenario: the packs are mapped, not loaded
    Given a router with the packs the package ships
    When netryx opens the address table and the vendor list
    Then the two together hold no more than 2 MB of its heap
    # -> TestTheFixedTablesFitTheirBudget

  Scenario: a pack answers exactly as the old file did
    Given the address table and the vendor list built from the same sources
    When an address or a hardware address is looked up in the pack and in the old file
    Then both give the same country, network, organisation and maker, and the country rules get the same ranges
    # -> TestThePackAnswersAsTheLoaderForEveryAddress
    # -> TestTheVendorPackAnswersAsTheListForEveryPrefix
    # -> TestAPackAnswersAsTheTableItWasWrittenFrom
    # -> TestAVendorPackAnswersAsTheList

  Scenario: a damaged pack is refused, and the console runs as without the file
    Given a pack damaged or cut short on the router's flash
    When netryx opens it
    Then it is refused with the reason, the console shows no country or maker, and the log says why
    # -> TestADamagedPackIsRefusedWithItsReason
    # -> TestADamagedVendorPackIsRefusedWithItsReason
    # -> TestADamagedDataPackDegradesLikeAMissingOne

  Scenario: an install configured before the packs keeps working
    Given a config that still names the old text files
    When the old file is there it is read the old way, and when the upgrade has taken it away the pack beside it is read
    Then countries and makers are still named
    # -> TestOpenGeoReadsAPackAndTheOldFile
    # -> TestOpenOUIReadsAPackAndTheOldFile
    # -> TestAnOldConfigsTextPathFindsThePack

  Scenario: a package never ships a pack that fails its check
    Given the package tree
    When it is checked before it is shipped
    Then a missing, damaged or wrong-kind pack is refused
    # -> TestDatapackWritesPacksThatVerify
