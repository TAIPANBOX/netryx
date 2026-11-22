// Package dns reads the questions devices ask the router's resolver and the
// answers it gives them.
//
// This is the best naming signal a router has, and the plan originally threw
// it away. On the WAN side it is invisible, because the house resolver talks
// to Cloudflare over TLS; but on the LAN side the devices ask dnsmasq in the
// clear, and that names nearly every flow, including the ones whose TLS
// handshake hides the name behind Encrypted Client Hello.
//
// Everything here is written against bytes that come off somebody's network,
// so nothing is trusted: every read is bounded, compression pointers must
// point backwards and are limited in number, and a message this code cannot
// make sense of is discarded rather than guessed at.
package dns

import (
	"encoding/binary"
	"net"
	"strings"
)

// Message is the little that matters here.
type Message struct {
	Response  bool
	Questions []string
	Answers   []Answer
}

// Answer is one address a name resolved to.
type Answer struct {
	Name string
	IP   net.IP
	TTL  uint32
}

const (
	maxJumps  = 8   // compression pointers to follow before giving up
	maxLabels = 128 // labels in one name
	typeA     = 1
	typeAAAA  = 28
	typeCNAME = 5
)

// Parse reads a DNS message. The second return says whether anything usable
// was found: a false here means "not DNS, or not DNS this code understands",
// never a partially filled answer.
func Parse(b []byte) (Message, bool) {
	if len(b) < 12 {
		return Message{}, false
	}
	flags := binary.BigEndian.Uint16(b[2:4])
	// Opcode must be a standard query (0) and the reply code, if this is a
	// response, must be no error: a name that did not resolve is not a name.
	if (flags>>11)&0xf != 0 {
		return Message{}, false
	}
	m := Message{Response: flags&0x8000 != 0}
	if m.Response && flags&0xf != 0 {
		return Message{}, false
	}
	qd := int(binary.BigEndian.Uint16(b[4:6]))
	an := int(binary.BigEndian.Uint16(b[6:8]))
	if qd > 8 || an > 64 {
		// A message claiming more than this is either not a resolver's or is
		// trying to make this loop expensive.
		return Message{}, false
	}
	off := 12
	for i := 0; i < qd; i++ {
		name, next, ok := readName(b, off)
		if !ok || next+4 > len(b) {
			return Message{}, false
		}
		off = next + 4
		if name != "" {
			m.Questions = append(m.Questions, name)
		}
	}
	for i := 0; i < an; i++ {
		name, next, ok := readName(b, off)
		if !ok || next+10 > len(b) {
			break // a truncated answer section leaves what was already read
		}
		typ := binary.BigEndian.Uint16(b[next : next+2])
		ttl := binary.BigEndian.Uint32(b[next+4 : next+8])
		rdlen := int(binary.BigEndian.Uint16(b[next+8 : next+10]))
		rd := next + 10
		if rd+rdlen > len(b) {
			break
		}
		switch {
		case typ == typeA && rdlen == 4:
			m.Answers = append(m.Answers, Answer{Name: name, IP: net.IP(append([]byte{}, b[rd:rd+4]...)), TTL: ttl})
		case typ == typeAAAA && rdlen == 16:
			m.Answers = append(m.Answers, Answer{Name: name, IP: net.IP(append([]byte{}, b[rd:rd+16]...)), TTL: ttl})
		case typ == typeCNAME:
			// The address that follows a CNAME chain is filed under the name
			// that was asked for, not under the alias: a device asked for
			// "www.example.com", and that is the answer a person wants to
			// read, not "e1234.a.akamaiedge.net".
		}
		off = rd + rdlen
	}
	if len(m.Questions) == 0 && len(m.Answers) == 0 {
		return Message{}, false
	}
	return m, true
}

// readName reads a possibly compressed name and returns it with the offset
// just past it in the ORIGINAL message, which is not where a pointer led.
func readName(b []byte, off int) (string, int, bool) {
	var parts []string
	jumps := 0
	next := -1
	for labels := 0; labels < maxLabels; labels++ {
		if off < 0 || off >= len(b) {
			return "", 0, false
		}
		l := int(b[off])
		switch {
		case l == 0:
			if next < 0 {
				next = off + 1
			}
			return strings.Join(parts, "."), next, true
		case l&0xc0 == 0xc0:
			if off+1 >= len(b) {
				return "", 0, false
			}
			target := int(binary.BigEndian.Uint16(b[off:off+2]) & 0x3fff)
			// Backwards only, and a limited number of times. Forward or
			// self-referential pointers are how a malformed message turns a
			// parser into an infinite loop.
			if target >= off || jumps >= maxJumps {
				return "", 0, false
			}
			if next < 0 {
				next = off + 2
			}
			off = target
			jumps++
		case l&0xc0 != 0:
			return "", 0, false // reserved label type
		default:
			if off+1+l > len(b) {
				return "", 0, false
			}
			parts = append(parts, string(b[off+1:off+1+l]))
			off += 1 + l
		}
	}
	return "", 0, false
}
