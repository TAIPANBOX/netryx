package rules

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxPending bounds the console's list of devices in quarantine, Grace is how
// long after the baseline was taken a device never seen joins it instead of
// being held (spec 6.8: "known on the day 2c is installed"), and pendingKeep
// how long a device in quarantine that is no longer seen stays listed.
const (
	MaxPending  = 1024
	Grace       = 24 * time.Hour
	pendingKeep = 7 * 24 * time.Hour
)

// ErrClockNotSet says the clock is before ClockFloor: no baseline is taken
// until NTP has answered.
var ErrClockNotSet = errors.New("the router's clock is not set yet: quarantine is waiting")

// ClockFloor is the earliest clock a baseline is taken at.
var ClockFloor = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// ErrBaselineRebuilt says the baseline file was damaged and taken again, with
// a new grace.
var ErrBaselineRebuilt = errors.New("the list of known devices was damaged and rebuilt with a 24-hour grace")

// Seen is a device the router sees on the house side.
type Seen struct {
	MAC, Hostname string
	At            time.Time
}

// Pending is a device in quarantine, for the console. LikelySame is the MAC of
// an approved device with the same hostname: a phone that rotates its address
// comes back as new, and this names it.
type Pending struct {
	MAC, Hostname string
	First, Last   time.Time
	LikelySame    string
	// withSame says the device was seen on the network together with the
	// known device of its hostname, so it cannot be that device.
	withSame bool
}

// Roster is the quarantine's memory: the baseline of MACs known when 2c first
// ran (<state>/known-macs.json, written by rename) and the devices held since.
// Enforcement does not wait for it: the kernel holds every MAC that is not in
// Approved from its first frame; the roster only says who is approved and
// lists who is held.
type Roster struct {
	path string
	// heldPath keeps the MACs held since the baseline (held-macs.json beside
	// it): a held device has the internet, so it is in the history and the
	// inventory, and a rebuilt baseline must leave it out.
	heldPath string
	now      func() time.Time

	mu      sync.Mutex
	created time.Time
	known   map[string]bool
	good    bool // the baseline was read or written
	pending map[string]*Pending
	names   map[string]string // lower-case hostname -> approved MAC
	full    bool              // the overflow was reported
	held    map[string]bool   // held since the baseline, until approved
	present map[string]bool   // the MACs of the latest See
}

type rosterFile struct {
	Version int      `json:"version"`
	Created int64    `json:"created"`
	MACs    []string `json:"macs"`
}

// NewRoster returns a roster over path; nothing is read until Baseline.
func NewRoster(path string, now func() time.Time) *Roster {
	if now == nil {
		now = time.Now
	}
	return &Roster{path: path, heldPath: filepath.Join(filepath.Dir(path), "held-macs.json"), now: now,
		known: map[string]bool{}, pending: map[string]*Pending{}, names: map[string]string{},
		held: map[string]bool{}, present: map[string]bool{}}
}

// Baseline reads the baseline, or takes it from known when there is none. A
// damaged file is rewritten from known with a new grace, so a device asleep at
// that moment joins it rather than being held (the house is never put in
// quarantine by a bad file); the error says so.
func (q *Roster) Baseline(known []string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.loadHeld()
	b, err := os.ReadFile(q.path)
	switch {
	case err == nil:
		var f rosterFile
		if jerr := json.Unmarshal(b, &f); jerr == nil && f.Version == 1 && f.Created > 0 {
			q.created = time.Unix(f.Created, 0)
			q.known = map[string]bool{}
			for _, m := range f.MACs {
				q.add(m)
			}
			q.good = true
			return nil
		}
		return errors.Join(ErrBaselineRebuilt, q.fresh(known))
	case errors.Is(err, fs.ErrNotExist):
		return q.fresh(known)
	default:
		// Unreadable: hold nobody until it can be read.
		return err
	}
}

// fresh takes a new baseline from known, starting its grace now. Not before
// the clock is set: a fresh image starts from its build time, and a grace
// counted from it would end at the NTP jump. A good file is read whatever
// the clock says.
func (q *Roster) fresh(known []string) error {
	if q.now().Before(ClockFloor) {
		q.good = false
		return ErrClockNotSet
	}
	q.created = q.now()
	q.known = map[string]bool{}
	for _, m := range known {
		if mac, ok := normMAC(m); ok && !q.held[mac] {
			q.add(mac)
		}
	}
	if err := q.save(); err != nil {
		q.good = false
		return err
	}
	q.good = true
	return nil
}

func (q *Roster) add(m string) bool {
	mac, ok := normMAC(m)
	if !ok || q.known[mac] || len(q.known) >= MaxApproved {
		return false
	}
	q.known[mac] = true
	return true
}

func (q *Roster) save() error {
	macs := sortedKeys(q.known)
	b, err := json.Marshal(rosterFile{Version: 1, Created: q.created.Unix(), MACs: macs})
	if err != nil {
		return err
	}
	return writeFile(q.path, b)
}

// inGrace is the day after the baseline was taken, on a set clock: a clock
// set back (a reboot before NTP) never reopens it.
func (q *Roster) inGrace(now time.Time) bool {
	return !now.Before(ClockFloor) && !now.Before(q.created) && now.Before(q.created.Add(Grace))
}

func (q *Roster) loadHeld() {
	q.held = map[string]bool{}
	b, err := os.ReadFile(q.heldPath)
	if err != nil {
		return
	}
	var macs []string
	if json.Unmarshal(b, &macs) != nil {
		return
	}
	for _, m := range macs {
		if mac, ok := normMAC(m); ok && len(q.held) < MaxApproved {
			q.held[mac] = true
		}
	}
}

func (q *Roster) saveHeld() {
	b, err := json.Marshal(sortedKeys(q.held))
	if err == nil {
		_ = writeFile(q.heldPath, b)
	}
}

// randomised says a MAC is locally administered, as a phone's per-network
// or rotating address is.
func randomised(mac string) bool {
	if len(mac) < 2 {
		return false
	}
	b, err := strconv.ParseUint(mac[:2], 16, 8)
	return err == nil && b&0x02 != 0
}

// See takes the devices the router sees now and the MACs approved in UCI.
// During the grace a device never seen joins the baseline; after it, one in
// neither is a newcomer, reported once. full says the list overflowed, once.
func (q *Roster) See(devs []Seen, approved []string) (newcomers []Seen, full bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.good {
		return nil, false
	}
	now := q.now()
	ok := map[string]bool{}
	heldChanged := false
	for _, m := range approved {
		if mac, valid := normMAC(m); valid {
			ok[mac] = true
			delete(q.pending, mac)
			if q.held[mac] {
				delete(q.held, mac)
				heldChanged = true
			}
		}
	}
	q.present = map[string]bool{}
	for _, d := range devs {
		if mac, valid := normMAC(d.MAC); valid {
			q.present[mac] = true
		}
	}
	grew := false
	sameHere := func(host string) bool { m := q.names[host]; return m != "" && q.present[m] }
	for _, d := range devs {
		mac, valid := normMAC(d.MAC)
		if !valid {
			continue
		}
		if d.At.IsZero() || d.At.After(now) {
			// Seen in this look: a device known only from a lease has no time.
			d.At = now
		}
		host := strings.ToLower(strings.TrimSpace(d.Hostname))
		if q.known[mac] || ok[mac] {
			if host != "" {
				q.names[host] = mac
			}
			continue
		}
		if q.inGrace(now) && !q.held[mac] {
			grew = q.add(mac) || grew
			if host != "" {
				q.names[host] = mac
			}
			continue
		}
		if p := q.pending[mac]; p != nil {
			p.Last = d.At
			if d.Hostname != "" {
				p.Hostname = d.Hostname
			}
			p.withSame = p.withSame || sameHere(host)
			continue
		}
		if len(q.pending) >= MaxPending && !q.makeRoom(now) {
			if !q.full {
				q.full, full = true, true
			}
			continue
		}
		q.pending[mac] = &Pending{MAC: mac, Hostname: d.Hostname, First: d.At, Last: d.At, withSame: sameHere(host)}
		if !q.held[mac] && len(q.held) < MaxApproved {
			q.held[mac] = true
			heldChanged = true
		}
		newcomers = append(newcomers, Seen{MAC: mac, Hostname: d.Hostname, At: d.At})
	}
	for mac, p := range q.pending {
		if now.Sub(p.Last) > pendingKeep {
			delete(q.pending, mac)
		}
	}
	if len(q.pending) < MaxPending {
		q.full = false
	}
	if grew {
		_ = q.save()
	}
	if heldChanged {
		q.saveHeld()
	}
	return newcomers, full
}

// pendingStale is how long a listed device must have gone unseen before a
// full list lets it go for a newcomer.
const pendingStale = time.Hour

// makeRoom lets go of the listed device seen longest ago, if it has gone
// unseen for pendingStale: a flood that has stopped makes room for a real
// newcomer. The kernel holds every device not approved either way.
func (q *Roster) makeRoom(now time.Time) bool {
	oldest := ""
	for mac, p := range q.pending {
		if now.Sub(p.Last) > pendingStale && (oldest == "" || p.Last.Before(q.pending[oldest].Last)) {
			oldest = mac
		}
	}
	if oldest == "" {
		return false
	}
	delete(q.pending, oldest)
	return true
}

// Approved is the baseline plus the approvals in UCI, for the kernel's set.
func (q *Roster) Approved(uciApproved []string) []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	all := map[string]bool{}
	for m := range q.known {
		all[m] = true
	}
	for _, m := range uciApproved {
		if mac, ok := normMAC(m); ok {
			all[mac] = true
		}
	}
	return sortedKeys(all)
}

// Active says the quarantine holds: it is enabled, the baseline is good and
// its grace has ended.
func (q *Roster) Active(enabled bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return enabled && q.good && !q.inGrace(q.now())
}

// Good says the baseline was read or written.
func (q *Roster) Good() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.good
}

// HeldInGrace is the MACs held before that the kernel must still keep from
// the house while the quarantine lines are off for a grace (after a damaged
// baseline was rebuilt): they are isolated by name instead. Empty when the
// quarantine is off or holds by its own lines.
func (q *Roster) HeldInGrace(enabled bool) []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !enabled || !q.good || !q.inGrace(q.now()) {
		return nil
	}
	return sortedKeys(q.held)
}

// GraceEnds is when the quarantine starts holding (zero before a baseline).
func (q *Roster) GraceEnds() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.good {
		return time.Time{}
	}
	return q.created.Add(Grace)
}

// Pending is the devices in quarantine, most recently seen first.
func (q *Roster) Pending() []Pending {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Pending, 0, len(q.pending))
	for _, p := range q.pending {
		c := *p
		// The hostname is what the device says about itself: it names a
		// likely device only for a randomised MAC, and only while that device
		// is not on the network at the same time.
		if host := strings.ToLower(strings.TrimSpace(c.Hostname)); host != "" && randomised(c.MAC) {
			if same := q.names[host]; same != "" && !c.withSame && !q.present[same] {
				c.LikelySame = same
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Last.Equal(out[j].Last) {
			return out[i].Last.After(out[j].Last)
		}
		return out[i].MAC < out[j].MAC
	})
	return out
}
