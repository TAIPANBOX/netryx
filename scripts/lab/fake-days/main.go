// fake-days writes made-up daily files for the lab, so the screens that need
// weeks of days (a device's usual week) can be seen before the lab has lived
// them. It writes only days of the window that have no file, never over one,
// and prints each file it wrote so they can be taken away again. Lab only: it
// is never packaged.
//
//	fake-days -dir /etc/netryx/daily -mac 02:00:00:00:00:01,02:00:00:00:00:02 -primary wan -backup lte
//
// With -primary and -backup each day also carries the lines' counters read at
// its close: the primary moving about 10 GiB a day, the backup its checks'
// few MiB and, on a few days, the traffic of a failover.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"time"

	"netryx/internal/daily"
)

func main() {
	dir := flag.String("dir", "/etc/netryx/daily", "the daily files' directory")
	macs := flag.String("mac", "", "comma-separated hardware addresses to give days to")
	primary := flag.String("primary", "", "the primary line's network, to give counters to")
	backup := flag.String("backup", "", "the backup line's network, to give counters to")
	flag.Parse()
	now := time.Now()
	st, err := daily.Open(*dir, now)
	if err != nil {
		log.Fatal(err)
	}
	var devs []daily.Device
	for _, m := range strings.Split(*macs, ",") {
		hw, err := net.ParseMAC(strings.TrimSpace(m))
		if err != nil || len(hw) != 6 {
			log.Fatalf("%q is not a MAC address", m)
		}
		devs = append(devs, daily.Device{MAC: [6]byte(hw)})
	}
	sort.Slice(devs, func(i, j int) bool { return string(devs[i].MAC[:]) < string(devs[j].MAC[:]) })
	var prim, back uint64
	for n, date := range daily.Window(now) {
		d := daily.Day{Date: date, Mask: 1<<24 - 1}
		if *primary != "" && *backup != "" {
			prim += uint64(9+n%3) << 30
			back += 3 << 20
			if n == 6 || n == 7 || n == 19 {
				back += uint64(1+n%5) << 30
			}
			d.CountersAt = date.Add(24*time.Hour + 2*time.Minute)
			d.Ifaces = []daily.Iface{{Name: *primary, Rx: prim * 9 / 10, Tx: prim / 10}, {Name: *backup, Rx: back * 9 / 10, Tx: back / 10}}
			sort.Slice(d.Ifaces, func(i, j int) bool { return d.Ifaces[i].Name < d.Ifaces[j].Name })
		}
		if n%9 == 4 {
			d.Mask &^= 0x3f // the router was off from midnight to six
		}
		for i := range devs {
			dev := daily.Device{MAC: devs[i].MAC}
			for h := 0; h < 24; h++ {
				mb := uint64(2 + (n*7+h*3+i*5)%5)
				evening := h >= 17 && h <= 21
				weekend := date.Weekday() == time.Saturday || date.Weekday() == time.Sunday
				switch {
				case evening:
					mb += uint64(150 + 40*i + (n*13)%60)
				case weekend && h >= 9 && h <= 16:
					mb += uint64(60 + (n*11+h)%40)
				case h < 6:
					mb = 0
				}
				dev.Down[h] = daily.Units(mb * 1 << 20 * 9 / 10)
				dev.Up[h] = daily.Units(mb * 1 << 20 / 10)
			}
			d.Devices = append(d.Devices, dev)
		}
		ok, err := st.Write(d)
		if err != nil {
			log.Fatal(err)
		}
		if ok {
			fmt.Println(daily.Name(date))
		}
	}
}
