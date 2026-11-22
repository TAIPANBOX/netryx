// Package flows turns the kernel's connection events into per-flow records a
// person can read, and into byte deltas a rollup can add up.
package flows

import (
	"net/netip"
	"time"

	"netryx/internal/ct"
	"netryx/internal/enrich"
)

// Direction says which side started the flow, seen from the house.
type Direction string

const (
	Out   Direction = "out"   // a device at home opened it
	In    Direction = "in"    // something outside opened it
	Local Direction = "local" // both ends are at home
)

// LANCountry is the country a flow with both ends at home carries: it never
// crossed the internet line, so it has no place and no organisation.
const LANCountry = "LAN"

// Flow is one connection as the console shows it.
type Flow struct {
	ID            uint32
	MAC           string
	Local, Remote netip.AddrPort
	Proto         uint8
	Dir           Direction
	Name, NameSrc string // NameSrc: "sni","quic","dns","ech","none"
	App           enrich.App
	Geo           enrich.ASInfo
	Start, End    time.Time
	Updated       time.Time
	Up, Down      ct.Counters
	Offloaded     bool
	Count         int
	// RateUp and RateDown are the bytes per second this flow moved over its
	// last Sync interval, as the device sees it: 0 when it moved nothing,
	// when it was first met, and once it has ended.
	RateUp, RateDown float64
	// Evicted says the table pushed this flow out to make room while it
	// was still open: End is when that happened, not when it ended.
	Evicted bool
	// WinUp and WinDown are the bytes this flow moved over the window a live
	// query asked for; zero otherwise.
	WinUp, WinDown uint64
}

// Delta is the bytes one flow moved since its last delta, stamped with what
// it was fixed as.
type Delta struct {
	At                   time.Time
	MAC, App, CC, Domain string
	ASN                  uint32
	Org                  string
	Up, Down             uint64
	First                bool
	// NameSrc is how the flow got its name ("sni", "quic", "dns", "ech",
	// "none"), so the console can say how sure a domain is.
	NameSrc string
}

// LivePoint is one Sync of a device's live ring: what each app moved per
// second over the Step before At, busiest first. An app that moved nothing
// is absent; past maxPointApps apps the rest are folded into "other".
type LivePoint struct {
	At   time.Time
	Step time.Duration
	Apps []AppRate
}

// AppRate is one app's bytes per second in a live point. float32 holds a
// rate to seven digits, which a chart cannot tell apart, at half the bytes:
// the ring is 150 of these lists for each of up to 512 devices.
type AppRate struct {
	App      string
	Up, Down float32
}
