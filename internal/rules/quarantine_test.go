package rules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newRosterAt(t *testing.T, at time.Time) (*Roster, *fakeClock, string) {
	t.Helper()
	c := &fakeClock{t: at}
	path := filepath.Join(t.TempDir(), "known-macs.json")
	return NewRoster(path, c.now), c, path
}

func TestTheDevicesKnownAtInstallAreApproved(t *testing.T) {
	r, c, _ := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{tv, "02:4E:58:00:00:02", "not-a-mac"}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(25 * time.Hour)
	got := r.Approved(nil)
	if len(got) != 2 || got[0] != tv || got[1] != laptop {
		t.Fatalf("Approved = %v; want the tv and the laptop", got)
	}
	if news, _ := r.See([]Seen{{MAC: tv, At: c.t}, {MAC: laptop, At: c.t}}, nil); len(news) != 0 {
		t.Fatalf("known devices came back as newcomers: %v", news)
	}
}

func TestADeviceNeverSeenBeforeIsQuarantined(t *testing.T) {
	r, c, _ := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{tv}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(25 * time.Hour)
	if !r.Active(true) {
		t.Fatal("after the grace the quarantine must be active")
	}
	news, _ := r.See([]Seen{{MAC: camera, Hostname: "cam", At: c.t}}, nil)
	if len(news) != 1 || news[0].MAC != camera {
		t.Fatalf("newcomers = %v; want the camera", news)
	}
	if again, _ := r.See([]Seen{{MAC: camera, At: c.t.Add(time.Minute)}}, nil); len(again) != 0 {
		t.Fatalf("a newcomer is reported once, got again %v", again)
	}
	if p := r.Pending(); len(p) != 1 || p[0].MAC != camera || p[0].Hostname != "cam" {
		t.Fatalf("Pending = %+v", p)
	}
	for _, m := range r.Approved(nil) {
		if m == camera {
			t.Fatal("a newcomer is approved")
		}
	}
	if r.Active(false) {
		t.Fatal("quarantine turned off must not be active")
	}
}

// spec 6.8: "known on the day 2c is installed". A device asleep when the
// baseline was taken joins it while the day lasts.
func TestADeviceAsleepOnInstallDayIsNotQuarantined(t *testing.T) {
	r, c, path := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{tv}); err != nil {
		t.Fatal(err)
	}
	if r.Active(true) {
		t.Fatal("the quarantine must wait for the grace to end")
	}
	c.t = c.t.Add(20 * time.Hour)
	if news, _ := r.See([]Seen{{MAC: camera, At: c.t}}, nil); len(news) != 0 {
		t.Fatalf("a device seen during the grace is a newcomer: %v", news)
	}
	again := NewRoster(path, c.now)
	if err := again.Baseline(nil); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(5 * time.Hour)
	found := false
	for _, m := range again.Approved(nil) {
		found = found || m == camera
	}
	if !found {
		t.Fatal("a device added during the grace is not in the saved baseline")
	}
}

// Review Focus 1: a damaged baseline must never put the house in quarantine.
// It is rebuilt from what the router knows and a new grace starts, so a
// device asleep at that moment joins it rather than being held.
func TestADamagedBaselineNeverQuarantinesTheHouse(t *testing.T) {
	r, c, path := newRosterAt(t, summer(t))
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Baseline([]string{tv}); err == nil {
		t.Fatal("a damaged baseline must be reported")
	}
	if r.Active(true) {
		t.Fatal("a rebuilt baseline holds nobody until its grace ends")
	}
	c.t = c.t.Add(time.Hour)
	if news, _ := r.See([]Seen{{MAC: laptop, At: c.t}}, nil); len(news) != 0 {
		t.Fatalf("a device asleep at the rebuild came back as a newcomer: %v", news)
	}
	b, _ := os.ReadFile(path)
	if len(b) == 0 || b[0] != '{' {
		t.Fatalf("the baseline was not rewritten: %q", b)
	}
}

// Review Focus 2: a flood of made-up MACs keeps the console's list at its
// bound, adds nothing to Approved, and is reported once.
func TestAFloodOfNewMACsIsBounded(t *testing.T) {
	r, c, _ := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{tv}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(25 * time.Hour)
	var flood []Seen
	for i := 0; i < 5000; i++ {
		flood = append(flood, Seen{MAC: fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256), At: c.t})
	}
	_, full := r.See(flood, nil)
	if !full {
		t.Fatal("an overflow must be reported")
	}
	if n := len(r.Pending()); n != MaxPending {
		t.Fatalf("Pending holds %d; want %d", n, MaxPending)
	}
	if n := len(r.Approved(nil)); n != 1 {
		t.Fatalf("Approved holds %d after the flood; want 1", n)
	}
	if _, full := r.See(flood, nil); full {
		t.Fatal("the overflow is reported once, not at every look")
	}
}

// Review Focus 5: a phone that rotates its MAC comes back as new; the
// approval names the known device with the same hostname.
func TestARotatingMACNamesTheLikelyDevice(t *testing.T) {
	r, c, _ := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{phone}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(25 * time.Hour)
	r.See([]Seen{{MAC: phone, Hostname: "iPhone", At: c.t}}, nil)
	rotated := "06:11:22:33:44:55"
	r.See([]Seen{{MAC: rotated, Hostname: "iphone", At: c.t}}, nil)
	p := r.Pending()
	if len(p) != 1 || p[0].LikelySame != phone {
		t.Fatalf("Pending = %+v; want the rotated MAC to name %s", p, phone)
	}
}

func TestAnApprovedMACIsNeverPending(t *testing.T) {
	r, c, _ := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{tv}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(25 * time.Hour)
	r.See([]Seen{{MAC: camera, At: c.t}}, nil)
	r.See([]Seen{{MAC: camera, At: c.t}}, []string{camera})
	if p := r.Pending(); len(p) != 0 {
		t.Fatalf("an approved device is still pending: %+v", p)
	}
	found := false
	for _, m := range r.Approved([]string{camera}) {
		found = found || m == camera
	}
	if !found {
		t.Fatal("an approval in UCI is not in Approved")
	}
}

func TestAPendingDeviceGoneForAWeekDropsOff(t *testing.T) {
	r, c, _ := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{tv}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(25 * time.Hour)
	r.See([]Seen{{MAC: camera, At: c.t}}, nil)
	c.t = c.t.Add(8 * 24 * time.Hour)
	r.See(nil, nil)
	if p := r.Pending(); len(p) != 0 {
		t.Fatalf("a device gone for 8 days is still listed: %+v", p)
	}
}

// A held device has the internet, so it is in the history and the inventory;
// a rebuild of a damaged baseline must not approve it (automated security
// review of 8b9b321).
func TestARebuiltBaselineNeverApprovesAHeldDevice(t *testing.T) {
	r, c, path := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{tv}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(25 * time.Hour)
	r.See([]Seen{{MAC: camera, At: c.t}}, nil)
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := NewRoster(path, c.now)
	if err := again.Baseline([]string{tv, camera}); err == nil {
		t.Fatal("a damaged baseline must be reported")
	}
	for _, m := range again.Approved(nil) {
		if m == camera {
			t.Fatal("the rebuild approved a held device")
		}
	}
	c.t = c.t.Add(time.Hour)
	again.See([]Seen{{MAC: camera, At: c.t}}, nil)
	for _, m := range again.Approved(nil) {
		if m == camera {
			t.Fatal("a held device joined the baseline during the new grace")
		}
	}
	if p := again.Pending(); len(p) != 1 || p[0].MAC != camera {
		t.Fatalf("Pending = %+v; want the camera still held", p)
	}
}

// The hostname is what a device says about itself; it names a likely device
// only for a randomised (locally administered) MAC while the known device is
// not on the network at the same time.
func TestALikelySameDeviceIsNamedOnlyWhenItCouldBeTheSame(t *testing.T) {
	r, c, _ := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{phone}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(25 * time.Hour)
	r.See([]Seen{{MAC: phone, Hostname: "iPhone", At: c.t}}, nil)
	both := "06:11:22:33:44:56"
	r.See([]Seen{{MAC: phone, Hostname: "iPhone", At: c.t}, {MAC: both, Hostname: "iPhone", At: c.t}}, nil)
	universal := "00:11:22:33:44:57"
	r.See([]Seen{{MAC: universal, Hostname: "iPhone", At: c.t}}, nil)
	for _, p := range r.Pending() {
		if p.LikelySame != "" {
			t.Fatalf("%s names %s, but %s", p.MAC, p.LikelySame, map[string]string{
				both: "the known phone is on the network at the same time", universal: "its MAC is not randomised"}[p.MAC])
		}
	}
}

// Found in the stage's live run: the inventory gives a device it only knows
// from a lease no last-seen time, and a newcomer stamped with the zero time
// was dropped as gone for a week in the same look.
func TestANewcomerWithNoTimeIsStillListed(t *testing.T) {
	r, c, _ := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{tv}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(25 * time.Hour)
	news, _ := r.See([]Seen{{MAC: camera}}, nil)
	if len(news) != 1 {
		t.Fatalf("newcomers = %v", news)
	}
	r.See([]Seen{{MAC: camera}}, nil)
	if p := r.Pending(); len(p) != 1 || !p[0].Last.Equal(c.t) {
		t.Fatalf("Pending = %+v; want the camera, last seen now", p)
	}
}

// Final review I4: a fresh image starts from its build time; a baseline taken
// then would have its grace end at the NTP jump. It waits for a set clock.
func TestTheBaselineWaitsForTheClock(t *testing.T) {
	r, c, path := newRosterAt(t, time.Date(2020, 1, 1, 0, 1, 0, 0, time.UTC))
	if err := r.Baseline([]string{tv}); !errors.Is(err, ErrClockNotSet) {
		t.Fatalf("Baseline before the clock is set: %v; want ErrClockNotSet", err)
	}
	if r.Good() || r.Active(true) {
		t.Fatal("no baseline may hold before the clock is set")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a baseline was written with an unset clock")
	}
	c.t = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	if err := r.Baseline([]string{tv}); err != nil {
		t.Fatal(err)
	}
	if r.Active(true) {
		t.Fatal("the grace starts when the clock is right")
	}
	c.t = c.t.Add(Grace + time.Minute)
	if !r.Active(true) {
		t.Fatal("a day after the clock was right the quarantine holds")
	}
}

// Final review I3: a full list makes room from devices not seen for an hour,
// so a real newcomer after a flood is listed.
func TestAFullListMakesRoomForANewDevice(t *testing.T) {
	r, c, _ := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{tv}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(25 * time.Hour)
	var flood []Seen
	for i := 0; i < MaxPending; i++ {
		flood = append(flood, Seen{MAC: fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256), At: c.t})
	}
	r.See(flood, nil)
	c.t = c.t.Add(2 * time.Hour)
	if news, _ := r.See([]Seen{{MAC: camera, Hostname: "cam", At: c.t}}, nil); len(news) != 1 {
		t.Fatalf("a real newcomer after the flood is not listed: %v", news)
	}
	found := false
	for _, p := range r.Pending() {
		found = found || p.MAC == camera
	}
	if !found || len(r.Pending()) != MaxPending {
		t.Fatalf("Pending holds the camera %v, size %d", found, len(r.Pending()))
	}
}

// A router rebooting before NTP keeps its good baseline: the clock is checked
// only before a new one is taken (automated security review of 2984e5a).
func TestAGoodBaselineIsReadBeforeTheClockIsSet(t *testing.T) {
	r, c, path := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{tv}); err != nil {
		t.Fatal(err)
	}
	c.t = time.Date(2020, 1, 1, 0, 1, 0, 0, time.UTC)
	again := NewRoster(path, c.now)
	if err := again.Baseline(nil); err != nil || !again.Good() {
		t.Fatalf("a good baseline before NTP: err %v, good %v", err, again.Good())
	}
}

// A clock set back before the baseline (a reboot before NTP) must not reopen
// the grace and let a newcomer into the baseline (automated security review
// of 2e9495f).
func TestAClockSetBackNeverReopensTheGrace(t *testing.T) {
	r, c, _ := newRosterAt(t, summer(t))
	if err := r.Baseline([]string{tv}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(25 * time.Hour)
	c.t = time.Date(2020, 1, 1, 0, 5, 0, 0, time.UTC)
	news, _ := r.See([]Seen{{MAC: camera, At: c.t}}, nil)
	for _, m := range r.Approved(nil) {
		if m == camera {
			t.Fatal("a clock set back reopened the grace: the camera joined the baseline")
		}
	}
	if len(news) != 1 || !r.Active(true) {
		t.Fatalf("newcomers %v, active %v; want the camera held", news, r.Active(true))
	}
}
