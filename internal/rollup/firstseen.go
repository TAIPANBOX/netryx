package rollup

import (
	"iter"
	"time"
)

// FirstSeen is when the history first saw a device at or after from: the
// start of the first hour it moved bytes in, or, before the hour ring's
// reach, the start of its first day; zero when the history holds none of it.
// Unlike the inventory's first sight it outlives a restart.
func (s *Store) FirstSeen(mac string, from time.Time) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	from, now := from.UTC(), s.now().UTC()
	var first time.Time
	earliest := func(l level, lo time.Time) {
		s.eachKeys(l, lo, now, func(at time.Time, keys iter.Seq2[Key, Bytes]) {
			if !first.IsZero() && !at.Before(first) {
				return
			}
			for k := range keys {
				if k.MAC == mac {
					first = at
					return
				}
			}
		})
	}
	earliest(days, from.Truncate(day))
	if !first.IsZero() && first.Before(from) {
		first = from.Truncate(time.Hour)
	}
	// The hour ring says which hour of that day, where it reaches back to it.
	day0 := first
	first = time.Time{}
	earliest(hours, from.Truncate(time.Hour))
	if first.IsZero() || (!day0.IsZero() && first.Sub(day0) >= day) {
		return day0
	}
	return first
}
