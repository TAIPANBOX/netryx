package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A stretch picked on a chart (from, to) replaces the period: the series and
// every list read only that stretch, at the step of the shortest range that
// holds it, and the bucket that starts at to is not part of it.
func TestASpanReadsOnlyItsStretch(t *testing.T) {
	fl, h, inv, w, ap := fixture()
	from := now0.Add(-3 * time.Hour)
	to := now0.Add(-2 * time.Hour)
	q := "?range=24h&from=" + from.Format(time.RFC3339) + "&to=" + to.Format(time.RFC3339)
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/overview"+q)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(h.series) != 1 {
		t.Fatalf("%d series calls, want 1", len(h.series))
	}
	s := h.series[0]
	// An hour fits the 1 h range, so it is read at a minute's step.
	if s.step != time.Minute {
		t.Errorf("step %v, want 1m", s.step)
	}
	if !s.from.Equal(from) {
		t.Errorf("series from %v, want %v", s.from, from)
	}
	if want := to.Add(-time.Minute); !s.to.Equal(want) {
		t.Errorf("series to %v, want the last step inside the stretch %v", s.to, want)
	}
	if len(h.tops) == 0 {
		t.Fatal("no list was read")
	}
	for _, c := range h.tops {
		if !c.from.Equal(from) || !c.to.Before(to) || c.to.Before(to.Add(-time.Second)) {
			t.Errorf("%s read %v to %v, want %v up to just before %v", c.dim, c.from, c.to, from, to)
		}
	}
}

func TestAWiderSpanTakesTheStepOfTheRangeThatHoldsIt(t *testing.T) {
	cases := []struct {
		span time.Duration
		step time.Duration
	}{
		{30 * time.Minute, time.Minute},
		{time.Hour, time.Minute},
		{5 * time.Hour, 10 * time.Minute},
		{3 * 24 * time.Hour, time.Hour},
		{20 * 24 * time.Hour, 24 * time.Hour},
	}
	for _, c := range cases {
		fl, h, inv, w, ap := fixture()
		to := now0.Truncate(time.Hour)
		from := to.Add(-c.span)
		q := "?from=" + from.Format(time.RFC3339) + "&to=" + to.Format(time.RFC3339)
		rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/overview"+q)
		if rec.Code != http.StatusOK || len(h.series) != 1 {
			t.Fatalf("span %v: status %d, %d series", c.span, rec.Code, len(h.series))
		}
		if h.series[0].step != c.step {
			t.Errorf("span %v: step %v, want %v", c.span, h.series[0].step, c.step)
		}
	}
}

func TestASpanMustBeAStretchOfTheLastMonth(t *testing.T) {
	f := func(d time.Duration) string { return now0.Add(d).Format(time.RFC3339) }
	bad := []string{
		"from=" + f(-time.Hour),                                // to missing
		"to=" + f(-time.Hour),                                  // from missing
		"from=yesterday&to=" + f(0),                            // not a time
		"from=" + f(-time.Hour) + "&to=" + f(-2*time.Hour),     // backwards
		"from=" + f(-time.Hour) + "&to=" + f(-time.Hour),       // empty
		"from=" + f(-31*24*time.Hour) + "&to=" + f(-time.Hour), // older than the history
		"from=" + f(time.Hour) + "&to=" + f(2*time.Hour),       // in the future
	}
	for _, q := range bad {
		fl, h, inv, w, ap := fixture()
		rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/overview?"+q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, rec.Code)
		}
		if len(h.series) != 0 {
			t.Errorf("%s: the history was read", q)
		}
	}
	// A stretch that runs past now ends at now.
	fl, h, inv, w, ap := fixture()
	rec := get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/overview?from="+f(-30*time.Minute)+"&to="+f(10*time.Minute))
	if rec.Code != http.StatusOK {
		t.Fatalf("a stretch ending in the future: status %d, %s", rec.Code, rec.Body.String())
	}
	if got := h.series[0].to; got.After(now0) {
		t.Errorf("series ends at %v, after now %v", got, now0)
	}
	if !strings.Contains(get(t, New(fl, h, inv, w, ap, clock, nil), http.MethodGet, "/api/overview?from=x&to=y").Body.String(), "from and to") {
		t.Error("the refusal does not say what from and to must be")
	}
}
