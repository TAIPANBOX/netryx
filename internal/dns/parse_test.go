package dns

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"
)

// build assembles a message the way a resolver does, including the name
// compression every real answer uses: the answer's name is a pointer back to
// the question rather than a copy of it. A parser tested only against
// uncompressed names passes here and reads nothing at all on a live network.
func build(t *testing.T, response bool, qname string, answers ...Answer) []byte {
	t.Helper()
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[0:2], 0x1234)
	var flags uint16
	if response {
		flags = 0x8180
	} else {
		flags = 0x0100
	}
	binary.BigEndian.PutUint16(b[2:4], flags)
	binary.BigEndian.PutUint16(b[4:6], 1)
	binary.BigEndian.PutUint16(b[6:8], uint16(len(answers)))
	qoff := len(b)
	for _, l := range strings.Split(qname, ".") {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	b = append(b, 0)
	b = append(b, 0, 1, 0, 1) // A, IN
	for _, a := range answers {
		ptr := make([]byte, 2)
		binary.BigEndian.PutUint16(ptr, 0xc000|uint16(qoff))
		b = append(b, ptr...) // the name, as a pointer to the question
		ip4 := a.IP.To4()
		if ip4 != nil {
			b = append(b, 0, typeA, 0, 1)
		} else {
			b = append(b, 0, typeAAAA, 0, 1)
		}
		ttl := make([]byte, 4)
		binary.BigEndian.PutUint32(ttl, a.TTL)
		b = append(b, ttl...)
		data := ip4
		if data == nil {
			data = a.IP.To16()
		}
		b = append(b, byte(len(data)>>8), byte(len(data)))
		b = append(b, data...)
	}
	return b
}

func TestAQuestionNamesTheDeviceIsAsking(t *testing.T) {
	m, ok := Parse(build(t, false, "api.telegram.org"))
	if !ok || m.Response {
		t.Fatalf("a query must parse as a query: %+v %v", m, ok)
	}
	if len(m.Questions) != 1 || m.Questions[0] != "api.telegram.org" {
		t.Fatalf("the question is the name the device wants: %+v", m.Questions)
	}
}

func TestAnAnswerGivesTheAddressesAName(t *testing.T) {
	msg := build(t, true, "www.example.com",
		Answer{IP: net.ParseIP("93.184.216.34"), TTL: 300},
		Answer{IP: net.ParseIP("2606:2800:220:1:248:1893:25c8:1946"), TTL: 300},
	)
	m, ok := Parse(msg)
	if !ok || !m.Response {
		t.Fatalf("a response must parse as one: %+v %v", m, ok)
	}
	if len(m.Answers) != 2 {
		t.Fatalf("both families must come back: %+v", m.Answers)
	}
	// The name in the answer is a compression pointer. Reading it wrongly is
	// the difference between naming a flow and naming nothing.
	for _, a := range m.Answers {
		if a.Name != "www.example.com" {
			t.Fatalf("the answer's name must follow the pointer: %q", a.Name)
		}
		if a.TTL != 300 {
			t.Fatalf("the time to live is how long the naming is good for: %d", a.TTL)
		}
	}
	if !m.Answers[0].IP.Equal(net.ParseIP("93.184.216.34")) || m.Answers[1].IP.To4() != nil {
		t.Fatalf("addresses came out wrong: %+v", m.Answers)
	}

	// The bytes a message is parsed from are a read buffer that the next
	// packet overwrites. An address that points into it instead of owning its
	// own copy changes into another device's address later, and every table
	// built from it is then quietly wrong.
	for i := range msg {
		msg[i] = 0xff
	}
	if !m.Answers[0].IP.Equal(net.ParseIP("93.184.216.34")) {
		t.Fatalf("the address must be a copy, not a window onto the buffer: %v", m.Answers[0].IP)
	}
}

// A message that did not resolve is not a name, and a message that is not a
// resolver's business at all must not be read as one.
func TestOnlyUsefulAnswersCount(t *testing.T) {
	nx := build(t, true, "nope.example")
	nx[3] = 0x83 // NXDOMAIN
	if _, ok := Parse(nx); ok {
		t.Fatal("a name that does not exist is not a naming")
	}
	upd := build(t, false, "a.example")
	upd[2] = 0x28 // opcode UPDATE
	if _, ok := Parse(upd); ok {
		t.Fatal("only standard queries are read")
	}
	if _, ok := Parse(nil); ok {
		t.Fatal("nothing is not a message")
	}
}

// These bytes come off somebody's network. None of them may hang the router
// or be answered with a name that was never there.
func TestHostileMessagesAreRefusedNotGuessed(t *testing.T) {
	good := build(t, true, "www.example.com", Answer{IP: net.ParseIP("1.2.3.4"), TTL: 60})

	// every truncation of a valid message
	for i := 0; i < len(good); i++ {
		m, ok := Parse(good[:i])
		if ok {
			for _, a := range m.Answers {
				if a.IP == nil || len(a.IP) == 0 {
					t.Fatalf("truncation at %d produced an empty address", i)
				}
			}
			for _, q := range m.Questions {
				if !strings.Contains("www.example.com", q) {
					t.Fatalf("truncation at %d invented the name %q", i, q)
				}
			}
		}
	}

	// a pointer to itself, the classic way to hang a name parser
	loop := make([]byte, 16)
	binary.BigEndian.PutUint16(loop[2:4], 0x0100)
	binary.BigEndian.PutUint16(loop[4:6], 1)
	loop[12], loop[13] = 0xc0, 0x0c // points at itself
	if _, ok := Parse(loop); ok {
		t.Fatal("a self-referential pointer is not a name")
	}

	// a pointer forward, which a well-formed message never uses
	fwd := append([]byte{}, good...)
	fwd[12], fwd[13] = 0xc0, byte(len(good)-1)
	if _, ok := Parse(fwd); ok {
		t.Fatal("a forward pointer is not a name")
	}

	// A forward pointer that leads to a perfectly good name. A parser without
	// the backwards rule follows it and reports a name that the device never
	// asked for; the previous version of this test could not tell, because
	// its forward pointer led to rubbish that failed for another reason.
	planted := append([]byte{}, good[:12]...)
	binary.BigEndian.PutUint16(planted[4:6], 1)
	binary.BigEndian.PutUint16(planted[6:8], 0)
	planted = append(planted, 0xc0, 18)   // question name: a pointer forward
	planted = append(planted, 0, 1, 0, 1) // type and class
	planted = append(planted, 7, 'e', 'l', 's', 'e', 'w', 'h', 'e', 0)
	if m, ok := Parse(planted); ok {
		t.Fatalf("a forward pointer must be refused even when it reads well: %+v", m)
	}

	// A label that claims more bytes than the message has left. Without the
	// bound this is a panic in the router's monitor, triggered by one packet.
	over := append([]byte{}, good[:12]...)
	binary.BigEndian.PutUint16(over[4:6], 1)
	binary.BigEndian.PutUint16(over[6:8], 0)
	over = append(over, 60, 'a', 'b', 'c') // says sixty bytes, carries three
	if _, ok := Parse(over); ok {
		t.Fatal("a label that runs past the end is not a name")
	}

	// counts that promise more than the message can hold
	big := append([]byte{}, good...)
	binary.BigEndian.PutUint16(big[6:8], 60000)
	if _, ok := Parse(big); ok {
		t.Fatal("an answer count nobody could satisfy must be refused")
	}

	// arbitrary noise on port 53: nothing here may be read as a name
	noise := []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	if m, ok := Parse(noise); ok {
		t.Fatalf("that is not DNS: %+v", m)
	}
}
