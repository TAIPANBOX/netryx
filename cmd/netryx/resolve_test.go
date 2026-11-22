package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"netryx/internal/ct"
	"netryx/internal/neigh"
	"netryx/internal/rollup"
)

// neighbours stands in for the kernel's neighbour table.
func neighbours(es ...neigh.Entry) func() ([]neigh.Entry, error) {
	return func() ([]neigh.Entry, error) { return es, nil }
}

func apiGet(t *testing.T, c *console, path string, v any) {
	t.Helper()
	rec := httptest.NewRecorder()
	c.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

type apiDevice struct {
	MAC    string `json:"mac"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
	Rate   struct {
		Up   float64 `json:"up"`
		Down float64 `json:"down"`
	} `json:"rate"`
}

type apiOverview struct {
	Clients struct {
		Online int `json:"online"`
	} `json:"clients"`
	TopClients []struct {
		MAC   string `json:"mac"`
		Name  string `json:"name"`
		Other bool   `json:"other"`
	} `json:"topClients"`
}

// flowEvents is one TCP connection as ct reports it, and a dump row of it
// with the counters so far.
type flowEvents struct {
	id         uint32
	orig, rply ct.Tuple
}

func connection(id uint32, src string, sport uint16, dst string, dport uint16) flowEvents {
	return flowEvents{id: id, orig: tuple(src, dst, sport, dport), rply: tuple(dst, src, dport, sport)}
}

func (f flowEvents) ev(k ct.Kind, up, down uint64) ct.Event {
	return ct.Event{Kind: k, ID: f.id, Orig: f.orig, Reply: f.rply, Up: ct.Counters{Bytes: up}, Down: ct.Counters{Bytes: down}}
}

// run takes connections through the console the way start does: their NEWs,
// two dumps five seconds apart with bytes moving, and, if end is set, their
// DESTROYs.
func run(c *console, clk *clock, t0 time.Time, end bool, fs ...flowEvents) {
	for _, f := range fs {
		c.event(f.ev(ct.New, 0, 0), t0)
	}
	dump := func(up, down uint64) []ct.Event {
		var d []ct.Event
		for _, f := range fs {
			d = append(d, f.ev(ct.Update, up, down))
		}
		return d
	}
	clk.Set(t0.Add(5 * time.Second))
	c.table.Sync(dump(0, 0), t0.Add(5*time.Second))
	clk.Set(t0.Add(10 * time.Second))
	c.table.Sync(dump(1000, 50000), t0.Add(10*time.Second))
	if end {
		clk.Set(t0.Add(11 * time.Second))
		for _, f := range fs {
			c.event(f.ev(ct.Destroy, 1000, 50000), t0.Add(11*time.Second))
		}
	}
}

var noon = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// A device on a fixed address, whose handshake was never read and which holds
// no lease, is known only from the neighbour table. Through the real flow
// table, collector and inventory it is one device, online, with the rate it
// is moving bytes at.
func TestADeviceKnownOnlyFromTheNeighbourTableHasItsRate(t *testing.T) {
	const mac = "aa:bb:cc:00:00:30"
	clk := &clock{t: noon}
	o := testOptions(t)
	o.Now = clk.Now
	o.Neigh = neighbours(neigh.Entry{IP: net.ParseIP("192.168.50.30"), MAC: mac, State: neigh.StateReachable})
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	run(c, clk, noon, false, connection(1, "192.168.50.30", 50001, "1.1.1.1", 443))

	var devs []apiDevice
	apiGet(t, c, "/api/devices", &devs)
	if len(devs) != 1 || devs[0].MAC != mac {
		t.Fatalf("one device, under its hardware address: %+v", devs)
	}
	if devs[0].Rate.Down != 10000 || devs[0].Rate.Up != 200 || !devs[0].Online {
		t.Fatalf("it moves 50 000 bytes in 5 s and is online: %+v", devs[0])
	}
	var ov apiOverview
	apiGet(t, c, "/api/overview?range=1h", &ov)
	if ov.Clients.Online != 1 {
		t.Fatalf("one client online: %+v", ov.Clients)
	}
}

// An SSH tunnel polling the console from the router's loopback is the
// console's own access, not a client and not traffic.
func TestTheConsoleTunnelIsNotAClient(t *testing.T) {
	clk := &clock{t: noon}
	o := testOptions(t)
	o.Now = clk.Now
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	run(c, clk, noon, true,
		connection(1, "127.0.0.1", 40000, "127.0.0.1", 8788),
		connection(2, "::1", 40001, "::1", 8788))

	var devs []apiDevice
	apiGet(t, c, "/api/devices", &devs)
	if len(devs) != 0 {
		t.Fatalf("the tunnel became a client: %+v", devs)
	}
	if rows := c.store.Top(rollup.Filter{}, "mac", noon.Add(-time.Hour), noon.Add(time.Hour), 0); len(rows) != 0 {
		t.Fatalf("the tunnel's bytes reached history: %+v", rows)
	}
	if r := c.table.Rates(); len(r) != 0 {
		t.Fatalf("the tunnel has a rate: %+v", r)
	}
}

// The router's own connections (its WAN address talking to the internet) are
// the router's: one fixed key that the overview shows as a service row, never
// a client in the list and never a row per router address.
func TestTheRoutersOwnTrafficIsTheRouter(t *testing.T) {
	const mac = "aa:bb:cc:00:00:30"
	clk := &clock{t: noon}
	o := testOptions(t)
	o.Now = clk.Now
	o.Neigh = neighbours(neigh.Entry{IP: net.ParseIP("192.168.50.30"), MAC: mac, State: neigh.StateReachable})
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	run(c, clk, noon, false,
		connection(1, "203.0.113.7", 40000, "1.1.1.1", 443),  // the router's WAN address
		connection(2, "192.168.50.1", 40001, "9.9.9.9", 853), // its house address
		connection(3, "192.168.50.30", 50001, "1.1.1.1", 443))
	if r := c.table.Rates()["router"]; r.Down != 20000 {
		t.Fatalf("both router flows are the router's rate: %+v", c.table.Rates())
	}
	clk.Set(noon.Add(11 * time.Second))
	for _, f := range []flowEvents{connection(1, "203.0.113.7", 40000, "1.1.1.1", 443), connection(2, "192.168.50.1", 40001, "9.9.9.9", 853), connection(3, "192.168.50.30", 50001, "1.1.1.1", 443)} {
		c.event(f.ev(ct.Destroy, 1000, 50000), noon.Add(11*time.Second))
	}
	keys := map[string]bool{}
	for _, r := range c.store.Top(rollup.Filter{}, "mac", noon.Add(-time.Hour), noon.Add(time.Hour), 0) {
		keys[r.Key] = true
	}
	if len(keys) != 2 || !keys["router"] || !keys[mac] {
		t.Fatalf("history holds the router and the client, nothing per address: %v", keys)
	}
	var devs []apiDevice
	apiGet(t, c, "/api/devices", &devs)
	if len(devs) != 1 || devs[0].MAC != mac {
		t.Fatalf("the router is not a client: %+v", devs)
	}
	var ov apiOverview
	apiGet(t, c, "/api/overview?range=1h", &ov)
	found := false
	for _, tc := range ov.TopClients {
		if tc.MAC == "router" {
			found = true
			if !tc.Other || tc.Name != "router" {
				t.Errorf("the router's row is a service row named router: %+v", tc)
			}
		}
	}
	if !found {
		t.Fatalf("the overview shows the router's traffic: %+v", ov.TopClients)
	}
}

// Connection events the kernel could not deliver are logged when their count
// changes, once, and reach the overview.
func TestLostEventsAreLoggedWhenTheyChange(t *testing.T) {
	c, err := buildConsole(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	c.logLost()
	atomic.AddUint64(&c.lost, 3)
	c.logLost()
	c.logLost()
	if n := strings.Count(buf.String(), "did not deliver 3 connection events"); n != 1 {
		t.Fatalf("logged %d times: %q", n, buf.String())
	}
	var ov struct {
		LostEvents uint64 `json:"lostEvents"`
	}
	apiGet(t, c, "/api/overview", &ov)
	if ov.LostEvents != 3 {
		t.Fatalf("the overview says how many were lost: %+v", ov)
	}
}
