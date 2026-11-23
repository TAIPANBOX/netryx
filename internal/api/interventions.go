package api

// A device's interventions: who let it through, which limit ran out, who let
// it out of quarantine or isolated it, which rule blocked it and who cut its
// connection, from the journal (newest first).

import (
	"net/http"
	"strings"
	"time"

	"netryx/internal/journal"
)

// Journal is the interventions on the house's devices; journal.Store
// satisfies it.
type Journal interface {
	Of(mac, group string, since time.Time) []journal.Entry
	Since() time.Time
	Skipped() int
	Add(journal.Entry) (bool, error)
}

// WithJournal serves a device's interventions from j and journals the
// connections the owner cuts with the button.
func WithJournal(j Journal) Option { return func(s *server) { s.journal = j } }

// maxInterventions is how many of a device's interventions one answer lists.
const maxInterventions = 100

type interventionJSON struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Rule   string `json:"rule,omitempty"`
	Name   string `json:"name,omitempty"`
	By     string `json:"by,omitempty"`
	Until  string `json:"until,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Group is the group an intervention was about, when it was not the
	// device itself.
	Group string `json:"group,omitempty"`
}

type interventionsJSON struct {
	// On says the router keeps a journal; Since is its oldest intervention.
	On      bool               `json:"on"`
	Since   string             `json:"since,omitempty"`
	Skipped int                `json:"skipped"`
	Entries []interventionJSON `json:"entries"`
}

func (s *server) interventions(r *http.Request) (any, error) {
	d, err := s.deviceFor(r)
	if err != nil {
		return nil, err
	}
	out := interventionsJSON{Entries: []interventionJSON{}}
	if s.journal == nil {
		return out, nil
	}
	out.On, out.Skipped = true, s.journal.Skipped()
	if t := s.journal.Since(); !t.IsZero() {
		out.Since = ts(t)
	}
	for _, e := range s.journal.Of(d.MAC, d.Group, time.Time{}) {
		if len(out.Entries) == maxInterventions {
			break
		}
		j := interventionJSON{At: ts(e.At), Kind: e.Kind, Rule: e.Rule, Name: e.Name, By: e.By, Detail: e.Detail}
		if !e.Until.IsZero() {
			j.Until = ts(e.Until)
		}
		if !strings.EqualFold(e.MAC, d.MAC) {
			j.Group = e.MAC
		}
		out.Entries = append(out.Entries, j)
	}
	return out, nil
}

// journalCut writes a connection the owner cut, with whether it was held.
func (s *server) journalCut(mac string, proto uint8, remote, app, note string) {
	if s.journal == nil {
		return
	}
	p := "tcp"
	if proto == 17 {
		p = "udp"
	}
	d := p + " " + remote
	if app != "" {
		d += " (" + app + ")"
	}
	if note != "" {
		d += ", not held: " + note
	}
	if len(d) > 128 {
		d = d[:125] + "..."
	}
	s.journal.Add(journal.Entry{Kind: journal.Cut, MAC: mac, By: "owner", Detail: d})
}
