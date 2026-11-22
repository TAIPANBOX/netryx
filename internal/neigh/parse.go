// Package neigh keeps the only stable answer to "which device is this
// address".
//
// A phone on IPv6 rotates through temporary addresses by design, so keying a
// device by address turns one phone into a dozen rows that each disappear
// after a day. The kernel's neighbour table holds the mapping that does not
// rotate: address to hardware address. This package reads it, watches it, and
// keeps its own history, because the kernel forgets an entry about a minute
// after the last packet while a house can hold hundreds of them.
package neigh

import (
	"net"
	"time"

	"netryx/internal/hostorder"
)

// hostOrder is how netlink lays out its own headers on this machine.
var hostOrder = hostorder.Native

// Netlink constants, spelled out rather than imported so the parser can be
// tested on any machine.
const (
	rtmNewNeigh = 28
	rtmDelNeigh = 29
	ndaDst      = 1
	ndaLladdr   = 2

	// states worth distinguishing: a FAILED or NONE entry says nothing about
	// which device owns the address.
	nudIncomplete = 0x01
	nudReachable  = 0x02
	nudStale      = 0x04
	nudDelay      = 0x08
	nudProbe      = 0x10
	nudFailed     = 0x20
	nudPermanent  = 0x80
)

// StateReachable is the kernel's NUD_REACHABLE: the neighbour answered within
// the last reachable time, so the device is on the network now.
const StateReachable = nudReachable

// Entry is one row of the kernel's neighbour table.
type Entry struct {
	IP      net.IP
	MAC     string
	IfIndex int
	State   uint16
	Deleted bool // the message was RTM_DELNEIGH
}

// Usable says whether the entry actually binds an address to a device. An
// INCOMPLETE or FAILED entry has no hardware address to trust.
func (e Entry) Usable() bool {
	if e.MAC == "" || e.IP == nil {
		return false
	}
	return e.State&(nudReachable|nudStale|nudDelay|nudProbe|nudPermanent) != 0
}

// ParseMessage reads one netlink message body (after the 16-byte header) as a
// neighbour entry. msgType says whether it is an addition or a removal.
func ParseMessage(msgType uint16, b []byte) (Entry, bool) {
	if msgType != rtmNewNeigh && msgType != rtmDelNeigh {
		return Entry{}, false
	}
	// struct ndmsg: family(1) pad(3) ifindex(4) state(2) flags(1) type(1)
	if len(b) < 12 {
		return Entry{}, false
	}
	e := Entry{
		IfIndex: int(int32(hostOrder.Uint32(b[4:8]))),
		State:   hostOrder.Uint16(b[8:10]),
		Deleted: msgType == rtmDelNeigh,
	}
	for p := 12; p+4 <= len(b); {
		l := int(hostOrder.Uint16(b[p : p+2]))
		typ := hostOrder.Uint16(b[p+2 : p+4])
		if l < 4 || p+l > len(b) {
			break
		}
		val := b[p+4 : p+l]
		switch typ {
		case ndaDst:
			if len(val) == 4 || len(val) == 16 {
				e.IP = append(net.IP(nil), val...)
			}
		case ndaLladdr:
			if len(val) == 6 {
				e.MAC = net.HardwareAddr(val).String()
			}
		}
		p += (l + 3) &^ 3 // rtattr entries are aligned to four bytes
	}
	if e.IP == nil {
		return Entry{}, false
	}
	return e, true
}

// Registry is the history the kernel does not keep: every address ever seen
// for a device, with when it was first and last seen.
type Registry struct {
	byIP  map[string]*record
	byMAC map[string]map[string]time.Time
	// reach holds the devices one of whose entries was REACHABLE in the
	// last observation of that entry.
	reach map[string]bool
}

type record struct {
	MAC   string
	First time.Time
	Last  time.Time
}

func NewRegistry() *Registry {
	return &Registry{byIP: map[string]*record{}, byMAC: map[string]map[string]time.Time{}, reach: map[string]bool{}}
}

// Observe folds one entry in. A deletion is NOT forgotten: the kernel evicts a
// neighbour a minute after its last packet, and a device that went quiet is
// still the same device when it comes back.
func (r *Registry) Observe(e Entry, now time.Time) {
	if !e.Usable() {
		return
	}
	ip := e.IP.String()
	rec, ok := r.byIP[ip]
	if !ok {
		rec = &record{MAC: e.MAC, First: now}
		r.byIP[ip] = rec
	}
	rec.MAC, rec.Last = e.MAC, now
	if e.State&nudReachable != 0 {
		r.reach[e.MAC] = true
	}
	if r.byMAC[e.MAC] == nil {
		r.byMAC[e.MAC] = map[string]time.Time{}
	}
	r.byMAC[e.MAC][ip] = now
}

// MACOf returns the hardware address behind an IP, or "" when it was never
// seen. This is the join that makes a rotating IPv6 address one device.
func (r *Registry) MACOf(ip net.IP) string {
	if r == nil || ip == nil {
		return ""
	}
	if rec, ok := r.byIP[ip.String()]; ok {
		return rec.MAC
	}
	return ""
}

// AddressesOf lists every address seen for a device, newest first.
func (r *Registry) AddressesOf(mac string) []string {
	out := make([]string, 0, len(r.byMAC[mac]))
	for ip := range r.byMAC[mac] {
		out = append(out, ip)
	}
	return out
}

// Reachable says whether the kernel last reported one of the device's
// entries REACHABLE.
func (r *Registry) Reachable(mac string) bool {
	return r != nil && r.reach[mac]
}

// Devices is how many distinct hardware addresses have been seen.
func (r *Registry) Devices() int { return len(r.byMAC) }

// Forget drops addresses not seen since the cutoff, so a week of rotation does
// not grow without bound. The device itself is kept as long as one address is.
func (r *Registry) Forget(before time.Time) int {
	dropped := 0
	for ip, rec := range r.byIP {
		if rec.Last.Before(before) {
			delete(r.byIP, ip)
			delete(r.byMAC[rec.MAC], ip)
			if len(r.byMAC[rec.MAC]) == 0 {
				delete(r.byMAC, rec.MAC)
			}
			dropped++
		}
	}
	return dropped
}
