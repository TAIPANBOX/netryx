package flows

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Query selects flows for the console. Empty fields match everything.
type Query struct {
	MAC, App, CC, Text string // App is the app's id; Text searches name, MAC, remote address and app name
	Dir                Direction
	Live               bool // read the live table instead of the journal
	From, To           time.Time
	Limit              int    // 100 when zero, never more than 500
	Cursor             string // the next value of the previous page
	// Window, with Live, ranks the live flows by the bytes each moved over
	// the last minutes (WinUp and WinDown) instead of newest first.
	Window time.Duration
	// ASN, when not zero, keeps the flows to that network.
	ASN uint32
}

const (
	defaultLimit = 100
	maxLimit     = 500
)

// journal is a ring of finished flows. Each row's sequence number is its
// position in the order flows finished, so the row for seq s is at s modulo
// the ring's size while it is still held, and a cursor is just a number.
type journal struct {
	ring []Flow
	next uint64 // seq the next row will get; rows held are [next-len, next)
}

func (j *journal) push(f Flow) {
	if len(j.ring) == 0 {
		return
	}
	j.ring[j.next%uint64(len(j.ring))] = f
	j.next++
}

// oldest is the smallest seq still in the ring.
func (j *journal) oldest() uint64 {
	if n := uint64(len(j.ring)); j.next > n {
		return j.next - n
	}
	return 0
}

// Query returns flows newest first and the cursor for the page after, empty
// when there is none. A cursor this table did not give out finds nothing,
// rather than quietly starting again from the newest.
func (t *Table) Query(q Query) (rows []Flow, next string) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	var cursor uint64
	if q.Cursor != "" {
		c, err := strconv.ParseUint(q.Cursor, 10, 64)
		if err != nil || c == 0 {
			return nil, ""
		}
		cursor = c
	}
	m := newMatcher(q)

	t.mu.Lock()
	defer t.mu.Unlock()

	// Each source yields (seq, flow) newest first; one more match than the
	// page holds says whether there is a next page.
	take := func(seq uint64, f *Flow) bool {
		if !m.match(f) {
			return true
		}
		if len(rows) == limit {
			next = strconv.FormatUint(seq+1, 10)
			return false
		}
		rows = append(rows, *f)
		return true
	}
	if q.Live && q.Window > 0 {
		// Ranked by recent volume, the whole page at once: the order of the
		// bytes moved has no stable cursor, so there is no next page.
		type ranked struct {
			en       *entry
			up, down uint64
		}
		var all []ranked
		for _, en := range t.live {
			t.resolve(en)
			if !m.match(&en.Flow) {
				continue
			}
			up, down := en.winBytes(t.lastSync, q.Window)
			all = append(all, ranked{en, up, down})
		}
		sort.Slice(all, func(i, j int) bool {
			a, b := all[i].up+all[i].down, all[j].up+all[j].down
			if a != b {
				return a > b
			}
			return all[i].en.seq > all[j].en.seq
		})
		for _, r := range all {
			if len(rows) == limit {
				break
			}
			f := r.en.Flow
			f.WinUp, f.WinDown = r.up, r.down
			rows = append(rows, f)
		}
		return rows, ""
	}
	if q.Live {
		for _, en := range t.liveNewestFirst(cursor) {
			// A live flow's device may have been learned since it opened. A
			// journal row is never asked again: its address may belong to
			// another device by now.
			t.resolve(en)
			if !take(en.seq, &en.Flow) {
				break
			}
		}
		return rows, next
	}
	j := &t.j
	if j.next == 0 {
		return nil, ""
	}
	top := j.next // exclusive
	if cursor != 0 && cursor < top {
		top = cursor
	}
	for s := top; s > j.oldest(); s-- {
		seq := s - 1
		if !take(seq, &j.ring[seq%uint64(len(j.ring))]) {
			break
		}
	}
	return rows, next
}

// matcher is a Query with its text lowered once rather than per row.
type matcher struct {
	q    Query
	text string
}

func newMatcher(q Query) matcher { return matcher{q: q, text: strings.ToLower(q.Text)} }

func (m matcher) match(f *Flow) bool {
	q := m.q
	if q.MAC != "" && !strings.EqualFold(f.MAC, q.MAC) {
		return false
	}
	if q.App != "" && !strings.EqualFold(f.App.ID, q.App) {
		return false
	}
	if q.CC != "" && !strings.EqualFold(f.Geo.CC, q.CC) {
		return false
	}
	if q.Dir != "" && f.Dir != q.Dir {
		return false
	}
	if q.ASN != 0 && f.Geo.ASN != q.ASN {
		return false
	}
	// A flow overlaps the window if it started before the window ends and
	// ended after it began; a live flow has not ended.
	if !q.To.IsZero() && f.Start.After(q.To) {
		return false
	}
	if !q.From.IsZero() && !f.End.IsZero() && f.End.Before(q.From) {
		return false
	}
	if m.text != "" {
		hay := []string{f.Name, f.MAC, f.Remote.Addr().String(), f.App.Name}
		for _, h := range hay {
			if strings.Contains(strings.ToLower(h), m.text) {
				return true
			}
		}
		return false
	}
	return true
}
