package api

// A device's last 30 days, from the daily files: for the screens that compare
// a device with its own usual (the week grid, rhythm and drift).

import (
	"net"
	"net/http"
	"time"

	"netryx/internal/daily"
)

// Daily is the store of daily files the API reads; daily.Store satisfies it.
type Daily interface {
	Days(mac [6]byte, now time.Time) []daily.DayOf
}

// WithDaily serves a device's days from d. Without it every day is unknown.
func WithDaily(d Daily) Option { return func(s *server) { s.daily = d } }

// dayJSON is one day: unknown days carry no hours; in a known day an hour the
// router was not counting is null, never zero.
type dayJSON struct {
	Date  string    `json:"date"`
	Known bool      `json:"known"`
	Mask  uint32    `json:"mask"`
	Down  []*uint64 `json:"down"`
	Up    []*uint64 `json:"up"`
}

type daysJSON struct {
	Unit string    `json:"unit"`
	Days []dayJSON `json:"days"`
}

func (s *server) days(r *http.Request) (any, error) {
	if _, err := s.deviceFor(r); err != nil {
		return nil, err
	}
	hw, _ := net.ParseMAC(r.PathValue("mac"))
	if len(hw) != 6 {
		return nil, httpError{http.StatusBadRequest, "mac must be a MAC address"}
	}
	now := s.now()
	var days []daily.DayOf
	if s.daily != nil {
		days = s.daily.Days([6]byte(hw), now)
	} else {
		for _, d := range daily.Window(now) {
			days = append(days, daily.DayOf{Date: d})
		}
	}
	out := daysJSON{Unit: "bytes", Days: make([]dayJSON, 0, len(days))}
	for _, d := range days {
		j := dayJSON{Date: d.Date.UTC().Format("2006-01-02"), Known: d.Known}
		if d.Known {
			j.Mask = d.Mask
			j.Down, j.Up = make([]*uint64, 24), make([]*uint64, 24)
			for h := 0; h < 24; h++ {
				if d.Mask&(1<<h) != 0 {
					down, up := d.Down[h], d.Up[h]
					j.Down[h], j.Up[h] = &down, &up
				}
			}
		}
		out.Days = append(out.Days, j)
	}
	return out, nil
}
