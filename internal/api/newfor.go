package api

// "New for this device": the apps and countries a device used since the
// start of yesterday (UTC) and in none of the 29 days before, from the
// history the router already keeps (the day ring); nothing new is written.

import (
	"net/http"
	"time"

	"netryx/internal/enrich"
)

const (
	// newLookBack is how far before yesterday a key must not have been seen
	// to be new: the rest of the day ring.
	newLookBack = 29 * 24 * time.Hour
	// newMinKnown is how long the router must have known a device before
	// anything is called new for it.
	newMinKnown = 7 * 24 * time.Hour
)

type newAppJSON struct {
	App   appJSON `json:"app"`
	First string  `json:"first"`
	Down  uint64  `json:"down"`
	Up    uint64  `json:"up"`
}

type newCountryJSON struct {
	CC    string `json:"cc"`
	First string `json:"first"`
	Down  uint64 `json:"down"`
	Up    uint64 `json:"up"`
}

type newForJSON struct {
	// TooNew says the router has known the device for less than a week,
	// KnownDays how many whole days it has.
	TooNew    bool             `json:"tooNew"`
	KnownDays int              `json:"knownDays"`
	Since     string           `json:"since"`
	Apps      []newAppJSON     `json:"apps"`
	Countries []newCountryJSON `json:"countries"`
}

func (s *server) newFor(r *http.Request) (any, error) {
	d, err := s.deviceFor(r)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	split := now.Truncate(24 * time.Hour).Add(-24 * time.Hour)
	out := newForJSON{Since: split.Format(time.RFC3339), Apps: []newAppJSON{}, Countries: []newCountryJSON{}}
	if !d.First.IsZero() {
		out.KnownDays = int(now.Sub(d.First) / (24 * time.Hour))
	}
	if d.First.IsZero() || now.Sub(d.First) < newMinKnown {
		out.TooNew = true
		return out, nil
	}
	from := split.Add(-newLookBack)
	cat := s.catalog()
	for _, n := range s.h.NewKeys(d.MAC, "app", split, from, now) {
		// The catch-alls are not news.
		if n.Key == serviceKey || n.Key == enrich.Unknown.ID {
			continue
		}
		out.Apps = append(out.Apps, newAppJSON{App: cat.value(n.Key), First: ts(n.First), Down: n.Bytes.Down, Up: n.Bytes.Up})
	}
	for _, n := range s.h.NewKeys(d.MAC, "cc", split, from, now) {
		// Nor is the house's own traffic, or an address with no country.
		if n.Key == lanCC || n.Key == "--" || n.Key == "" || n.Key == serviceKey {
			continue
		}
		out.Countries = append(out.Countries, newCountryJSON{CC: n.Key, First: ts(n.First), Down: n.Bytes.Down, Up: n.Bytes.Up})
	}
	return out, nil
}
