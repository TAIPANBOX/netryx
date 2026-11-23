package api

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"netryx/internal/enrich"
	"netryx/internal/flows"
	"netryx/internal/inventory"
	"netryx/internal/journal"
)

type fakeJournal struct {
	entries []journal.Entry
	added   []journal.Entry
	since   time.Time
	skipped int
	asked   [][2]string
}

func (f *fakeJournal) Of(mac, group string, since time.Time) []journal.Entry {
	f.asked = append(f.asked, [2]string{mac, group})
	return f.entries
}
func (f *fakeJournal) Since() time.Time { return f.since }
func (f *fakeJournal) Skipped() int     { return f.skipped }
func (f *fakeJournal) Add(e journal.Entry) (bool, error) {
	f.added = append(f.added, e)
	return true, nil
}

// A device's interventions come newest first, each with when, what, which
// rule, who did it and until when; one about the device's group says so.
func TestADeviceAnswersItsInterventions(t *testing.T) {
	fl, h, _, w, ap := fixture()
	inv := fakeInv{devs: []inventory.Device{{MAC: laptop, Name: "Laptop", Group: "kids", First: now0.Add(-time.Hour), Last: now0}}}
	fj := &fakeJournal{since: now0.Add(-48 * time.Hour), skipped: 2, entries: []journal.Entry{
		{At: now0.Add(-time.Minute), Kind: journal.Allow, ID: "agent_a1", MAC: "kids", Rule: "games", Name: "roblox", By: "agent", Until: now0.Add(29 * time.Minute)},
		{At: now0.Add(-time.Hour), Kind: journal.Blocked, MAC: laptop, Rule: "tiktok", Name: "tiktok", By: "owner"},
	}}
	srv := New(fl, h, inv, w, ap, clock, nil, WithJournal(fj))
	rec := get(t, srv, http.MethodGet, "/api/devices/"+laptop+"/interventions")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v struct {
		On      bool   `json:"on"`
		Since   string `json:"since"`
		Skipped int    `json:"skipped"`
		Entries []struct {
			At, Kind, Rule, Name, By, Until, Detail, Group string
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !v.On || v.Since != "2026-11-18T12:00:00Z" || v.Skipped != 2 || len(v.Entries) != 2 {
		t.Fatalf("%s", rec.Body)
	}
	a := v.Entries[0]
	if a.Kind != "allow" || a.By != "agent" || a.Rule != "games" || a.Name != "roblox" || a.Until != "2026-11-20T12:29:00Z" || a.Group != "kids" {
		t.Errorf("the group's allowance: %+v", a)
	}
	if b := v.Entries[1]; b.Kind != "blocked" || b.Group != "" || b.Until != "" {
		t.Errorf("a block: %+v", b)
	}
	if len(fj.asked) != 1 || fj.asked[0] != [2]string{laptop, "kids"} {
		t.Errorf("asked for %v", fj.asked)
	}
	// Without the journal it says it keeps none, never an empty history.
	rec = get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/devices/"+laptop+"/interventions")
	v.On = true
	json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != 200 || v.On {
		t.Errorf("without the journal: %d %s", rec.Code, rec.Body)
	}
	if rec := get(t, srv, http.MethodGet, "/api/devices/02:00:00:00:00:99/interventions"); rec.Code != 404 {
		t.Errorf("an unknown device: %d", rec.Code)
	}
}

// A connection the owner cut with the button is journalled, with what was
// cut and whether the router could hold it.
func TestAButtonCutIsJournalled(t *testing.T) {
	fr := house()
	fl, h, inv, w, ap := fixture()
	fj := &fakeJournal{}
	srv := New(fl, h, inv, w, ap, clock, nil, WithRules(fr), WithJournal(fj))
	fl.live = append(fl.live, flows.Flow{
		MAC: phone, Local: netip.MustParseAddrPort("192.168.1.20:40000"),
		Remote: netip.MustParseAddrPort("149.154.167.50:443"), Proto: 6, Dir: flows.Out,
		App: enrich.App{ID: "telegram"},
	})
	rec := send(t, srv, http.MethodPost, "/api/devices/"+phone+"/conns/cut", `{"local":"192.168.1.20:40000","remote":"149.154.167.50:443","proto":6}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(fj.added) != 1 || fj.added[0].Kind != journal.Cut || fj.added[0].MAC != phone || fj.added[0].By != "owner" || fj.added[0].Detail != "tcp 149.154.167.50:443 (telegram)" {
		t.Errorf("journalled %+v", fj.added)
	}
	fr.cutNote = "the set is full"
	send(t, srv, http.MethodPost, "/api/devices/"+phone+"/conns/cut", `{"local":"192.168.1.20:40000","remote":"149.154.167.50:443","proto":6}`)
	if len(fj.added) != 2 || fj.added[1].Detail != "tcp 149.154.167.50:443 (telegram), not held: the set is full" {
		t.Errorf("a cut not held: %+v", fj.added)
	}
}
