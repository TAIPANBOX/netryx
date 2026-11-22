package rules

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	usageMAC = "02:4e:58:00:00:03"
	usageApp = "tiktok"
)

func trackedUsage(t *testing.T, loc *time.Location) *Usage {
	t.Helper()
	u := NewUsage(loc)
	u.Track(map[string]map[string]bool{usageMAC: {usageApp: true}})
	return u
}

func TestAMinuteCountsAtSixtyFourKilobytes(t *testing.T) {
	loc := london(t)
	at := time.Date(2026, 11, 21, 10, 0, 5, 0, loc)

	u := trackedUsage(t, loc)
	u.Add(usageMAC, usageApp, 60000, 5535, at) // 65535 bytes: one short
	u.Close(at.Add(time.Minute))
	if got := u.Minutes(usageMAC, usageApp); got != 0 {
		t.Fatalf("65535 bytes counted %d minutes; want 0", got)
	}

	u = trackedUsage(t, loc)
	u.Add(usageMAC, usageApp, 60000, 5536, at) // 65536 bytes, up and down together
	if got := u.Minutes(usageMAC, usageApp); got != 0 {
		t.Fatalf("a minute counted before it closed: %d", got)
	}
	u.Close(at.Add(time.Minute))
	if got := u.Minutes(usageMAC, usageApp); got != 1 {
		t.Fatalf("65536 bytes counted %d minutes; want 1", got)
	}

	u = trackedUsage(t, loc)
	u.Add(usageMAC, usageApp, 40000, 0, at)
	u.Add(usageMAC, usageApp, 40000, 0, at.Add(time.Minute)) // the next minute: neither reaches the bar
	u.Close(at.Add(3 * time.Minute))
	if got := u.Minutes(usageMAC, usageApp); got != 0 {
		t.Fatalf("two minutes below the bar counted %d; want 0", got)
	}
}

func TestUsageRestartsAtLocalMidnight(t *testing.T) {
	loc := london(t)
	// 2026-03-29 is the 23-hour day in London (clocks go forward at 01:00 GMT).
	before := time.Date(2026, 3, 28, 23, 58, 0, 0, loc)
	u := trackedUsage(t, loc)
	u.Add(usageMAC, usageApp, 70000, 0, before)
	u.Close(before.Add(time.Minute))
	if got := u.Minutes(usageMAC, usageApp); got != 1 {
		t.Fatalf("before midnight: %d minutes; want 1", got)
	}
	midnight := time.Date(2026, 3, 29, 0, 0, 30, 0, loc)
	u.Close(midnight)
	if got := u.Minutes(usageMAC, usageApp); got != 0 {
		t.Fatalf("after local midnight: %d minutes; want 0", got)
	}

	// Summer: local midnight is 23:00 UTC, so 23:30 UTC is already the next day.
	summer := time.Date(2026, 7, 6, 23, 58, 0, 0, loc)
	u = trackedUsage(t, loc)
	u.Add(usageMAC, usageApp, 70000, 0, summer)
	u.Close(summer.Add(time.Minute))
	u.Close(time.Date(2026, 7, 6, 23, 30, 0, 0, time.UTC))
	if got := u.Minutes(usageMAC, usageApp); got != 0 {
		t.Fatalf("after local (not UTC) midnight: %d minutes; want 0", got)
	}
}

func TestUsageIsReloadedOnlyOnTheSameDay(t *testing.T) {
	loc := london(t)
	path := filepath.Join(t.TempDir(), "usage.json")
	at := time.Date(2026, 11, 21, 10, 0, 0, 0, loc)
	u := trackedUsage(t, loc)
	for i := 0; i < 3; i++ {
		u.Add(usageMAC, usageApp, 70000, 0, at.Add(time.Duration(i)*time.Minute))
	}
	u.Close(at.Add(3 * time.Minute))
	if err := u.Save(path); err != nil {
		t.Fatal(err)
	}

	same := trackedUsage(t, loc)
	if err := same.Load(path, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := same.Minutes(usageMAC, usageApp); got != 3 {
		t.Fatalf("reloaded the same day: %d minutes; want 3", got)
	}

	next := trackedUsage(t, loc)
	if err := next.Load(path, time.Date(2026, 11, 22, 0, 1, 0, 0, loc)); err != nil {
		t.Fatal(err)
	}
	if got := next.Minutes(usageMAC, usageApp); got != 0 {
		t.Fatalf("yesterday's file reloaded: %d minutes; want 0", got)
	}

	missing := trackedUsage(t, loc)
	if err := missing.Load(filepath.Join(t.TempDir(), "none.json"), at); err != nil {
		t.Fatalf("a missing file is no error: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := missing.Load(path, at); err == nil {
		t.Fatal("a damaged file loaded without an error")
	}
}

func TestOnlyTrackedPairsAreKept(t *testing.T) {
	loc := london(t)
	at := time.Date(2026, 11, 21, 10, 0, 0, 0, loc)
	u := trackedUsage(t, loc)
	u.Add("02:4e:58:00:00:01", usageApp, 70000, 0, at)
	u.Add(usageMAC, "youtube", 70000, 0, at)
	u.Add(usageMAC, usageApp, 70000, 0, at)
	u.Close(at.Add(time.Minute))
	if u.Minutes("02:4e:58:00:00:01", usageApp) != 0 || u.Minutes(usageMAC, "youtube") != 0 {
		t.Fatal("an untracked pair was counted")
	}
	if n := u.Len(); n != 1 {
		t.Fatalf("the table holds %d pairs; want 1", n)
	}
	u.Track(map[string]map[string]bool{})
	if n := u.Len(); n != 0 {
		t.Fatalf("after untracking the table holds %d pairs; want 0", n)
	}
}

func TestAWildcardPairCountsEveryDevice(t *testing.T) {
	loc := london(t)
	at := time.Date(2026, 11, 21, 10, 0, 0, 0, loc)
	u := NewUsage(loc)
	u.Track(map[string]map[string]bool{"": {usageApp: true}})
	u.Add("02:4e:58:00:00:01", usageApp, 70000, 0, at)
	u.Add(usageMAC, usageApp, 70000, 0, at)
	u.Add(usageMAC, "youtube", 70000, 0, at)
	u.Close(at.Add(time.Minute))
	snap := u.Snapshot()
	if snap["02:4e:58:00:00:01"][usageApp] != 1 || snap[usageMAC][usageApp] != 1 || snap[usageMAC]["youtube"] != 0 {
		t.Fatalf("Snapshot = %v; want one minute of tiktok for both devices and none of youtube", snap)
	}
}

// Deltas are filed under a MAC, an address the resolver does not know yet, or
// a service key; only a MAC can stand in an ether_addr set, and one bad
// element would make nft refuse every apply.
func TestOnlyHardwareAddressesAreCounted(t *testing.T) {
	loc := london(t)
	at := time.Date(2026, 11, 21, 10, 0, 0, 0, loc)
	u := NewUsage(loc)
	u.Track(map[string]map[string]bool{"": {usageApp: true}})
	for _, key := range []string{"192.168.1.5", "router", "other", "fe80::1", "02:4E:58:00:00:03"} {
		u.Add(key, usageApp, 70000, 0, at)
	}
	u.Close(at.Add(time.Minute))
	snap := u.Snapshot()
	if len(snap) != 1 || snap[usageMAC][usageApp] != 1 {
		t.Fatalf("Snapshot = %v; want only the MAC, in lower case", snap)
	}
}
