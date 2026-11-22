package sni

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"net"
	"testing"
)

// The parser is fed a ClientHello produced by Go's own TLS stack, so the test
// cannot drift away from the wire format by agreeing with the parser's bugs.
func realClientHello(t *testing.T, server string) []byte {
	t.Helper()
	c1, c2 := net.Pipe()
	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := c2.Read(buf)
		done <- buf[:n]
		c2.Close()
	}()
	go func() {
		_ = tls.Client(c1, &tls.Config{ServerName: server, InsecureSkipVerify: true}).Handshake()
		c1.Close()
	}()
	return <-done
}

func TestSNIFromARealClientHello(t *testing.T) {
	for _, name := range []string{"api.telegram.org", "discord.com", "a.very.long.name.example.co.uk"} {
		hello := realClientHello(t, name)
		if got := FromTLS(hello); got != name {
			t.Fatalf("SNI for %q read as %q", name, got)
		}
		if !IsTLSHandshake(hello) {
			t.Fatalf("a real ClientHello must look like a TLS handshake")
		}
	}
}

func TestSNIRefusesWhatItCannotRead(t *testing.T) {
	hello := realClientHello(t, "example.com")
	cases := map[string][]byte{
		"empty":            {},
		"not tls":          []byte("GET / HTTP/1.1\r\n"),
		"truncated record": hello[:20],
		"random":           randomBytes(t, 300),
	}
	for what, b := range cases {
		if got := FromTLS(b); got != "" {
			t.Fatalf("%s should give nothing, gave %q", what, got)
		}
	}
	// A record that is NOT a handshake must be refused on its type alone, even
	// though everything after it still looks like a ClientHello.
	notHandshake := append([]byte(nil), hello...)
	notHandshake[0] = 0x17 // application data
	if got := FromTLS(notHandshake); got != "" {
		t.Fatalf("a non-handshake record must be refused, gave %q", got)
	}
	wrongVersion := append([]byte(nil), hello...)
	wrongVersion[1] = 0x02
	if got := FromTLS(wrongVersion); got != "" {
		t.Fatalf("a record with a nonsense version must be refused, gave %q", got)
	}

	// An extension that claims more bytes than the record holds must end the
	// parse, not read past the end of the buffer.
	cut := clientHelloCutInsideSNI(t, "example.org")
	if got := FromTLS(cut); got != "" {
		t.Fatalf("a truncated server_name extension must give nothing, gave %q", got)
	}

	// The inner name length lives inside an extension whose own length is
	// honest, so only a second bounds check catches it.
	lying := append([]byte(nil), hello...)
	if idx := bytes.Index(lying, []byte("example.com")); idx > 2 {
		binary.BigEndian.PutUint16(lying[idx-2:idx], 0x4000) // a name 16k long
		if got := FromTLS(lying); got != "" {
			t.Fatalf("a name longer than its extension must give nothing, gave %q", got)
		}
	} else {
		t.Fatal("could not find the name to corrupt")
	}

	// a ClientHello with no SNI at all (an IP-address connection) is normal
	c1, c2 := net.Pipe()
	done := make(chan []byte, 1)
	go func() { buf := make([]byte, 4096); n, _ := c2.Read(buf); done <- buf[:n]; c2.Close() }()
	go func() {
		_ = tls.Client(c1, &tls.Config{InsecureSkipVerify: true}).Handshake()
		c1.Close()
	}()
	if got := FromTLS(<-done); got != "" {
		t.Fatalf("a hello without SNI must read as empty, got %q", got)
	}
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[0], b[1] = 0x16, 0x03 // looks like TLS, is not
	binary.BigEndian.PutUint16(b[3:5], uint16(n-5))
	return b
}

// clientHelloCutInsideSNI returns a hello whose server_name extension is cut
// off in the middle: the length field promises a name that is not there.
func clientHelloCutInsideSNI(t *testing.T, name string) []byte {
	t.Helper()
	hello := realClientHello(t, name)
	idx := bytes.Index(hello, []byte(name))
	if idx < 0 {
		t.Fatal("the name should be in the hello")
	}
	return hello[:idx+2] // two bytes into the name, the rest is gone
}

// Under Encrypted Client Hello the visible server_name is a decoy the origin
// publishes, so every site behind one Cloudflare zone would be reported under
// the same name. "Hidden" is the only honest answer.
func TestECHIsReportedAsHiddenNotAsTheDecoyName(t *testing.T) {
	hello := realClientHello(t, "cloudflare-ech.com")
	withECH := insertExtension(t, hello, 0xfe0d, []byte{0x00, 0x01, 0x02, 0x03})

	r := Inspect(withECH)
	if !r.ECH {
		t.Fatal("the encrypted_client_hello extension must be noticed")
	}
	if r.Name != "cloudflare-ech.com" {
		t.Fatalf("the outer name is still readable and worth keeping: %q", r.Name)
	}
	if got := FromTLS(withECH); got != "" {
		t.Fatalf("the decoy must not be published as the destination, got %q", got)
	}

	// without the extension the same hello reports its name as before
	plain := Inspect(hello)
	if plain.ECH || plain.Name != "cloudflare-ech.com" {
		t.Fatalf("a plain hello must be unaffected: %+v", plain)
	}
	if got := FromTLS(hello); got != "cloudflare-ech.com" {
		t.Fatalf("a plain hello still yields its name, got %q", got)
	}
}

// insertExtension appends one extension to a ClientHello, fixing the three
// lengths that describe it: the extensions block, the handshake, the record.
func insertExtension(t *testing.T, hello []byte, typ uint16, body []byte) []byte {
	t.Helper()
	out := append([]byte(nil), hello...)
	ext := make([]byte, 4+len(body))
	binary.BigEndian.PutUint16(ext[0:2], typ)
	binary.BigEndian.PutUint16(ext[2:4], uint16(len(body)))
	copy(ext[4:], body)

	// walk to the extensions length field the same way the parser does
	p := 5 + 38
	p += 1 + int(out[p])                              // session id
	p += 2 + int(binary.BigEndian.Uint16(out[p:p+2])) // cipher suites
	p += 1 + int(out[p])                              // compression
	extLenAt := p
	extLen := int(binary.BigEndian.Uint16(out[extLenAt : extLenAt+2]))

	out = append(out, ext...)
	binary.BigEndian.PutUint16(out[extLenAt:extLenAt+2], uint16(extLen+len(ext)))
	hs := int(out[6])<<16 | int(out[7])<<8 | int(out[8])
	out[6], out[7], out[8] = byte((hs+len(ext))>>16), byte((hs+len(ext))>>8), byte(hs+len(ext))
	binary.BigEndian.PutUint16(out[3:5], uint16(int(binary.BigEndian.Uint16(out[3:5]))+len(ext)))
	return out
}

// With post-quantum key shares a ClientHello runs past a kilobyte, so a
// capture that stops at a few hundred bytes truncates nearly every browser
// handshake. The name sits near the front; throwing it away because the tail
// is missing turns a working parser into one that reports nothing at all.
func TestATruncatedHelloStillYieldsTheNameItAlreadyRead(t *testing.T) {
	hello := realClientHello(t, "api.telegram.org")
	if len(hello) < 700 {
		t.Fatalf("this test needs a hello bigger than the old snaplen; got %d bytes", len(hello))
	}
	idx := bytes.Index(hello, []byte("api.telegram.org"))
	if idx < 0 {
		t.Fatal("the name should be in the hello")
	}
	// cut well after the name but well before the end, as a snaplen does
	cut := hello[:idx+len("api.telegram.org")+40]
	r := Inspect(cut)
	if r.Name != "api.telegram.org" {
		t.Fatalf("a truncated hello must still yield the name it carried: %+v (hello %d bytes, cut at %d)",
			r, len(hello), len(cut))
	}
	if !r.Incomplete {
		t.Fatal("and must say that the rest was not seen")
	}
	if got := FromTLS(cut); got != "api.telegram.org" {
		t.Fatalf("the simple form must return it too, got %q", got)
	}
	// a whole hello is not incomplete
	if whole := Inspect(hello); whole.Incomplete || whole.Name != "api.telegram.org" {
		t.Fatalf("a complete hello must not be marked incomplete: %+v", whole)
	}
}
