// Package collect keeps what the live capture has learned: which device asked
// for which service, and when.
//
// It is the piece that was missing. The service in the package polled the
// kernel's connection table and published byte counts, so the rule, the log
// group and the handshake parser were exercised only by a person running
// `netryx watch` by hand. Whatever the capture path did or did not do, the
// installed product never showed it.
package collect

import (
	"container/list"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"netryx/internal/ct"
	"netryx/internal/dns"
	"netryx/internal/export"
	"netryx/internal/nflog"
	"netryx/internal/sni"
)

// Site is one destination a device asked for.
type Site struct {
	Name  string    `json:"name"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
	// Via says how the name was learned: from the handshake ("tls"), or from
	// the resolver the device asked ("dns"). It matters because the second
	// kind still works when the first is encrypted, and because a person
	// reading the table deserves to know which it was.
	Via string `json:"via,omitempty"`
}

// Device is what one piece of hardware has been seen doing.
type Device struct {
	MAC   string
	Addrs []string
	Sites map[string]*Site
	// Handshakes is every start-of-connection this device was seen making,
	// named or not. Hidden counts the ones whose name was encrypted, and
	// Unnamed the ones this code could not read: QUIC, for now, and anything
	// whose first segment did not carry the name.
	Handshakes  int
	Hidden      int
	Unnamed     int
	First, Last time.Time
	// TotalUp and TotalDown are what this device has actually moved since the
	// service started. They come from the kernel telling us a connection has
	// finished, with its final counters, and they are the only volumes that
	// do not fall: the connection table can only say what is open right now,
	// so a device that finished a download disappears from it entirely.
	TotalUp, TotalDown int
	Closed             int
}

// Collector is safe for one reader goroutine and any number of scrapes.
//
// Both caps matter on a router: this runs on a box with 128 MB of RAM in the
// worst case, and a device that asks for a thousand different names must not
// be able to grow the table without limit.
type Collector struct {
	mu         sync.Mutex
	devs       map[string]*Device
	byAddr     map[string]string // address -> MAC, for joining with the connection table
	MaxDevices int
	MaxSites   int
	// names is what the resolver said: address -> the name it answered for.
	// This is what names a flow whose handshake hid its destination, and it
	// is capped for the same reason everything else here is. It is kept like
	// pairs: a repeat answer replaces the name and makes the address the
	// newest, and the least recently answered address is let go first, so
	// the memory follows the addresses the house uses now. nameOrder runs
	// oldest to newest.
	names     map[string]*list.Element
	nameOrder *list.List
	MaxNames  int
	// pairs is what each flow's own handshake said, per local>remote pair.
	// The resolver's answer is filed per address, and one CDN address serves
	// a thousand names; the handshake names the one this device asked for.
	// It shares MaxNames, and the oldest is let go first so the memory
	// follows what the house is doing now. order runs oldest to newest.
	pairs map[string]*list.Element
	order *list.List
	// pending holds the first part of a handshake that did not fit in one
	// packet. A ClientHello with post-quantum key shares runs to about 1.8 KB
	// and spans two segments, and the second one carries no signature of its
	// own: on its own it is unreadable, and joined to the first it is a name.
	// The rule already pays to copy it, so throwing it away is paying for
	// nothing.
	pending      map[string]*partial
	pendingOwner map[string]string
	MaxPending   int
	PendingFor   time.Duration
	// Dropped counts what the caps refused, so a truncated table says so
	// rather than looking complete.
	Dropped int
	// lost counts copies the kernel numbered and never delivered, which is
	// the difference between a quiet house and a monitor that could not keep
	// up.
	lost uint64
	// Resolve is the console's one answer to "which device is this address":
	// a MAC, "router" for the router's own addresses, or "" when nothing
	// knows. IsLocal says whether an address is at home. Both are called
	// without the collector's lock held; nil answers as if nothing were known.
	Resolve func(addr string) string
	IsLocal func(netip.Addr) bool
	// OnAnswer is told every answer the router's own resolver gives (copied
	// by the output-side answer rule, nflog.AnswerPrefix, from port 53 of an
	// address Resolve calls the router), with the name it answered for; OnName every handshake that names its destination, with
	// the device's hardware address and the connection as the device opened
	// it. They feed the rules engine, and are called without the lock held.
	OnAnswer func(name string, ip net.IP)
	OnName   func(mac string, local, remote netip.AddrPort, proto uint8, name string)
	// OnTry is told each copy of a try towards a blocked country (prefix
	// nflog.TryPrefix and the rule's id): the rule, the device's hardware
	// address and the address it tried. Called without the lock held.
	OnTry func(rule, mac string, peer net.IP)
}

// hookLocalOut is NF_INET_LOCAL_OUT, the hook of the router's own output
// chain: what it copies the router sent.
const hookLocalOut = 3

// handshake is a named handshake to tell OnName once the lock is released.
type handshake struct {
	mac           string
	local, remote netip.AddrPort
	proto         uint8
	name          string
}

func New() *Collector {
	return &Collector{
		devs: map[string]*Device{}, byAddr: map[string]string{},
		names: map[string]*list.Element{}, nameOrder: list.New(),
		pairs: map[string]*list.Element{}, order: list.New(),
		pending:      map[string]*partial{},
		pendingOwner: map[string]string{},
		MaxDevices:   512, MaxSites: 64, MaxNames: 4096,
		MaxPending: 256, PendingFor: 5 * time.Second,
	}
}

// Observe folds one copied packet in.
func (c *Collector) Observe(p nflog.Packet, now time.Time) {
	if rule, ok := strings.CutPrefix(p.Prefix, nflog.TryPrefix); ok {
		// A country rule's copy of a try: the alerts', not the capture's.
		if c.OnTry != nil {
			c.OnTry(rule, p.HWAddr, p.Peer())
		}
		return
	}
	payload := nflog.TransportPayload(p.Payload)
	sport, dport := p.Ports()
	// The safe-search resolver's answers leave from SafeAnswerPort; only the
	// router's answer rule copies them, so the prefix is what makes them DNS.
	safe := sport == nflog.SafeAnswerPort && p.Prefix == nflog.AnswerPrefix
	if sport == 53 || dport == 53 || safe {
		answers := c.observeDNS(p, payload, dport == 53, now)
		// Only the router's own resolver is believed: a copy made by the
		// router's output-side answer rule (its prefix, which the kernel
		// attaches and no device can send), from port 53 of one of its
		// addresses. A device can write the router's address into a packet
		// at the ingress hook; it cannot choose the rule that copied it.
		if c.OnAnswer != nil && len(answers) > 0 && p.Prefix == nflog.AnswerPrefix &&
			(sport == 53 || safe) && dport != 53 && c.fromRouter(p) {
			for _, a := range answers {
				c.OnAnswer(a.Name, a.IP)
			}
		}
		return
	}
	if h := c.observe(p, payload, sport, dport, now); h != nil && c.OnName != nil {
		// Whose it is: the frame's own hardware address when the copy has
		// one, else the console's one answer (Resolve, called without the
		// lock). A copy from the router's own output chain, or from one of
		// its addresses, is the router's whatever the frame says.
		switch {
		case p.Hook == hookLocalOut:
			h.mac = RouterKey
		default:
			if c.Resolve != nil {
				h.mac = c.Resolve(h.local.Addr().String())
			}
			if h.mac != RouterKey && p.HWAddr != "" {
				h.mac = p.HWAddr
			}
		}
		c.OnName(h.mac, h.local, h.remote, h.proto, h.name)
	}
}

// fromRouter says whether the packet's header source is one of the router's
// own addresses, by Resolve; without Resolve nothing is. It takes no lock.
func (c *Collector) fromRouter(p nflog.Packet) bool {
	if c.Resolve == nil {
		return false
	}
	ip, ok := netip.AddrFromSlice(p.Source())
	if !ok {
		return false
	}
	return c.Resolve(ip.Unmap().String()) == RouterKey
}

// observe is Observe for a packet that is not DNS. It returns the handshake
// it named, if any, for OnName.
func (c *Collector) observe(p nflog.Packet, payload []byte, sport, dport uint16, now time.Time) (named *handshake) {
	addr := p.Device().String()
	key := p.HWAddr
	if key == "" {
		key = addr
	}
	if key == "" {
		return // nothing to attribute it to
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Only the start of something counts. The router's own chain copies the
	// first packets of every connection it makes, and treating each of them
	// as a handshake turns ordinary traffic into destinations that "could not
	// be named". A continuation carries no signature of its own, so it is
	// recognised by belonging to a flow whose first packet had one.
	flow := flowKey(p, addr, sport, dport)
	_, waiting := c.pending[flow]
	first := startOfSomething(payload, dport)
	if !first && !waiting {
		return
	}

	whole := payload
	if waiting {
		whole = c.join(flow, payload, now)
	}
	res := sni.Inspect(whole)
	if first && res.Name == "" && !res.ECH && !waiting {
		// A handshake whose name is not in this packet: hold it, because with
		// post-quantum key shares a ClientHello spans two segments and the
		// name is usually in the second.
		whole = c.join(flow, payload, now)
		res = sni.Inspect(whole)
	}

	if addr != "" && p.HWAddr != "" {
		c.byAddr[addr] = p.HWAddr
	}
	d := c.device(key, p.HWAddr, now)
	if d == nil {
		return
	}
	d.Last = now
	if addr != "" {
		d.Addrs = addUnique(d.Addrs, addr)
	}
	if first {
		// One handshake is one count, whichever packet of it finally carries
		// the name.
		d.Handshakes++
	}

	done := func() { delete(c.pending, flow) }
	switch {
	case res.Name != "" && !res.ECH:
		c.site(d, res.Name, "tls", now)
		via := "sni"
		proto := uint8(6)
		if isUDP(p.Payload) {
			via, proto = "quic", 17
		}
		c.remember(addr, peerKey(p), res.Name, via)
		done()
		local, lok := netip.AddrFromSlice(p.Device())
		remote, rok := netip.AddrFromSlice(p.Peer())
		if lok && rok {
			named = &handshake{local: netip.AddrPortFrom(local.Unmap(), sport),
				remote: netip.AddrPortFrom(remote.Unmap(), dport), proto: proto, name: res.Name}
		}
	case res.ECH:
		// The name was there and the client encrypted it. The resolver may
		// still have said what this address is called, and that answer is as
		// good a name as the handshake would have given.
		d.Hidden++
		name := c.answer(peerKey(p))
		if name != "" {
			c.site(d, name, "dns", now)
		}
		// Remembered as hidden even with no answer: the decoy in the clear
		// must never become the flow's name, and the flow must say why it
		// has none.
		c.remember(addr, peerKey(p), name, "ech")
		done()
	default:
		if name := c.answer(peerKey(p)); name != "" {
			c.site(d, name, "dns", now)
			done()
			return
		}
		if _, held := c.pending[flow]; held {
			// Still waiting for the rest of it. Counting it as unnamed now
			// would be counting the same handshake twice; expire() counts the
			// ones nobody finishes.
			c.pendingOwner[flow] = key
			return
		}
		d.Unnamed++
	}
	return named
}

// flowKey names one direction of one connection, which is the unit a
// handshake is split across.
func flowKey(p nflog.Packet, src string, sport, dport uint16) string {
	return src + ":" + strconv.Itoa(int(sport)) + ">" + peerKey(p) + ":" + strconv.Itoa(int(dport))
}

// startOfSomething says whether this packet opens a conversation whose
// destination could be named: a TLS handshake record, or a QUIC long header,
// which is what every Initial packet carries.
func startOfSomething(payload []byte, dport uint16) bool {
	if sni.IsTLSHandshake(payload) {
		return true
	}
	return dport == 443 && len(payload) > 0 && payload[0]&0xc0 == 0xc0
}

// observeDNS reads a question a device asked, or an answer the resolver gave,
// and returns the answers for OnAnswer.
func (c *Collector) observeDNS(p nflog.Packet, payload []byte, query bool, now time.Time) []dns.Answer {
	m, ok := dns.Parse(payload)
	if !ok {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// The answers name addresses whoever talks to them later.
	for _, a := range m.Answers {
		c.learn(a.IP.String(), a.Name)
	}
	if !query || len(m.Questions) == 0 {
		return m.Answers
	}
	// A question is the device saying where it is about to go, and it arrives
	// before the handshake does.
	addr := p.Device().String()
	key := p.HWAddr
	if key == "" {
		key = addr
	}
	if key == "" {
		return m.Answers
	}
	if addr != "" && p.HWAddr != "" {
		c.byAddr[addr] = p.HWAddr
	}
	d := c.device(key, p.HWAddr, now)
	if d == nil {
		return m.Answers
	}
	d.Last = now
	if addr != "" {
		d.Addrs = addUnique(d.Addrs, addr)
	}
	for _, q := range m.Questions {
		c.site(d, q, "dns", now)
	}
	return m.Answers
}

// learn files the resolver's answer for an address. A repeat answer replaces
// the name and makes the address the newest; a full memory lets the least
// recently answered address go. The caller holds the lock.
func (c *Collector) learn(addr, name string) {
	if e := c.names[addr]; e != nil {
		e.Value.(*pairName).name = name
		c.nameOrder.MoveToBack(e)
		return
	}
	for len(c.names) >= c.MaxNames && c.nameOrder.Len() > 0 {
		old := c.nameOrder.Front()
		delete(c.names, old.Value.(*pairName).key)
		c.nameOrder.Remove(old)
		c.Dropped++
	}
	if c.MaxNames <= 0 {
		c.Dropped++
		return
	}
	c.names[addr] = c.nameOrder.PushBack(&pairName{key: addr, name: name})
}

// answer is the name the resolver last gave for an address, or "". The caller
// holds the lock.
func (c *Collector) answer(addr string) string {
	if e := c.names[addr]; e != nil {
		return e.Value.(*pairName).name
	}
	return ""
}

// device returns the row for this key, creating it while there is room.
func (c *Collector) device(key, mac string, now time.Time) *Device {
	d := c.devs[key]
	if d == nil {
		if len(c.devs) >= c.MaxDevices {
			c.Dropped++
			return nil
		}
		d = &Device{MAC: mac, Sites: map[string]*Site{}, First: now}
		c.devs[key] = d
	}
	return d
}

// site records one destination under a device.
func (c *Collector) site(d *Device, name, via string, now time.Time) {
	s := d.Sites[name]
	if s == nil {
		if len(d.Sites) >= c.MaxSites {
			c.Dropped++
			return
		}
		s = &Site{Name: name, Via: via}
		d.Sites[name] = s
	}
	if s.Via != via {
		s.Via = "tls+dns" // seen both ways: the strongest naming there is
	}
	s.Count++
	s.Last = now
}

// peerKey is the address at the far end, which is what the resolver's answers
// are filed under.
func peerKey(p nflog.Packet) string {
	if ip := p.Peer(); ip != nil {
		return ip.String()
	}
	return ""
}

// Finished folds a connection the kernel has just closed, with its final
// counters, into the device that made it.
//
// The device is found by the connection's pre-NAT source, which is the whole
// reason this runs on the router rather than on a tap: after NAT that address
// is gone. Only a source at home is a device: a connection opened from
// outside, or by the router itself, is nobody's in the house. The source is
// resolved by Resolve when it is set, so an address the neighbour table or a
// lease knows lands on its hardware address rather than on a row of its own.
func (c *Collector) Finished(e ct.Event, now time.Time) {
	if e.Kind != ct.Destroy || e.Orig.SrcIP == nil {
		return
	}
	ip, ok := netip.AddrFromSlice(e.Orig.SrcIP)
	if !ok {
		return
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || !c.local(ip) {
		return
	}
	addr := ip.String()
	// Resolve may take the collector's own lock, so it runs before it.
	mac := ""
	if c.Resolve != nil {
		mac = c.Resolve(addr)
	}
	if mac == RouterKey {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if mac == "" {
		mac = c.byAddr[addr]
	}
	key := mac
	if key == "" {
		key = addr
	}
	d := c.devs[key]
	if d == nil {
		// A connection from a device that has made no handshake we could
		// read. It still moved bytes, and those bytes are the answer to "who
		// is using the line", so the row is created for them.
		if d = c.device(key, mac, now); d == nil {
			return
		}
	}
	d.Addrs = addUnique(d.Addrs, addr)
	d.TotalUp += int(e.Up.Bytes)
	d.TotalDown += int(e.Down.Bytes)
	d.Closed++
	if now.After(d.Last) {
		d.Last = now
	}
}

// RouterKey is the device key the console files the router's own traffic
// under: its own addresses have no hardware address a client would have.
const RouterKey = "router"

// local says whether an address is at home, by IsLocal when it is set and by
// the private ranges otherwise.
func (c *Collector) local(a netip.Addr) bool {
	if c.IsLocal != nil {
		return c.IsLocal(a)
	}
	return a.IsPrivate() || a.IsLinkLocalUnicast()
}

// partial is one handshake waiting for the rest of itself.
type partial struct {
	buf  []byte
	when time.Time
}

// maxPartial caps one flow's buffer. A ClientHello is bounded by the record
// layer at 16 KB and in practice runs to about 1.8 KB; anything past this is
// not a handshake we are going to read, and holding it would let one flow
// spend the router's memory.
const maxPartial = 8 << 10

// join adds this payload to whatever was already waiting for the flow and
// returns the whole of it. Segments are joined in the order they arrived: the
// rule copies only the first few packets of a flow, and a handshake that
// arrives out of order is one this code does not read rather than one it
// reads wrongly.
func (c *Collector) join(key string, payload []byte, now time.Time) []byte {
	c.expire(now)
	p := c.pending[key]
	if p == nil {
		if len(c.pending) >= c.MaxPending {
			c.expire(now)
		}
		if len(c.pending) >= c.MaxPending {
			c.Dropped++
			return payload
		}
		p = &partial{when: now}
		c.pending[key] = p
	}
	room := maxPartial - len(p.buf)
	if room <= 0 {
		return payload
	}
	if len(payload) > room {
		payload = payload[:room]
	}
	p.buf = append(p.buf, payload...)
	p.when = now
	return p.buf
}

// expire drops the halves nobody finished, and counts them where they belong:
// a handshake that was seen and never read is exactly what "could not be
// named" means, and losing the count would make the page look better than the
// network is.
func (c *Collector) expire(now time.Time) {
	for k, p := range c.pending {
		if now.Sub(p.when) <= c.PendingFor {
			continue
		}
		if owner := c.pendingOwner[k]; owner != "" {
			if d := c.devs[owner]; d != nil {
				d.Unnamed++
			}
		}
		delete(c.pendingOwner, k)
		delete(c.pending, k)
	}
}

// Lost records copies that never arrived.
func (c *Collector) Lost(n uint64) {
	if n == 0 {
		return
	}
	c.mu.Lock()
	c.lost += n
	c.mu.Unlock()
}

// LostCopies is how many the kernel numbered and did not deliver.
func (c *Collector) LostCopies() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lost
}

// pairName is one flow's name as its handshake gave it.
type pairName struct {
	key, name, via string
}

// remember files a handshake's name under the pair it was seen on, evicting
// the oldest pair when the memory is full. The caller holds the lock.
func (c *Collector) remember(local, remote, name, via string) {
	// net.IP(nil).String() is "<nil>", which is not an address anything will
	// ever ask about.
	if local == "" || remote == "" || local == "<nil>" {
		return
	}
	key := local + ">" + remote
	if e := c.pairs[key]; e != nil {
		pn := e.Value.(*pairName)
		pn.name, pn.via = name, via
		c.order.MoveToBack(e)
		return
	}
	for len(c.pairs) >= c.MaxNames && c.order.Len() > 0 {
		old := c.order.Front()
		delete(c.pairs, old.Value.(*pairName).key)
		c.order.Remove(old)
		c.Dropped++
	}
	if c.MaxNames <= 0 {
		return
	}
	c.pairs[key] = c.order.PushBack(&pairName{key: key, name: name, via: via})
}

// NameOf says what a flow from local to remote is called, and how that was
// learned: the flow's own handshake ("sni", or "quic" when it came over UDP),
// a hidden handshake named by the resolver ("ech", with an empty name when
// the resolver never answered), or the resolver alone ("dns").
//
// The resolver's answer is read at call time rather than copied when the
// handshake was seen, because under ECH the answer often lands after it.
func (c *Collector) NameOf(local, remote string) (name, via string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.pairs[local+">"+remote]; e != nil {
		pn := e.Value.(*pairName)
		if pn.via == "ech" && pn.name == "" {
			return c.answer(remote), "ech"
		}
		return pn.name, pn.via
	}
	if n := c.answer(remote); n != "" {
		return n, "dns"
	}
	return "", ""
}

// isUDP says whether the packet's transport is UDP, which for a handshake on
// 443 means QUIC.
func isUDP(pkt []byte) bool {
	switch {
	case len(pkt) >= 20 && pkt[0]>>4 == 4:
		return pkt[9] == 17
	case len(pkt) >= 40 && pkt[0]>>4 == 6:
		return pkt[6] == 17
	}
	return false
}

// MACOf returns the hardware address seen using this network address, if any.
func (c *Collector) MACOf(addr string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byAddr[addr]
}

// Devices returns a copy, newest activity first.
func (c *Collector) Devices() []Device {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Device, 0, len(c.devs))
	for _, d := range c.devs {
		cp := *d
		cp.Addrs = append([]string{}, d.Addrs...)
		cp.Sites = map[string]*Site{}
		for k, v := range d.Sites {
			s := *v
			cp.Sites[k] = &s
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// TopSites lists a device's destinations, most asked first.
func (d Device) TopSites(n int) []Site {
	out := make([]Site, 0, len(d.Sites))
	for _, s := range d.Sites {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// Merge joins what the connection table counted with what the capture named.
//
// The two see different things and are keyed differently. The connection table
// counts bytes per source ADDRESS; the capture names services per MAC. A phone
// with three IPv6 privacy addresses is three rows in one and one row in the
// other, and the MAC is the answer: rows whose address belongs to a known MAC
// are folded together.
func Merge(counted []export.Device, live []Device, macOf func(string) string) []export.Device {
	byMAC := map[string]*export.Device{}
	byAddr := map[string]*export.Device{}
	// Pointers into a slice go stale the moment append grows it, which is the
	// kind of bug that shows up as counters that quietly stay at zero. The
	// rows are pointers until the very end for that reason.
	var rows []*export.Device
	add := func(d export.Device) *export.Device {
		if d.MAC != "" {
			if prev := byMAC[d.MAC]; prev != nil {
				prev.Flows += d.Flows
				prev.UpPackets += d.UpPackets
				prev.UpBytes += d.UpBytes
				prev.DownPackets += d.DownPackets
				prev.DownBytes += d.DownBytes
				prev.NATed = prev.NATed || d.NATed
				prev.Offloaded = prev.Offloaded || d.Offloaded
				if prev.Name == prev.Addr && d.Name != d.Addr {
					prev.Name = d.Name // a leased name beats an address
				}
				if d.Addr != "" {
					prev.Addr = prev.Addr + ", " + d.Addr
				}
				return prev
			}
		}
		cp := d
		rows = append(rows, &cp)
		if d.MAC != "" {
			byMAC[d.MAC] = &cp
		}
		if d.Addr != "" {
			byAddr[d.Addr] = &cp
		}
		return &cp
	}
	for _, d := range counted {
		if d.MAC == "" {
			d.MAC = macOf(d.Addr)
		}
		add(d)
	}
	// A device the capture saw but the connection table did not: a flow that
	// closed between scrapes, or accounting turned off. It belongs in the
	// table with no volume rather than not at all.
	for _, l := range live {
		var d *export.Device
		if l.MAC != "" {
			d = byMAC[l.MAC]
		} else {
			// Not everything has a hardware address: the router's own
			// traffic, loopback, a tunnel. Those rows join on the address
			// instead, and dropping them would mean the router could never
			// report on itself.
			for _, a := range l.Addrs {
				if r := byAddr[a]; r != nil {
					d = r
					break
				}
			}
		}
		if d == nil {
			addr := ""
			if len(l.Addrs) > 0 {
				addr = l.Addrs[0]
			}
			name := addr
			if name == "" {
				name = l.MAC
			}
			if name == "" {
				continue // nothing to call it and nothing to join it to
			}
			d = add(export.Device{Name: name, Addr: addr, MAC: l.MAC})
		}
		d.Handshakes += l.Handshakes
		d.Hidden += l.Hidden
		d.TotalUpBytes += l.TotalUp
		d.TotalDownBytes += l.TotalDown
		d.ClosedFlows += l.Closed
		for _, s := range l.TopSites(10) {
			d.Sites = append(d.Sites, s.Name)
		}
	}
	out := make([]export.Device, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	return out
}

func addUnique(list []string, v string) []string {
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
}
