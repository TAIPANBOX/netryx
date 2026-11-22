// Package device answers the question a WAN-side tap cannot: which machine in
// the house is behind this flow. On the router the answer is exact, because
// conntrack remembers the address a packet had before NAT rewrote it.
package device

import (
	"bufio"
	"io"
	"net"
	"strconv"
	"strings"
)

// Counters are one direction of a flow. The two directions are kept apart on
// purpose: a device that sends far more than it receives is the shape of an
// upload, a backup, or an exfiltration, and a single total hides it.
type Counters struct {
	Packets int
	Bytes   int
}

// Flow is one tracked connection as the router sees it, before and after NAT.
type Flow struct {
	Proto   string
	SrcIP   net.IP // the house address, before NAT
	SrcPort uint16
	DstIP   net.IP // where it goes
	DstPort uint16
	NATIP   net.IP // the address the world sees, empty when there is no NAT

	Up   Counters // house -> internet, the original direction
	Down Counters // internet -> house, the reply direction

	Assured   bool
	Unreplied bool
	// Offloaded means the kernel handed this flow to the flow table or to the
	// hardware path. Its counters then refresh in bursts rather than per
	// packet, so the totals are approximate and the dashboard must say so.
	Offloaded bool
}

func (f Flow) TotalPackets() int { return f.Up.Packets + f.Down.Packets }
func (f Flow) TotalBytes() int   { return f.Up.Bytes + f.Down.Bytes }

// Stats says how the reading went, so a caller can tell "nothing happened"
// from "the kernel is not counting".
type Stats struct {
	Lines  int
	Parsed int
	// AcctOff is set when not one flow carried a byte counter. On OpenWrt that
	// means net.netfilter.nf_conntrack_acct is 0, and every byte figure in the
	// product would silently be zero. It is a condition to report, never to
	// average into a chart.
	AcctOff bool
}

// ParseConntrack reads /proc/net/nf_conntrack.
func ParseConntrack(r io.Reader) ([]Flow, Stats, error) {
	var out []Flow
	var st Stats
	sawCounter := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		st.Lines++
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 {
			continue
		}
		proto := fields[2]
		if proto != "tcp" && proto != "udp" && proto != "icmp" && proto != "icmpv6" {
			continue
		}
		f := Flow{Proto: proto}
		var replySrc, replyDst net.IP
		var dir int // 0 = original tuple, 1 = reply tuple
		for _, kv := range fields {
			switch kv {
			case "[UNREPLIED]":
				f.Unreplied = true
				continue
			case "[ASSURED]":
				f.Assured = true
				continue
			case "[OFFLOAD]", "[HW_OFFLOAD]":
				f.Offloaded = true
				continue
			}
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch k {
			case "src":
				// The second src= starts the reply tuple. Counting on the byte
				// field instead breaks the moment accounting is off, which is
				// exactly when the parser is least able to complain.
				if dir == 0 && f.SrcIP == nil {
					f.SrcIP = net.ParseIP(v)
				} else {
					dir = 1
					replySrc = net.ParseIP(v)
				}
			case "dst":
				if dir == 0 {
					f.DstIP = net.ParseIP(v)
				} else if replyDst == nil {
					replyDst = net.ParseIP(v)
				}
			case "sport":
				n, _ := strconv.Atoi(v)
				if dir == 0 {
					f.SrcPort = uint16(n)
				}
			case "dport":
				n, _ := strconv.Atoi(v)
				if dir == 0 {
					f.DstPort = uint16(n)
				}
			case "packets":
				n, _ := strconv.Atoi(v)
				if dir == 0 {
					f.Up.Packets = n
				} else {
					f.Down.Packets = n
				}
			case "bytes":
				n, _ := strconv.Atoi(v)
				sawCounter = true
				if dir == 0 {
					f.Up.Bytes = n
				} else {
					f.Down.Bytes = n
				}
			}
		}
		if f.SrcIP == nil || f.DstIP == nil {
			continue
		}
		// The reply's destination is the address the world answers to. Where it
		// differs from the original source, that difference IS the NAT, and it
		// is what lets a WAN-side flow be traced back to a house device.
		if replyDst != nil && !replyDst.Equal(f.SrcIP) {
			f.NATIP = replyDst
		}
		_ = replySrc
		st.Parsed++
		out = append(out, f)
	}
	st.AcctOff = st.Parsed > 0 && !sawCounter
	return out, st, sc.Err()
}
