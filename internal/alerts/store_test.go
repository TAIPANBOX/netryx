package alerts

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func london(t *testing.T) *time.Location {
	t.Helper()
	l, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func newStore(t *testing.T) (*Store, *clock, string) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 21, 12, 0, 0, 0, london(t))}
	path := filepath.Join(t.TempDir(), "alerts.json")
	return NewStore(path, c.now, london(t)), c, path
}

const cam = "02:4e:58:00:00:04"

// spec 6.9: a new device once; a blocked-country try once per device and
// country a day; unusual upload once per device a day.
func TestEachKindOfAlertIsRateLimited(t *testing.T) {
	s, c, _ := newStore(t)
	if !s.Raise(Alert{Kind: NewDevice, MAC: cam}) || s.Raise(Alert{Kind: NewDevice, MAC: cam}) {
		t.Fatal("a new device is told once")
	}
	if !s.Raise(Alert{Kind: BlockedCountry, MAC: cam, Country: "RU"}) {
		t.Fatal("the first try towards RU is told")
	}
	if s.Raise(Alert{Kind: BlockedCountry, MAC: cam, Country: "RU"}) {
		t.Fatal("a second try towards RU the same day is not")
	}
	if !s.Raise(Alert{Kind: BlockedCountry, MAC: cam, Country: "BY"}) {
		t.Fatal("another country is told")
	}
	if !s.Raise(Alert{Kind: Upload, MAC: cam, Bytes: 300e6}) || s.Raise(Alert{Kind: Upload, MAC: cam, Bytes: 400e6}) {
		t.Fatal("unusual upload is told once a day")
	}
	// The router's day: 23:59 and 00:01 London are two days.
	c.t = time.Date(2026, 11, 22, 0, 1, 0, 0, london(t))
	if !s.Raise(Alert{Kind: BlockedCountry, MAC: cam, Country: "RU"}) || !s.Raise(Alert{Kind: Upload, MAC: cam, Bytes: 300e6}) {
		t.Fatal("a new local day tells again")
	}
	if s.Raise(Alert{Kind: NewDevice, MAC: cam}) {
		t.Fatal("a new device is told once ever, not once a day")
	}
	if s.Raise(Alert{Kind: "nonsense", MAC: cam}) {
		t.Fatal("an unknown kind is not stored")
	}
}

func TestTheAlertStoreIsBounded(t *testing.T) {
	s, _, _ := newStore(t)
	for i := 0; i < 300; i++ {
		s.Raise(Alert{Kind: NewDevice, MAC: fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256)})
	}
	l := s.List()
	if len(l) != MaxAlerts {
		t.Fatalf("%d alerts kept; want %d", len(l), MaxAlerts)
	}
	if l[0].MAC != "02:00:00:00:01:2b" {
		t.Fatalf("newest first: got %s", l[0].MAC)
	}
}

func TestAlertsSurviveARestart(t *testing.T) {
	s, c, path := newStore(t)
	s.Raise(Alert{Kind: NewDevice, MAC: cam})
	s.Raise(Alert{Kind: BlockedCountry, MAC: cam, Country: "RU"})
	again := NewStore(path, c.now, london(t))
	if l := again.List(); len(l) != 2 || l[0].Kind != BlockedCountry {
		t.Fatalf("after a restart: %+v", l)
	}
	if again.Raise(Alert{Kind: NewDevice, MAC: cam}) || again.Raise(Alert{Kind: BlockedCountry, MAC: cam, Country: "RU"}) {
		t.Fatal("a restart must not tell the same thing again")
	}
}

func TestAnUnsentAlertIsMarkedWhenSent(t *testing.T) {
	s, _, _ := newStore(t)
	s.Raise(Alert{Kind: NewDevice, MAC: cam})
	u := s.Unsent()
	if len(u) != 1 || u[0].ID == "" {
		t.Fatalf("Unsent = %+v", u)
	}
	s.Mark(u[0].ID, "", "Telegram: 429")
	if l := s.List(); l[0].Problem != "Telegram: 429" || len(s.Unsent()) != 1 {
		t.Fatalf("a failed send stays unsent with its problem: %+v", l[0])
	}
	s.Mark(u[0].ID, "telegram", "")
	if len(s.Unsent()) != 0 || s.List()[0].Sent != "telegram" {
		t.Fatalf("a sent alert is not unsent any more: %+v", s.List()[0])
	}
}

// spec 6.9: more than four times the 7-day hourly maximum and at least 200 MB,
// for a device with at least a day of history.
func TestAnUnusualUploadNeedsFourTimesTheWeeksMaxAndTwoHundredMB(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		up, max uint64
		have    time.Duration
		want    bool
	}{
		{199e6, 10e6, 7 * day, false},  // ten times, under 200 MB
		{300e6, 100e6, 7 * day, false}, // 3x
		{300e6, 60e6, 7 * day, true},   // 5x
		{400e6, 100e6, 7 * day, false}, // exactly 4x is not more than 4x
		{300e6, 0, 7 * day, true},      // nothing before
		{300e6, 10e6, 23 * time.Hour, false},
	} {
		if got := Unusual(c.up, c.max, c.have); got != c.want {
			t.Errorf("Unusual(%d, %d, %v) = %v; want %v", c.up, c.max, c.have, got, c.want)
		}
	}
}

// Final review I3: new devices never push the serious alerts out of the list.
func TestNewDevicesNeverPushOutTheSeriousAlerts(t *testing.T) {
	s, _, _ := newStore(t)
	s.Raise(Alert{Kind: BlockedCountry, MAC: cam, Country: "RU"})
	s.Raise(Alert{Kind: Upload, MAC: cam, Bytes: 300e6})
	for i := 0; i < 300; i++ {
		s.Raise(Alert{Kind: NewDevice, MAC: fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256)})
	}
	kinds := map[string]bool{}
	for _, a := range s.List() {
		kinds[a.Kind] = true
	}
	if !kinds[BlockedCountry] || !kinds[Upload] || len(s.List()) != MaxAlerts {
		t.Fatalf("after a flood of new devices: %v, %d kept", kinds, len(s.List()))
	}
}

// A second flood in the same day is told too: the summary repeats once an
// hour, not once a day (automated security review of caa4867).
func TestASecondFloodLaterIsToldAgain(t *testing.T) {
	s, c, _ := newStore(t)
	if !s.Raise(Alert{Kind: NewDevices, Count: 900}) || s.Raise(Alert{Kind: NewDevices, Count: 50}) {
		t.Fatal("one summary for one flood")
	}
	c.t = c.t.Add(time.Hour)
	if !s.Raise(Alert{Kind: NewDevices, Count: 700}) {
		t.Fatal("a flood an hour later is told again")
	}
}
