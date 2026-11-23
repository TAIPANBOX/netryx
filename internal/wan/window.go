package wan

import "time"

// maxWindowSamples bounds the readings kept: at one every 10 s, a little more
// than the longest window the console offers (10 minutes).
const maxWindowSamples = 64

type sample struct {
	at     time.Time     // the reading's end
	dt     time.Duration // the seconds it covers, back from at
	rx, tx float64       // bytes per second over them
}

// Window keeps the line's recent readings, so the console shows its average
// over a window rather than the last 10 s, which jumps with every reading.
type Window struct {
	s    []sample
	last time.Time
}

// Add records a reading's rates at at; the first reading, and the first after
// the clock went back, only start the clock.
func (w *Window) Add(at time.Time, rx, tx float64) {
	if w.last.IsZero() || !at.After(w.last) {
		if !w.last.IsZero() && at.Before(w.last) {
			w.s = nil
		}
		w.last = at
		return
	}
	w.s = append(w.s, sample{at: at, dt: at.Sub(w.last), rx: rx, tx: tx})
	w.last = at
	if len(w.s) > maxWindowSamples {
		w.s = append(w.s[:0], w.s[len(w.s)-maxWindowSamples:]...)
	}
}

// Average is the line's rates over the span back from now, each reading
// weighted by the part of its seconds inside the span, and the time covered
// (less than span while fewer readings exist; 0 with none).
func (w *Window) Average(now time.Time, span time.Duration) (rx, tx float64, covered time.Duration) {
	start := now.Add(-span)
	var brx, btx float64
	for _, s := range w.s {
		from := s.at.Add(-s.dt)
		if !s.at.After(start) || s.at.After(now) {
			continue
		}
		if from.Before(start) {
			from = start
		}
		d := s.at.Sub(from)
		brx += s.rx * d.Seconds()
		btx += s.tx * d.Seconds()
		covered += d
	}
	if covered <= 0 {
		return 0, 0, 0
	}
	return brx / covered.Seconds(), btx / covered.Seconds(), covered
}
