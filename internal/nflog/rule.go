package nflog

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// RuleSet returns the nftables text that makes the kernel send the first
// packets of every connection to a log group.
//
// It lives in its own table on purpose. fw4 owns `inet fw4` and rewrites it
// wholesale on every reload; a rule added there would vanish the next time
// somebody saved a firewall setting in LuCI. A separate table is left alone,
// and `nft delete table inet netryx` removes every trace in one command.
//
// The priority sits just below fw4's filter hook so the copy is made before
// anything drops the packet, and the chain's policy is accept: this table
// observes and never decides.
func RuleSet(o RuleOptions) string {
	var b strings.Builder
	// Applying the same file twice must leave the same ruleset, not two copies
	// of every rule and two copies of every packet. Declaring the table before
	// deleting it makes the delete safe on a router where it does not exist
	// yet, which is the usual case and would otherwise fail the whole script.
	b.WriteString("table inet " + o.Table + " {}\n")
	b.WriteString("delete table inet " + o.Table + "\n")
	b.WriteString("table inet " + o.Table + " {\n")
	b.WriteString("\tchain observe {\n")
	fmt.Fprintf(&b, "\t\ttype filter hook forward priority filter - 1; policy accept;\n")
	for _, iface := range o.Interfaces {
		fmt.Fprintf(&b, "\t\tiifname \"%s\" ct original packets < %d log group %d snaplen %d\n",
			iface, o.FirstPackets, o.Group, o.Snaplen)
	}
	if len(o.Interfaces) == 0 {
		fmt.Fprintf(&b, "\t\tct original packets < %d log group %d snaplen %d\n",
			o.FirstPackets, o.Group, o.Snaplen)
	}
	b.WriteString("\t}\n")
	if o.Own {
		// The router's own traffic never crosses the forward hook, and on a
		// box that is not a gateway that is all there is to see. It is also
		// the question the Keenetic tap was built to answer: where does the
		// router itself go.
		b.WriteString("\tchain observe_own {\n")
		fmt.Fprintf(&b, "\t\ttype filter hook output priority filter - 1; policy accept;\n")
		fmt.Fprintf(&b, "\t\tct original packets < %d log group %d snaplen %d\n",
			o.FirstPackets, o.Group, o.Snaplen)
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// ValidInterface is what an interface name may be. The name is interpolated
// into a script that runs as root, so it is checked rather than trusted: this
// is a configuration value, and a configuration value is somebody's input.
var ValidInterface = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// CheckInterfaces refuses anything that is not a plausible interface name.
func CheckInterfaces(names []string) error {
	for _, n := range names {
		if !ValidInterface.MatchString(n) {
			return fmt.Errorf("not an interface name: %q", n)
		}
	}
	return nil
}

// RuleOptions are the knobs, with the reasoning attached.
type RuleOptions struct {
	Table      string
	Group      uint16
	Interfaces []string
	// FirstPackets is how many packets of a connection the kernel copies. It
	// has to be more than one: a modern ClientHello with post-quantum key
	// shares runs past one segment, so stopping at the first packet loses the
	// name about half the time.
	FirstPackets int
	// Own adds the router's own outbound traffic, which never passes the
	// forward hook.
	Own bool
	// Mode picks the hook: see the constants for what each one costs.
	Mode Mode
	// SetTimeout is how long a four-tuple stays in the ingress set. Long
	// enough to cover a handshake, short enough that the set stays small on a
	// busy network.
	SetTimeout string
	// SetSize caps the set. A dynamic set with no size grows with whatever is
	// sent to it, and the router has no memory to spare for that. Only flows
	// that begin with a handshake ever enter, so the real occupancy is small
	// and this is a ceiling, not a working figure.
	SetSize int
	// UDPRate bounds the QUIC rule, which has no set behind it.
	UDPRate string
	// DNS adds the resolver's traffic: the questions devices ask the router
	// and the answers it gives them. It is the only naming signal that
	// survives Encrypted Client Hello, and it is also the most sensitive
	// thing this package reads, so it is a setting the operator can turn off.
	DNS bool
	// DNSRate bounds the DNS rules, which have no set behind them.
	DNSRate string
	// Budget is how many packets of one flow the ingress hook may copy. It is
	// NOT FirstPackets: the forward hook counted a connection's packets and
	// could afford eight, while here the only reason to copy more than the
	// first is that a ClientHello with post-quantum key shares runs to about
	// 1.8 KB and spans two segments. @measured on the Flint: with eight, three
	// handshakes cost 24 copies, and the 21 that were not the ClientHello were
	// the client's own data. Three is the handshake and no more.
	Budget int
	// Snaplen caps how much of each packet is copied. It has to clear a
	// modern ClientHello: with post-quantum key shares those run to 1.3-1.8
	// KB, so the 600 bytes that were "enough for a handshake" before 2024
	// truncate nearly every browser handshake today. Still short enough that
	// a copy never carries a meaningful amount of someone's cleartext.
	Snaplen int
}

// DefaultRule is what the service installs when nobody has said otherwise.
func DefaultRule(ifaces ...string) RuleOptions {
	return RuleOptions{
		Table:        "netryx",
		Group:        7,
		Interfaces:   ifaces,
		FirstPackets: 8,
		Snaplen:      1800,
		Own:          true,
		SetTimeout:   "30s",
		SetSize:      4096,
		Budget:       3,
		DNS:          true,
		DNSRate:      "100/second burst 200 packets",
		UDPRate:      "50/second burst 100 packets",
	}
}

// TeardownCommand removes everything this package installed.
func TeardownCommand(table string) string { return "delete table inet " + table }

// Apply installs the ruleset with nft. Shelling out to nft rather than
// speaking the netlink protocol directly is a deliberate choice for phase 1:
// the command is on every OpenWrt that has a firewall, the text is auditable
// by the person whose router it is, and the failure messages are theirs.
func Apply(o RuleOptions) (string, error) {
	if err := CheckInterfaces(o.Interfaces); err != nil {
		return "", err
	}
	if o.Mode == Ingress {
		devices, err := DevicesFor(sysfsNet, o.Interfaces)
		if err != nil {
			return "", err
		}
		out, err := nftRun(NetdevRuleSet(o, devices))
		if err == nil && o.Own {
			// The router's own traffic needs the output hook whichever mode
			// the house is watched in, and whatever the bridge holds.
			var own string
			own, err = nftRun(OwnRuleSet(o))
			out += own
		}
		if err == nil && len(devices) == 0 {
			err = ErrNoDevices
		}
		return out, err
	}
	return nftRun(RuleSet(o))
}

// ErrNoDevices is Apply's answer for an ingress rule whose interfaces hold no
// device right now (a bridge with every port down). The netdev table goes in
// without its chain and the router's own chain goes in as usual; the
// supervisor attaches the chain once a port comes up. It is a state to say,
// not a failure to stop on.
var ErrNoDevices = errors.New("no device to listen on: the watched interfaces have no port up")

// nftRun is the seam. Remove is the one function whose whole job is what it
// asks the kernel for, and a test that cannot see the commands can only prove
// that nft was absent.
var nftRun = runNFT

// TeardownScripts is every command needed to leave the router as it was found.
// Both families are here because a run may have used either hook, and the one
// left behind is the bad kind of leftover: a chain still copying packets to a
// log group nobody is reading.
func TeardownScripts(table string) []string {
	return []string{TeardownCommand(table) + "\n", TeardownNetdev(table) + "\n"}
}

// Remove takes the tables away, and with them every rule this package added.
// One failing does not stop the other: the usual failure is "no such file",
// from the hook this run never used.
func Remove(table string) (string, error) {
	var out strings.Builder
	var failed int
	var first error
	scripts := TeardownScripts(table)
	for _, s := range scripts {
		o, err := nftRun(s)
		out.WriteString(o)
		if err != nil {
			failed++
			if first == nil {
				first = err
			}
		}
	}
	// Every one failing is a real failure: nft missing, or no permission.
	if failed == len(scripts) {
		return out.String(), first
	}
	return out.String(), nil
}

// Mode says where the selection happens.
type Mode int

const (
	// Forward selects in the forward hook using the connection's packet
	// count. Exact and cheap, and it loses everything the moment the kernel
	// offloads a flow: with fw4's flow_offload the handshake takes the fast
	// path at the client's third packet and never reaches any forward chain.
	Forward Mode = iota
	// Ingress selects on the way in at the bridge's member devices. It runs
	// before a software flowtable and before the bridge, so an offloaded flow
	// is still seen. Hardware offload is a different story and the honest
	// statement is narrower: once the packet engine has been programmed for a
	// flow, its packets never reach the CPU at all and no software hook sees
	// them, this one included. What saves the handshake is that programming
	// is asynchronous and starts on the connection's third packet, so the
	// ClientHello is already past. A handshake that comes later, as in
	// STARTTLS, is lost - and it was lost at the forward hook too.
	//
	// There is no connection attached at ingress, so identity comes from the
	// frame itself: the source MAC, and an address that has not been through
	// NAT yet.
	Ingress
)

// NetdevRuleSet is the ingress form. It selects by what a packet IS, not by
// whether its four-tuple has been seen before.
//
// The difference is not academic. @measured on the Flint, 2026-09-15: with a
// set keyed on novelty, one 95-second flow of 188 packets had 32 of them
// copied, because nft's `add` does not refresh an element's timeout. The entry
// dies under a live flow and the next packet creates a new one with a new
// budget, for ever. The same flow through `update` was copied 8 times in
// total. So: `update`, and only on a packet that carries a handshake.
//
// A TLS record that opens a ClientHello is three bytes of signature: content
// type 0x16, version major 0x03, and handshake type 0x01 at byte five. On the
// wire that picked 3 packets out of 39 on a port carrying three handshakes.
// Everything else on that flow is copied only while the element's budget
// lasts, and a plain set lookup spends that budget too (@measured), so the
// bound covers the whole flow rather than each rule separately.
func NetdevRuleSet(o RuleOptions, devices []string) string {
	var b strings.Builder
	b.WriteString("table netdev " + o.Table + " {}\n")
	b.WriteString("delete table netdev " + o.Table + "\n")
	b.WriteString("table netdev " + o.Table + " {\n")
	for _, f := range families {
		fmt.Fprintf(&b, "\tset %s { type %s . inet_service . %s . inet_service; flags dynamic, timeout; timeout %s; size %d; }\n",
			f.set, f.addrType, f.addrType, o.SetTimeout, o.SetSize)
	}
	if len(devices) == 0 {
		// nft refuses `devices = { }`. The table stands without its chain,
		// and the supervisor reads it back as attached to nothing.
		b.WriteString("}\n")
		return b.String()
	}
	b.WriteString("\tchain observe {\n")
	fmt.Fprintf(&b, "\t\ttype filter hook ingress devices = { %s } priority -1; policy accept;\n", strings.Join(devices, ", "))
	// Everything that arrived, so that "how much of this house did the
	// monitor copy" is a number rather than a promise. It is also the only
	// place a full set shows: nft drops the element and says nothing.
	b.WriteString("\t\tcounter comment \"seen\"\n")
	for _, f := range families {
		tuple := f.addr + " saddr . th sport . " + f.addr + " daddr . th dport"
		// The handshake itself: seed the flow's budget and copy this packet.
		fmt.Fprintf(&b, "\t\t%smeta l4proto tcp %s update @%s { %s limit rate 1/hour burst %d packets } counter log group %d snaplen %d comment \"hello\"\n",
			f.guard, clientHello, f.set, tuple, o.Budget, o.Group, o.Snaplen)
		// The rest of it, membership only: a ClientHello with post-quantum key
		// shares runs past one segment, and the second segment carries no
		// signature of its own. Creating nothing here is what keeps bulk
		// traffic out of the set.
		fmt.Fprintf(&b, "\t\t%smeta l4proto tcp %s @%s counter log group %d snaplen %d comment \"more\"\n",
			f.guard, tuple, f.set, o.Group, o.Snaplen)
	}
	if o.DNS {
		for _, f := range families {
			// The questions devices ask the router's own resolver. They
			// arrive in the clear, they name the destination before the
			// connection to it is even made, and a query is one small packet.
			fmt.Fprintf(&b, "\t\t%smeta l4proto udp udp dport 53 limit rate %s counter log group %d snaplen %d comment \"dnsq\"\n",
				f.guard, o.DNSRate, o.Group, dnsQuerySnaplen)
		}
	}
	for _, f := range families {
		// QUIC's Initial packet is self-describing: a long header has both top
		// bits set. No set here, because anything at all may be sent to a UDP
		// port and a set would grow with it; a rate is the honest bound. The
		// port matters too: one DNS message in four begins with both bits set,
		// and without the port they would all be copied.
		fmt.Fprintf(&b, "\t\t%smeta l4proto udp udp dport 443 %s limit rate %s counter log group %d snaplen %d comment \"quic\"\n",
			f.guard, quicInitial, o.UDPRate, o.Group, o.Snaplen)
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

// A question fits in a small packet; an answer with several addresses and
// EDNS does not, and a truncated answer names nothing.
const (
	dnsQuerySnaplen  = 512
	dnsAnswerSnaplen = 1500
)

// The two signatures, named so the rule reads as what it means.
const (
	clientHello = `@ih,0,16 0x1603 @ih,40,8 0x01`
	quicInitial = `@ih,0,8 & 0xc0 == 0xc0`
)

// families keeps the two address families in one place. meta protocol guards
// the family and meta l4proto reads the real transport protocol: ip6 nexthdr
// is the FIRST header, so a packet carrying a hop-by-hop or destination
// options header would never match it, silently.
var families = []struct {
	guard, set, addr, addrType string
}{
	{"meta protocol ip ", "hello4", "ip", "ipv4_addr"},
	{"meta protocol ip6 ", "hello6", "ip6", "ipv6_addr"},
}

// OwnRuleSet is the router's own outbound traffic, and it is a separate table
// because the ingress hook cannot see it: traffic the router originates never
// arrives on a bridge port. Without this, choosing the ingress mode silently
// dropped the question the tap was built to answer - where does the router
// itself go.
// TryPrefix starts the log prefix of a country rule's copy of a try,
// followed by the rule's id (spec 6.9; the rules table writes it, the
// collector hands it to the alerts and counts it as nothing else).
const TryPrefix = "netryx-try "

// AnswerPrefix is the log prefix of the copies of the router's own DNS
// answers. The kernel attaches it by the rule that copied the packet, so a
// reader can tell an answer the router sent from anything a device sent
// with the router's address written in it: no ingress rule carries it.
const AnswerPrefix = "netryx-dnsa"

// SafeAnswerPort is the port the safe-search resolver answers from as the
// output hook sees it: the redirect from 53 is undone only after that hook
// (docs/stage2b-measured.md, section 2). It is below 1024, so only root can
// bind it: a local process cannot answer in its place while it is down.
const SafeAnswerPort = 553

func OwnRuleSet(o RuleOptions) string {
	var b strings.Builder
	b.WriteString("table inet " + o.Table + " {}\n")
	b.WriteString("delete table inet " + o.Table + "\n")
	b.WriteString("table inet " + o.Table + " {\n")
	b.WriteString("\tchain observe_own {\n")
	fmt.Fprintf(&b, "\t\ttype filter hook output priority filter - 1; policy accept;\n")
	if o.DNS {
		// The answers, which are what give an address a name. They leave the
		// router, so this is the only hook that sees them. The accept ends
		// THIS chain for that packet, so the rule below does not copy it a
		// second time; fw4's chains are untouched by it.
		fmt.Fprintf(&b, "\t\tudp sport { 53, %d } limit rate %s counter log prefix \"%s\" group %d snaplen %d accept comment \"dnsa\"\n",
			SafeAnswerPort, o.DNSRate, AnswerPrefix, o.Group, dnsAnswerSnaplen)
	}
	fmt.Fprintf(&b, "\t\tct original packets < %d log group %d snaplen %d comment \"own\"\n", o.FirstPackets, o.Group, o.Snaplen)
	b.WriteString("\t}\n}\n")
	return b.String()
}

// TeardownNetdev removes the ingress table.
func TeardownNetdev(table string) string { return "delete table netdev " + table }
