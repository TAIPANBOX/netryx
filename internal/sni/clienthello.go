// Package sni pulls the name a device asked for out of a TLS handshake. It is
// the one field that says "this is Telegram" rather than "this is some address
// at Cloudflare", and it is readable without decrypting anything, because the
// ClientHello is sent before the keys exist.
//
// The limits are named here rather than in a footnote: Encrypted Client Hello
// hides this field, and its share of the internet grows every year; a device
// that resolves names over DoH gives nothing away either. What the parser
// cannot see, it reports as not seen.
package sni

import "encoding/binary"

// Result is what a handshake gave up. ECH matters on its own: under Encrypted
// Client Hello the server_name that IS visible is the outer one, a decoy such
// as cloudflare-ech.com, so reporting it as the destination would label every
// such site with the same wrong name. Hidden is the honest answer.
type Result struct {
	Name string // the server name, empty when there is none or it is hidden
	ECH  bool   // the client sent encrypted_client_hello, so Name is a decoy
	// Incomplete means the record was cut before the extension list ended.
	// The name may still be here: server_name is usually one of the first
	// extensions, and with post-quantum key shares a ClientHello runs to
	// 1.3-1.8 KB, so a capture that stops earlier truncates almost every
	// browser handshake. Throwing away a name that was already read is how
	// this parser used to report nothing at all.
	Incomplete bool
}

// FromTLS returns the server name in a TLS ClientHello, or "" when the record
// is not a ClientHello, is truncated, carries no SNI, or hides it behind ECH.
func FromTLS(b []byte) string {
	r := Inspect(b)
	if r.ECH {
		return ""
	}
	return r.Name
}

// Inspect reads the handshake and reports both the name and whether the client
// encrypted it.
func Inspect(b []byte) Result {
	// TLS record: type(1) version(2) length(2)
	if len(b) < 5 || b[0] != 0x16 || b[1] != 0x03 {
		return Result{}
	}
	rec := int(binary.BigEndian.Uint16(b[3:5]))
	body := b[5:]
	if len(body) > rec {
		body = body[:rec]
	}
	// handshake: type(1) length(3) version(2) random(32)
	if len(body) < 39 || body[0] != 0x01 {
		return Result{}
	}
	p := body[38:] // skip handshake header, version and random
	// session id
	if len(p) < 1 {
		return Result{Incomplete: true}
	}
	n := int(p[0])
	if len(p) < 1+n+2 {
		return Result{}
	}
	p = p[1+n:]
	// cipher suites
	n = int(binary.BigEndian.Uint16(p[:2]))
	if len(p) < 2+n+1 {
		return Result{}
	}
	p = p[2+n:]
	// compression methods
	n = int(p[0])
	if len(p) < 1+n+2 {
		return Result{}
	}
	p = p[1+n:]
	// extensions
	total := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	if len(p) > total {
		p = p[:total]
	}
	var out Result
	for len(p) >= 4 {
		typ := binary.BigEndian.Uint16(p[:2])
		size := int(binary.BigEndian.Uint16(p[2:4]))
		if len(p) < 4+size {
			// The record ends inside this extension. Whatever was already
			// read stands; what is beyond the cut is simply unknown.
			out.Incomplete = true
			return out
		}
		ext := p[4 : 4+size]
		switch typ {
		case 0x0000: // server_name
			if len(ext) < 5 || ext[2] != 0x00 { // 0 = host_name
				return Result{}
			}
			l := int(binary.BigEndian.Uint16(ext[3:5]))
			if len(ext) < 5+l {
				// A name cut in half is a wrong name, not a partial one.
				out.Incomplete = true
				return out
			}
			out.Name = string(ext[5 : 5+l])
		case 0xfe0d: // encrypted_client_hello
			// The outer name stays readable, so the scan must continue past
			// this extension rather than stop at the first one it likes.
			out.ECH = true
		}
		p = p[4+size:]
	}
	return out
}

// IsTLSHandshake says whether a payload starts like a TLS handshake record, so
// a caller can skip the rest of a flow cheaply.
func IsTLSHandshake(b []byte) bool {
	return len(b) >= 3 && b[0] == 0x16 && b[1] == 0x03
}
