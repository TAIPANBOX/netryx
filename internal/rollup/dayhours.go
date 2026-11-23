package rollup

import "time"

// DayHours is one closed UTC day as the hour ring holds it: for every key of
// its hours (a hardware address, or a catch-all such as "router" or "other"),
// the bytes down and up of each hour, and which of the 24 hours the ring
// holds at all. An hour the ring does not hold is one the router was not
// counting (switched off, or the service stopped), never a quiet hour.
type DayHours struct {
	Day  time.Time
	Mask uint32
	// By maps a key to its hours: [0] down, [1] up, each indexed by hour.
	By map[string]*[2][24]uint64
}

// DayHours answers the day starting at d's UTC midnight. It answers only a
// day that has closed by the store's clock and that the hour ring reaches
// back to whole, so a missing hour in the mask is the router's, not the
// ring's: otherwise it says false.
func (s *Store) DayHours(d time.Time) (DayHours, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d = d.UTC().Truncate(day)
	if !closed(d, day, s.now().UTC()) || !s.hrs.covers(d) {
		return DayHours{}, false
	}
	out := DayHours{Day: d, By: map[string]*[2][24]uint64{}}
	for h := 0; h < 24; h++ {
		bk := s.hrs.get(d.Add(time.Duration(h) * time.Hour))
		if bk == nil {
			continue
		}
		out.Mask |= 1 << h
		for k, b := range bk.keys() {
			v := out.By[k.MAC]
			if v == nil {
				v = &[2][24]uint64{}
				out.By[k.MAC] = v
			}
			v[0][h] += b.Down
			v[1][h] += b.Up
		}
	}
	return out, true
}
