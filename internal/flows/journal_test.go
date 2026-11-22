package flows

import (
	"testing"
	"time"

	"netryx/internal/ct"
)

func finish(tb *Table, e ct.Event, at time.Time) {
	e.Kind = ct.New
	tb.Apply(e, at)
	e.Kind = ct.Destroy
	tb.Apply(e, at.Add(time.Second))
}

func ids(rows []Flow) []uint32 {
	out := []uint32{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func TestJournalKeepsTheNewestAndPagesBackwards(t *testing.T) {
	f := newFakes()
	o := f.options()
	o.MaxJournal = 4
	tb := NewTable(o)
	for id := uint32(1); id <= 6; id++ {
		finish(tb, watch(id, 10, 10), t0.Add(time.Duration(id)*time.Minute))
	}
	rows, next := tb.Query(Query{Limit: 2})
	if got := ids(rows); len(got) != 2 || got[0] != 6 || got[1] != 5 {
		t.Fatalf("the newest come first: %v", got)
	}
	if next == "" {
		t.Fatal("and there is a next page")
	}
	rows, next = tb.Query(Query{Limit: 2, Cursor: next})
	if got := ids(rows); len(got) != 2 || got[0] != 4 || got[1] != 3 {
		t.Fatalf("the cursor carries on where the last page stopped: %v", got)
	}
	if next != "" {
		t.Fatalf("the two oldest were pushed out of the ring, so there is nothing after: %q", next)
	}
	if rows[0].End.IsZero() || rows[0].Count != 1 {
		t.Fatalf("a journaled flow says when it ended: %+v", rows[0])
	}
}

func TestQueryFiltersByTextAppCountryAndDirection(t *testing.T) {
	f := newFakes()
	f.name(phone, youtube, "www.YouTube.com", "sni")
	tb := NewTable(f.options())
	finish(tb, event(ct.New, 1, phone, 51001, youtube, 443, 10, 10), t0)                   // youtube, US, out
	finish(tb, event(ct.New, 2, laptop, 51002, youtube, 443, 10, 10), t0.Add(time.Second)) // google by org, US, out
	finish(tb, event(ct.New, 3, elsewhere, 40000, nas, 22, 10, 10), t0.Add(2*time.Second)) // unknown, GB, in
	finish(tb, event(ct.New, 4, laptop, 52000, nas, 445, 10, 10), t0.Add(3*time.Second))   // local, LAN
	tb.Apply(event(ct.New, 5, phone, 51005, elsewhere, 443, 0, 0), t0.Add(4*time.Second))  // still live

	cases := []struct {
		name string
		q    Query
		want []uint32
	}{
		{"text matches the name whatever its case", Query{Text: "youtube"}, []uint32{1}},
		{"text matches the hardware address", Query{Text: "4E:C2"}, []uint32{1}},
		{"text matches the remote address", Query{Text: "81.2.69"}, []uint32{3}},
		{"text matches the app's name", Query{Text: "googl"}, []uint32{2}},
		{"app is the app's id", Query{App: "google"}, []uint32{2}},
		{"country", Query{CC: "gb"}, []uint32{3}},
		{"a local flow is LAN, not a place", Query{CC: "LAN"}, []uint32{4}},
		{"direction", Query{Dir: In}, []uint32{3}},
		{"direction local", Query{Dir: Local}, []uint32{4}},
		{"mac", Query{MAC: phoneMAC}, []uint32{1}},
		{"filters combine", Query{CC: "US", Dir: Out, App: "youtube"}, []uint32{1}},
		{"time window", Query{From: t0.Add(1500 * time.Millisecond), To: t0.Add(2500 * time.Millisecond)}, []uint32{3, 2}},
		{"live reads the live table", Query{Live: true}, []uint32{5}},
		{"live filters too", Query{Live: true, Text: "youtube"}, []uint32{}},
	}
	for _, c := range cases {
		rows, _ := tb.Query(c.q)
		got := ids(rows)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

func TestAPageIsNeverLargerThanFiveHundred(t *testing.T) {
	f := newFakes()
	o := f.options()
	o.MaxJournal, o.MaxLive = 1000, 1000
	tb := NewTable(o)
	for id := uint32(1); id <= 600; id++ {
		finish(tb, watch(id, 1, 1), t0)
	}
	rows, next := tb.Query(Query{Limit: 100000})
	if len(rows) != 500 || next == "" {
		t.Fatalf("a page is capped at 500 and says there is more: %d %q", len(rows), next)
	}
	rows, next = tb.Query(Query{Limit: 100000, Cursor: next})
	if len(rows) != 100 || next != "" || rows[99].ID != 1 {
		t.Fatalf("and the rest is the next page: %d %q", len(rows), next)
	}
	if rows, next := tb.Query(Query{Cursor: "not a cursor"}); len(rows) != 0 || next != "" {
		t.Fatalf("a cursor this table never gave out finds nothing: %d %q", len(rows), next)
	}
}
