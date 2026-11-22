package main

import (
	"encoding/binary"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netryx/internal/ct"
	"netryx/internal/nflog"
)

// dnsAnswerPacket is the router's resolver answering a device: name is ip.
func dnsAnswerPacket(name, ip, from, to string) nflog.Packet {
	q := make([]byte, 12)
	binary.BigEndian.PutUint16(q[0:2], 0x4242)
	binary.BigEndian.PutUint16(q[2:4], 0x8180)
	binary.BigEndian.PutUint16(q[4:6], 1)
	binary.BigEndian.PutUint16(q[6:8], 1)
	for _, l := range strings.Split(name, ".") {
		q = append(q, byte(len(l)))
		q = append(q, l...)
	}
	q = append(q, 0, 0, 1, 0, 1)
	q = append(q, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 1, 44, 0, 4)
	q = append(q, net.ParseIP(ip).To4()...)
	pkt := make([]byte, 28+len(q))
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[9] = 17
	copy(pkt[12:16], net.ParseIP(from).To4())
	copy(pkt[16:20], net.ParseIP(to).To4())
	binary.BigEndian.PutUint16(pkt[20:22], 53)
	binary.BigEndian.PutUint16(pkt[22:24], 40000)
	binary.BigEndian.PutUint16(pkt[24:26], uint16(8+len(q)))
	copy(pkt[28:], q)
	return nflog.Packet{Payload: pkt}
}

// The kernel answers a dump of its connection table with CT_NEW messages,
// so every dump row reads as a NEW. A live flow must still be named, placed
// and given its app once it can be, and carry that on the flows screen,
// not only once it has ended.
func TestALiveFlowCarriesItsNameAppAndCountry(t *testing.T) {
	clk := &clock{t: noon}
	o := testOptions(t)
	o.Now = clk.Now
	dir := t.TempDir()
	o.Geo = filepath.Join(dir, "ip2asn.tsv")
	o.Apps = filepath.Join(dir, "appmap.tsv")
	write(t, o.Geo, "1.1.1.0\t1.1.1.255\t13335\tUS\tCLOUDFLARENET\n")
	write(t, o.Apps, "o\tcloudflare\tcloudflare\tCloudflare\tcdn\n")
	c, err := buildConsole(o)
	if err != nil {
		t.Fatal(err)
	}
	c.col.Observe(dnsAnswerPacket("one.one.one.one", "1.1.1.1", "192.168.50.1", "192.168.50.30"), noon)
	f := connection(1, "192.168.50.30", 50001, "1.1.1.1", 443)
	c.event(f.ev(ct.New, 0, 0), noon)
	// Dump rows as the kernel sends them: CT_NEW.
	clk.Set(noon.Add(5 * time.Second))
	c.table.Sync([]ct.Event{f.ev(ct.New, 500, 5000)}, noon.Add(5*time.Second))
	clk.Set(noon.Add(10 * time.Second))
	c.table.Sync([]ct.Event{f.ev(ct.New, 1000, 50000)}, noon.Add(10*time.Second))

	live := c.table.Live()
	if len(live) != 1 || live[0].Name != "one.one.one.one" || live[0].App.ID != "cloudflare" || live[0].Geo.CC != "US" {
		t.Fatalf("the live flow is stamped: %+v", live)
	}
	var page struct {
		Rows []struct {
			Name string `json:"name"`
			CC   string `json:"cc"`
			ASN  uint32 `json:"asn"`
			App  struct {
				ID string `json:"id"`
			} `json:"app"`
		} `json:"rows"`
	}
	apiGet(t, c, "/api/flows?live=1", &page)
	if len(page.Rows) != 1 || page.Rows[0].Name != "one.one.one.one" || page.Rows[0].App.ID != "cloudflare" ||
		page.Rows[0].CC != "US" || page.Rows[0].ASN != 13335 {
		t.Fatalf("the flows screen shows the stamp: %+v", page.Rows)
	}
}
