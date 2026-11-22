// Package wan turns the router's view of the internet line into numbers the
// console can show: up or down, addresses, uptime, throughput, latency,
// jitter and loss. It only parses and accumulates; running ubus, ping and
// reading sysfs is the caller's job, so every rule here is testable offline.
package wan

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Status is the internet line as the console shows it.
type Status struct {
	Up     bool
	Device string
	IPv4   string
	IPv6   string
	Uptime time.Duration
	// RxRate and TxRate are bytes per second.
	RxRate, TxRate float64
	LatencyMs      float64
	JitterMs       float64
	LossPct        float64
	// LatencyOK mirrors the ok result of Prober.Stats: true when the window
	// held a successful probe, so LatencyMs and JitterMs are measured. The
	// wiring that reads Stats sets it; until then it stays false and the
	// console reads latency as no data.
	LatencyOK bool
}

// ubusInterface holds only the fields of `ubus call network.interface.wan status`
// that Status needs; the rest of the document is ignored on purpose.
type ubusInterface struct {
	Up          bool   `json:"up"`
	Device      string `json:"device"`
	L3Device    string `json:"l3_device"`
	Uptime      int64  `json:"uptime"`
	IPv4Address []struct {
		Address string `json:"address"`
	} `json:"ipv4-address"`
	IPv6Address []struct {
		Address string `json:"address"`
	} `json:"ipv6-address"`
}

// ParseUbusInterface reads the JSON printed by `ubus call network.interface.wan status`.
// The layer 3 device is preferred over the configured device because that is
// the interface the traffic actually leaves through (PPPoE, VLAN, bridge), and
// the configured device is the fallback for a line that is still coming up.
// Uptime is the ubus "uptime" field, which is in seconds.
func ParseUbusInterface(js []byte) (Status, error) {
	var u ubusInterface
	if err := json.Unmarshal(js, &u); err != nil {
		return Status{}, fmt.Errorf("parse ubus interface status: %w", err)
	}
	st := Status{
		Up:     u.Up,
		Device: u.Device,
		Uptime: time.Duration(u.Uptime) * time.Second,
	}
	if u.L3Device != "" {
		st.Device = u.L3Device
	}
	if len(u.IPv4Address) > 0 {
		st.IPv4 = u.IPv4Address[0].Address
	}
	// wan6 reports its IPv6 on a second interface, so this is often empty;
	// it is filled in whenever the same document carries an address.
	if len(u.IPv6Address) > 0 {
		st.IPv6 = u.IPv6Address[0].Address
	}
	return st, nil
}

// timeRe matches the round trip in both busybox ("time=9.812 ms") and
// iputils ("time=9.81 ms") output. A timeout prints no such field, so the
// absence of a match is how a lost probe is recognised.
var timeRe = regexp.MustCompile(`time=([0-9]+(?:\.[0-9]+)?) ms`)

// ParsePing returns the round-trip time of one probe, and false when the
// probe got no reply. The caller must run ping with `-c 1 -W 1`: one echo per
// probe keeps one output line equal to one sample, and the one-second wait
// bounds how long a dead line can stall the 10 second loop.
func ParsePing(out string) (ms float64, ok bool) {
	m := timeRe.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// windowSize is how many recent probes the Prober remembers. Thirty probes at
// a 10 second interval is five minutes: long enough to smooth one drop,
// short enough that loss recovers after an outage ends.
const windowSize = 30

type probe struct {
	ms float64
	ok bool
}

// Prober keeps the last windowSize probe results. The zero value is ready to use.
type Prober struct {
	window []probe
}

// Add records one probe. A failed probe passes ms as zero and ok as false.
func (p *Prober) Add(ms float64, ok bool) {
	p.window = append(p.window, probe{ms: ms, ok: ok})
	if len(p.window) > windowSize {
		// Shift in place so a long-running console does not keep growing the backing array.
		copy(p.window, p.window[len(p.window)-windowSize:])
		p.window = p.window[:windowSize]
	}
}

// Stats summarises the window. Latency is the mean of the successful probes,
// so it sits on the same window as loss and jitter. Jitter is the mean
// absolute difference between consecutive successful probes; failed probes
// are skipped there so a drop does not register as a latency swing. Loss is
// failed over total, in percent.
//
// ok is false when the window holds no successful probe. Then latency and
// jitter are zero, and the caller must show "no reply" rather than 0 ms.
// Loss is still reported, and is 100 on a dead line.
func (p *Prober) Stats() (latency, jitter, loss float64, ok bool) {
	var (
		failed    int
		succeeded int
		sum       float64
		prev      float64
		prevOK    bool
		diffSum   float64
		diffCount int
	)
	for _, pr := range p.window {
		if !pr.ok {
			failed++
			continue
		}
		succeeded++
		sum += pr.ms
		if prevOK {
			diffSum += math.Abs(pr.ms - prev)
			diffCount++
		}
		prev = pr.ms
		prevOK = true
	}
	if succeeded > 0 {
		latency = sum / float64(succeeded)
		ok = true
	}
	if diffCount > 0 {
		jitter = diffSum / float64(diffCount)
	}
	if n := len(p.window); n > 0 {
		loss = 100 * float64(failed) / float64(n)
	}
	return latency, jitter, loss, ok
}

const (
	// counter32Max is the largest value a 32-bit statistics counter reaches
	// before it wraps to zero. OpenWrt on mips and arm32 exposes these.
	counter32Max  = 1<<32 - 1
	counter32Span = 1 << 32
	// maxWrapBytesPerSec is 10 Gbit/s. A drop that would need more traffic
	// than this to be a wrap is treated as a reset instead.
	maxWrapBytesPerSec = 10e9 / 8
)

// Counter turns the cumulative byte counters of one interface into rates.
// It reads through an fs.FS so tests can supply the counters without a real
// network device; in production the FS is os.DirFS("/").
type Counter struct {
	fsys   fs.FS
	dev    string
	wrap32 bool
	primed bool
	at     time.Time
	lastRx uint64
	lastTx uint64
}

// NewCounter returns a Counter for dev, for example "eth0".
func NewCounter(fsys fs.FS, dev string) *Counter {
	return newCounter(fsys, dev, strconv.IntSize == 32)
}

// newCounter takes wrap32 explicitly so tests do not depend on the word size
// of the machine they run on.
func newCounter(fsys fs.FS, dev string, wrap32 bool) *Counter {
	return &Counter{fsys: fsys, dev: dev, wrap32: wrap32}
}

// Sample reads both counters and returns their rate in bytes per second since
// the previous sample. The first sample only sets the baseline and returns zero
// rates. A counter that went down is either a 32-bit wrap, which is
// measured across the 2^32 span, or a reset, which reports zero. Either way
// the new value becomes the baseline, and a rate is never negative.
func (c *Counter) Sample(now time.Time) (rx, tx float64, err error) {
	cur, err := c.read("rx_bytes")
	if err != nil {
		return 0, 0, err
	}
	curTx, err := c.read("tx_bytes")
	if err != nil {
		return 0, 0, err
	}

	if !c.primed {
		c.primed = true
		c.at, c.lastRx, c.lastTx = now, cur, curTx
		return 0, 0, nil
	}

	dt := now.Sub(c.at).Seconds()
	if dt > 0 {
		rx = counterRate(c.lastRx, cur, dt, c.wrap32)
		tx = counterRate(c.lastTx, curTx, dt, c.wrap32)
	}
	c.at, c.lastRx, c.lastTx = now, cur, curTx
	return rx, tx, nil
}

// counterRate is the bytes per second between two readings of one counter.
// A drop is a 32-bit wrap only on a 32-bit build, where the kernel's
// unsigned long, and so the sysfs counter, is 32 bits wide. There a drop
// from a value at or below 2^32-1 is measured across the 2^32 span, as long
// as that does not imply more than 10 Gbit/s. On a 64-bit build the counter
// is 64 bits wide, so every drop is a reset (rate 0): read as a wrap, an
// interface recreated at a low value would report about 429 MB/s of traffic
// that never happened.
func counterRate(last, cur uint64, dt float64, wrap32 bool) float64 {
	if cur >= last {
		return float64(cur-last) / dt
	}
	if wrap32 && last <= counter32Max {
		wrapped := float64(counter32Span - last + cur)
		if wrapped/dt <= maxWrapBytesPerSec {
			return wrapped / dt
		}
	}
	return 0
}

func (c *Counter) read(name string) (uint64, error) {
	// fs.FS paths are relative and slash-separated, so no leading slash.
	path := "sys/class/net/" + c.dev + "/statistics/" + name
	b, err := fs.ReadFile(c.fsys, path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return v, nil
}
