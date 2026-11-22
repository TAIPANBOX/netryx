// Package export hands the same numbers to whatever the operator already runs.
//
// The dashboard in this package's sibling is for someone who installed one
// package and wants a page; these two endpoints are for someone who already
// has Prometheus and Grafana on a NAS, or Zabbix at work, and would rather
// keep one place to look. Neither costs a dependency or a byte of flash.
package export

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"
)

// Device is one machine in the house as the exporters see it. Deliberately an
// aggregate: a series per device is a few dozen, a series per flow is tens of
// thousands and would ruin whatever it is scraped into.
type Device struct {
	Name  string `json:"name"`
	Addr  string `json:"addr"`
	MAC   string `json:"mac,omitempty"`
	Flows int    `json:"flows"`
	// Up and down are kept apart: a device that sends far more than it
	// receives is the shape worth noticing, and one total hides it.
	UpPackets   int  `json:"up_packets"`
	UpBytes     int  `json:"up_bytes"`
	DownPackets int  `json:"down_packets"`
	DownBytes   int  `json:"down_bytes"`
	NATed       bool `json:"nated"`
	// Sites are the services this device asked for by name, most asked first.
	// They come from the capture, never from the connection table, and they
	// are JSON only: one metric series per name per device would multiply the
	// cardinality by the internet.
	Sites []string `json:"sites,omitempty"`
	// Handshakes counts starts of connections seen for this device, and
	// Hidden the ones whose name was encrypted by the client.
	Handshakes int `json:"handshakes,omitempty"`
	Hidden     int `json:"hidden_names,omitempty"`
	// These are the volumes of connections that have FINISHED, accumulated
	// since the service started. Unlike the figures above they never fall,
	// which is what makes them answer "who used the line today".
	TotalUpBytes   int `json:"total_up_bytes,omitempty"`
	TotalDownBytes int `json:"total_down_bytes,omitempty"`
	ClosedFlows    int `json:"closed_flows,omitempty"`
	// Offloaded flows are counted by the flow table or the hardware and
	// refresh in bursts, so these figures are approximate.
	Offloaded bool `json:"offloaded,omitempty"`
}

// Snapshot is everything the exporters publish at one moment.
type Snapshot struct {
	Taken   time.Time `json:"taken"`
	Devices []Device  `json:"devices"`
	Flows   int       `json:"flows_total"`
	// AcctOff says the kernel is not counting bytes at all, so every volume
	// here would be zero. Published rather than hidden, because a chart of
	// zeros looks like a quiet network.
	AcctOff  bool `json:"accounting_off"`
	Findings int  `json:"findings_total"`
	Blocked  int  `json:"rules_blocked"`
	// LostCopies is how many selected packets the kernel could not hand over,
	// counted from its own numbering. Published rather than hidden: a monitor
	// that silently misses what it cannot keep up with is worse than one that
	// says it did.
	LostCopies int `json:"lost_copies"`
}

// Prometheus writes the text exposition format. Names follow the convention:
// a unit suffix, a _total suffix on counters, and no series per flow.
func Prometheus(w io.Writer, s Snapshot) error {
	b := &strings.Builder{}
	fmt.Fprintf(b, "# HELP netryx_up whether the collector produced this scrape\n# TYPE netryx_up gauge\nnetryx_up 1\n")
	fmt.Fprintf(b, "# HELP netryx_flows_total connections currently tracked by the kernel\n# TYPE netryx_flows_total gauge\nnetryx_flows_total %d\n", s.Flows)
	fmt.Fprintf(b, "# HELP netryx_findings_total anomalies kept since start\n# TYPE netryx_findings_total counter\nnetryx_findings_total %d\n", s.Findings)
	fmt.Fprintf(b, "# HELP netryx_block_rules blocking rules in force\n# TYPE netryx_block_rules gauge\nnetryx_block_rules %d\n", s.Blocked)
	fmt.Fprintf(b, "# HELP netryx_copies_lost_total selected packets the kernel numbered and could not deliver\n# TYPE netryx_copies_lost_total counter\nnetryx_copies_lost_total %d\n", s.LostCopies)
	fmt.Fprintf(b, "# HELP netryx_accounting_off 1 when the kernel is not counting bytes, so every volume reads zero\n# TYPE netryx_accounting_off gauge\nnetryx_accounting_off %d\n", boolVal(s.AcctOff))
	// A gauge, not a counter: these figures come from the connections that are
	// open right now, so they fall when a connection closes. Declared as a
	// counter, a scraper would read every close as a counter reset and rate()
	// would be noise. The accumulated totals belong to the DESTROY events,
	// which is a separate metric and not this one.
	fmt.Fprintf(b, "# HELP netryx_device_bytes bytes on the connections a device has open now\n# TYPE netryx_device_bytes gauge\n")
	for _, d := range sorted(s.Devices) {
		fmt.Fprintf(b, "netryx_device_bytes{%s,direction=\"up\"} %d\n", labels(d), d.UpBytes)
		fmt.Fprintf(b, "netryx_device_bytes{%s,direction=\"down\"} %d\n", labels(d), d.DownBytes)
	}
	fmt.Fprintf(b, "# HELP netryx_device_packets packets on the connections a device has open now\n# TYPE netryx_device_packets gauge\n")
	for _, d := range sorted(s.Devices) {
		fmt.Fprintf(b, "netryx_device_packets{%s,direction=\"up\"} %d\n", labels(d), d.UpPackets)
		fmt.Fprintf(b, "netryx_device_packets{%s,direction=\"down\"} %d\n", labels(d), d.DownPackets)
	}
	fmt.Fprintf(b, "# HELP netryx_device_flows connections open for a device\n# TYPE netryx_device_flows gauge\n")
	for _, d := range sorted(s.Devices) {
		fmt.Fprintf(b, "netryx_device_flows{%s} %d\n", labels(d), d.Flows)
	}
	// These two come from the capture rather than the connection table, and
	// unlike the volumes above they only ever go up, so they are counters.
	// The names themselves stay in the JSON: a label per destination would
	// give this router as many series as the house has destinations.
	fmt.Fprintf(b, "# HELP netryx_device_handshakes_total starts of connections seen for a device since this process started\n# TYPE netryx_device_handshakes_total counter\n")
	for _, d := range sorted(s.Devices) {
		fmt.Fprintf(b, "netryx_device_handshakes_total{%s} %d\n", labels(d), d.Handshakes)
	}
	fmt.Fprintf(b, "# HELP netryx_device_hidden_names_total handshakes whose destination name was encrypted by the client\n# TYPE netryx_device_hidden_names_total counter\n")
	for _, d := range sorted(s.Devices) {
		fmt.Fprintf(b, "netryx_device_hidden_names_total{%s} %d\n", labels(d), d.Hidden)
	}
	// Finished connections, accumulated. These are the counters a rate() is
	// meant for: unlike the gauges above they do not fall when a device
	// closes what it had open.
	fmt.Fprintf(b, "# HELP netryx_device_bytes_total bytes on connections that have finished, since this process started\n# TYPE netryx_device_bytes_total counter\n")
	for _, d := range sorted(s.Devices) {
		fmt.Fprintf(b, "netryx_device_bytes_total{%s,direction=\"up\"} %d\n", labels(d), d.TotalUpBytes)
		fmt.Fprintf(b, "netryx_device_bytes_total{%s,direction=\"down\"} %d\n", labels(d), d.TotalDownBytes)
	}
	fmt.Fprintf(b, "# HELP netryx_device_flows_closed_total connections of this device that have finished\n# TYPE netryx_device_flows_closed_total counter\n")
	for _, d := range sorted(s.Devices) {
		fmt.Fprintf(b, "netryx_device_flows_closed_total{%s} %d\n", labels(d), d.ClosedFlows)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func boolVal(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sorted(in []Device) []Device {
	out := append([]Device(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out
}

func labels(d Device) string {
	parts := []string{
		`device="` + escape(d.Name) + `"`,
		`addr="` + escape(d.Addr) + `"`,
	}
	if d.MAC != "" {
		parts = append(parts, `mac="`+escape(d.MAC)+`"`)
	}
	parts = append(parts, fmt.Sprintf(`family="%s"`, family(d.Addr)))
	return strings.Join(parts, ",")
}

func family(addr string) string {
	ip := net.ParseIP(addr)
	switch {
	case ip == nil:
		return "unknown"
	case ip.To4() != nil:
		return "ipv4"
	default:
		return "ipv6"
	}
}

// escape follows the exposition format: backslash, quote and newline only.
// A device is named by its owner, so the name can contain anything.
func escape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}

// JSON is the whole snapshot, for a Zabbix HTTP agent item with dependent
// items, or for anything else that would rather parse than scrape.
func JSON(w io.Writer, s Snapshot) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// Discovery is Zabbix low-level discovery: the macros it expects, so a host
// gets an item per device without anyone listing them by hand.
func Discovery(w io.Writer, s Snapshot) error {
	type row map[string]string
	out := struct {
		Data []row `json:"data"`
	}{}
	for _, d := range sorted(s.Devices) {
		out.Data = append(out.Data, row{
			"{#DEVICE}": d.Name,
			"{#ADDR}":   d.Addr,
			"{#MAC}":    d.MAC,
			"{#FAMILY}": family(d.Addr),
		})
	}
	if out.Data == nil {
		out.Data = []row{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
