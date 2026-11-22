package export

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sample() Snapshot {
	return Snapshot{
		Taken: time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC),
		Flows: 41, Findings: 3, Blocked: 2,
		Devices: []Device{
			{Name: `iPhone "Guest"`, Addr: "192.168.1.92", MAC: "02:57:c8:48:01:83", Flows: 12,
				UpPackets: 300, UpBytes: 20000, DownPackets: 600, DownBytes: 100000, NATed: true},
			{Name: "MacBook", Addr: "2a01:4b00:ab09:c000::10", Flows: 3,
				UpPackets: 40, UpBytes: 1000, DownPackets: 60, DownBytes: 4000},
		},
	}
}

func TestPrometheusShape(t *testing.T) {
	var b strings.Builder
	if err := Prometheus(&b, sample()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"# TYPE netryx_up gauge",
		"netryx_up 1",
		"netryx_flows_total 41",
		"netryx_findings_total 3",
		"netryx_block_rules 2",
		`netryx_device_bytes{device="iPhone \"Guest\"",addr="192.168.1.92",mac="02:57:c8:48:01:83",family="ipv4",direction="up"} 20000`,
		`netryx_device_bytes{device="iPhone \"Guest\"",addr="192.168.1.92",mac="02:57:c8:48:01:83",family="ipv4",direction="down"} 100000`,
		`netryx_device_flows{device="MacBook",addr="2a01:4b00:ab09:c000::10",family="ipv6"} 3`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing from the exposition:\n%s\n--- got ---\n%s", want, out)
		}
	}
	// every metric must be declared before its samples, or a scraper rejects it
	for _, m := range []string{"netryx_device_bytes", "netryx_device_packets", "netryx_device_flows"} {
		help := strings.Index(out, "# HELP "+m)
		first := strings.Index(out, m+"{")
		if help < 0 || first < 0 || help > first {
			t.Fatalf("%s: HELP at %d, first sample at %d", m, help, first)
		}
	}
}

// A series per flow would be tens of thousands of series; a series per device
// is a few dozen. This is the guard on that.
func TestPrometheusKeepsCardinalityPerDevice(t *testing.T) {
	s := sample()
	s.Flows = 20000
	var b strings.Builder
	if err := Prometheus(&b, s); err != nil {
		t.Fatal(err)
	}
	samples := 0
	for _, line := range strings.Split(b.String(), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			samples++
		}
	}
	// 6 singletons + per device: bytes up/down, packets up/down, flows,
	// handshakes, hidden names, finished bytes up/down, closed flows. Ten per
	// device and not one more: the moment a destination name becomes a label
	// this number grows with the internet.
	if want := 6 + 10*len(s.Devices); samples != want {
		t.Fatalf("expected %d samples, got %d: something scales with flows, not devices", want, samples)
	}
}

func TestZabbixJSONAndDiscovery(t *testing.T) {
	var b strings.Builder
	if err := JSON(&b, sample()); err != nil {
		t.Fatal(err)
	}
	var back Snapshot
	if err := json.Unmarshal([]byte(b.String()), &back); err != nil {
		t.Fatalf("the JSON a Zabbix item would read does not parse: %v", err)
	}
	if back.Flows != 41 || len(back.Devices) != 2 || back.Devices[0].Name == "" ||
		back.Devices[0].UpBytes == 0 || back.Devices[0].DownBytes == 0 {
		t.Fatalf("round trip lost data: %+v", back)
	}

	b.Reset()
	if err := Discovery(&b, sample()); err != nil {
		t.Fatal(err)
	}
	var d struct {
		Data []map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(b.String()), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Data) != 2 {
		t.Fatalf("discovery should list both devices: %+v", d.Data)
	}
	for _, macro := range []string{"{#DEVICE}", "{#ADDR}", "{#FAMILY}"} {
		if _, ok := d.Data[0][macro]; !ok {
			t.Fatalf("Zabbix needs %s in discovery: %+v", macro, d.Data[0])
		}
	}
	if d.Data[0]["{#FAMILY}"] != "ipv4" {
		t.Fatalf("family wrong: %+v", d.Data[0])
	}

	// an empty house must still produce valid discovery, not "null"
	b.Reset()
	if err := Discovery(&b, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "null") {
		t.Fatalf("empty discovery must be an empty list, got %s", b.String())
	}
}

// Accounting being off is a condition to publish, not a zero to draw.
func TestAccountingOffIsExported(t *testing.T) {
	s := sample()
	s.AcctOff = true
	var b strings.Builder
	if err := Prometheus(&b, s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "netryx_accounting_off 1") {
		t.Fatalf("a scraper must be able to see that the kernel is not counting:\n%s", b.String())
	}
	b.Reset()
	if err := JSON(&b, s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"accounting_off": true`) {
		t.Fatalf("the JSON must carry it too: %s", b.String())
	}
	ok := sample()
	b.Reset()
	if err := Prometheus(&b, ok); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "netryx_accounting_off 0") {
		t.Fatal("and must read 0 when accounting is on")
	}
}

// A counter that falls is a lie to a scraper: every close would read as a
// reset and rate() would be noise. These volumes come from the connections
// open at the moment of the scrape, so they are gauges.
func TestOpenConnectionVolumesAreGaugesNotCounters(t *testing.T) {
	var b strings.Builder
	if err := Prometheus(&b, sample()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, m := range []string{"netryx_device_bytes", "netryx_device_packets", "netryx_device_flows"} {
		if !strings.Contains(out, "# TYPE "+m+" gauge") {
			t.Fatalf("%s must be a gauge:\n%s", m, out)
		}
	}
	// The _total suffix belongs to the finished connections, which only ever
	// rise. The two must be different metrics with different types, or a
	// scraper reads a falling counter and every close becomes a reset.
	if !strings.Contains(out, "# TYPE netryx_device_bytes_total counter") {
		t.Fatalf("finished volumes are a counter:\n%s", out)
	}
	s := Snapshot{Devices: []Device{{
		Name: "phone", Addr: "192.168.1.9",
		UpBytes: 7, DownBytes: 9, TotalUpBytes: 5000, TotalDownBytes: 9000, ClosedFlows: 3,
	}}}
	b.Reset()
	if err := Prometheus(&b, s); err != nil {
		t.Fatal(err)
	}
	out = b.String()
	if !strings.Contains(out, `netryx_device_bytes{device="phone",addr="192.168.1.9",family="ipv4",direction="up"} 7`) {
		t.Fatalf("the gauge carries what is open now:\n%s", out)
	}
	if !strings.Contains(out, `netryx_device_bytes_total{device="phone",addr="192.168.1.9",family="ipv4",direction="up"} 5000`) {
		t.Fatalf("the counter carries what has finished:\n%s", out)
	}
	if !strings.Contains(out, `netryx_device_flows_closed_total{device="phone",addr="192.168.1.9",family="ipv4"} 3`) {
		t.Fatalf("finished connections must be counted:\n%s", out)
	}
}

// Handshakes accumulate for the life of the process, so they are counters,
// and the names behind them stay out of the labels: one series per name per
// device would multiply this router's metric count by the internet.
func TestHandshakesAreCountersAndNamesAreNotLabels(t *testing.T) {
	s := Snapshot{Devices: []Device{{
		Name: "phone", Addr: "192.168.1.115", MAC: "4e:c2:94:9f:07:79",
		Handshakes: 12, Hidden: 4,
		Sites: []string{"api.telegram.org", "discord.com"},
	}}}
	var b strings.Builder
	if err := Prometheus(&b, s); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"# TYPE netryx_device_handshakes_total counter",
		`netryx_device_handshakes_total{device="phone",addr="192.168.1.115",mac="4e:c2:94:9f:07:79",family="ipv4"} 12`,
		"# TYPE netryx_device_hidden_names_total counter",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "api.telegram.org") || strings.Contains(out, "discord.com") {
		t.Fatalf("a destination name must never become a label:\n%s", out)
	}
}

// A device's name is whatever that device told the DHCP server it was called,
// so it is somebody else's string on a page the household opens. It must not
// be able to bring its own markup, and the page must not fetch anything from
// anywhere: a monitor that loads a font from a third party reports the
// household to that third party on every page load.
func TestThePageEscapesWhatDevicesCallThemselvesAndFetchesNothing(t *testing.T) {
	s := Snapshot{
		Taken: time.Now(), Flows: 3,
		Devices: []Device{{
			Name: `<script>alert(1)</script>`, Addr: "192.168.1.9", MAC: "aa:bb:cc:dd:ee:ff",
			UpBytes: 3 * 1024 * 1024, DownBytes: 900, Flows: 2, Handshakes: 10, Hidden: 2,
			Sites: []string{`evil"><img src=x onerror=alert(1)>`, "api.telegram.org"},
		}},
	}
	var b strings.Builder
	if err := Page(&b, s); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	// What matters is that no TAG of theirs survives. The words may well
	// appear, escaped, in the middle of the text: that is the escaping
	// working, not failing.
	for _, tag := range []string{"<script>alert", "<img src=x", `"><img`} {
		if strings.Contains(out, tag) {
			t.Fatalf("a device brought its own markup onto the page (%q):\n%s", tag, out)
		}
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("the name must still be readable, escaped: %s", out)
	}
	for _, forbidden := range []string{"http://", "https://", "//fonts.", "cdn."} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("the page must fetch nothing from outside, found %q", forbidden)
		}
	}
	// The numbers a person reads: volumes in units, and the share of
	// handshakes whose name was hidden rather than a bare count.
	if !strings.Contains(out, "3.0 MB") {
		t.Fatalf("volumes must be readable: %s", out)
	}
	if !strings.Contains(out, "2 (20%)") {
		t.Fatalf("hidden names mean nothing without their share: %s", out)
	}
	if !strings.Contains(out, "api.telegram.org") {
		t.Fatal("the destinations are the point of the page")
	}
}

func TestAnEmptyPageSaysSoRatherThanLookingBroken(t *testing.T) {
	var b strings.Builder
	if err := Page(&b, Snapshot{Taken: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Nothing to see yet") {
		t.Fatalf("an empty table must explain itself:\n%s", b.String())
	}
	// And accounting being off is said on the page, not left as a column of
	// zeros that reads as a quiet network.
	b.Reset()
	if err := Page(&b, Snapshot{Taken: time.Now(), AcctOff: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "nf_conntrack_acct") {
		t.Fatalf("zeros must be explained:\n%s", b.String())
	}
}

// A monitor that silently misses what it cannot keep up with is worse than
// one that says so: the first reads as a quiet network.
func TestLostCopiesArePublishedRatherThanHidden(t *testing.T) {
	var b strings.Builder
	if err := Prometheus(&b, Snapshot{LostCopies: 12}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "# TYPE netryx_copies_lost_total counter\nnetryx_copies_lost_total 12") {
		t.Fatalf("the loss must be a counter and it must be there:\n%s", b.String())
	}
	b.Reset()
	if err := Page(&b, Snapshot{Taken: time.Now(), LostCopies: 12}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "12 copies") {
		t.Fatalf("and the page must say it, not only the metrics:\n%s", b.String())
	}
	// Nothing lost means nothing said: a warning that is always there is not
	// read when it matters.
	b.Reset()
	if err := Page(&b, Snapshot{Taken: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "copies") {
		t.Fatalf("a warning with nothing behind it teaches people to ignore warnings:\n%s", b.String())
	}
}
