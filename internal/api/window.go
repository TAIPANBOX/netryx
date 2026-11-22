package api

// The live columns: the average over a short sliding window (one, three or
// ten minutes), from the minutes the router keeps, so a figure does not jump
// with every two-second sample.

import (
	"net/http"
	"time"

	"netryx/internal/rollup"
)

var windows = map[string]time.Duration{"1m": time.Minute, "3m": 3 * time.Minute, "10m": 10 * time.Minute}

// defaultWindow is the window when the page names none.
const defaultWindow = 3 * time.Minute

func windowOf(r *http.Request) (time.Duration, error) {
	v := r.URL.Query().Get("window")
	if v == "" {
		return defaultWindow, nil
	}
	w, ok := windows[v]
	if !ok {
		return 0, httpError{http.StatusBadRequest, "window must be 1m, 3m or 10m"}
	}
	return w, nil
}

// recent is each key's average rate over the window, bytes per second, and
// the seconds the window covers (0 before the router has a minute).
func (s *server) recent(r *http.Request, f rollup.Filter, dim string) (map[string]rateJSON, float64, error) {
	w, err := windowOf(r)
	if err != nil {
		return nil, 0, err
	}
	rows, covered := s.h.Recent(f, dim, w)
	out := map[string]rateJSON{}
	sec := covered.Seconds()
	if sec <= 0 {
		return out, 0, nil
	}
	for _, row := range rows {
		out[row.Key] = rateJSON{Up: float64(row.Bytes.Up) / sec, Down: float64(row.Bytes.Down) / sec}
	}
	return out, sec, nil
}
