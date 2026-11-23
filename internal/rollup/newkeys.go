package rollup

import (
	"iter"
	"sort"
	"time"
)

// NewRow is a key a device used since a split and never before it: when it
// was first seen (the start of that hour) and what it moved since.
type NewRow struct {
	Key   string
	First time.Time
	Bytes Bytes
}

// NewKeys answers, for one device, the apps (dim "app") or countries (dim
// "cc") it used at or after split and in no day from from to split; the days
// before are read from the day ring, the days since from the hour ring. Rows
// come oldest first; any other dimension answers nil.
func (s *Store) NewKeys(mac, dim string, split, from, now time.Time) []NewRow {
	if dim != "app" && dim != "cc" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	split, from, now = split.UTC(), from.UTC(), now.UTC()
	seen := map[string]bool{}
	s.eachKeys(days, from.Truncate(day), split.Add(-time.Nanosecond), func(_ time.Time, keys iter.Seq2[Key, Bytes]) {
		for k := range keys {
			if k.MAC == mac {
				v, _ := splitKey(k, dim)
				seen[v] = true
			}
		}
	})
	rows := map[string]*NewRow{}
	s.eachKeys(hours, split, now, func(at time.Time, keys iter.Seq2[Key, Bytes]) {
		for k, b := range keys {
			if k.MAC != mac {
				continue
			}
			v, _ := splitKey(k, dim)
			if seen[v] {
				continue
			}
			r := rows[v]
			if r == nil {
				r = &NewRow{Key: v, First: at}
				rows[v] = r
			}
			if at.Before(r.First) {
				r.First = at
			}
			r.Bytes = add(r.Bytes, b)
		}
	})
	out := make([]NewRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].First.Equal(out[j].First) {
			return out[i].First.Before(out[j].First)
		}
		return out[i].Key < out[j].Key
	})
	return out
}
