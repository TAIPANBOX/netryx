// fake-history writes made-up history for the lab, so the screens that read
// weeks of it (what is new for a device, the week's charts) can be seen
// before the lab has lived them. It builds the days before the oldest real
// day file in a store of its own, saves them to a work directory, and copies
// into the history only the files the history does not have, never over one,
// printing each so they can be taken away again. Lab only: never packaged.
//
//	fake-history -dir /etc/netryx/history -work /tmp/fakehist \
//	  -profile '02:4e:58:00:00:01=youtube:US,google:US;02:4e:58:00:00:02=github:US'
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"netryx/internal/flows"
	"netryx/internal/rollup"
)

type use struct{ app, cc string }

func main() {
	dir := flag.String("dir", "/etc/netryx/history", "the history to add to")
	work := flag.String("work", "/tmp/fakehist", "a work directory of its own")
	profile := flag.String("profile", "", "mac=app:cc,app:cc;mac=...")
	days := flag.Int("days", 30, "how far back from today")
	flag.Parse()
	devs := map[string][]use{}
	for _, part := range strings.Split(*profile, ";") {
		mac, list, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		for _, au := range strings.Split(list, ",") {
			app, cc, _ := strings.Cut(au, ":")
			devs[mac] = append(devs[mac], use{app, cc})
		}
	}
	if len(devs) == 0 {
		log.Fatal("no profile")
	}
	// The fake days end before the oldest real day file.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	end := today
	if names, err := os.ReadDir(filepath.Join(*dir, "days")); err == nil {
		for _, n := range names {
			if t, err := time.Parse("20060102.gob", n.Name()); err == nil && t.Before(end) {
				end = t
			}
		}
	}
	start := today.AddDate(0, 0, -*days)
	if !start.Before(end) {
		log.Fatalf("no day to make: the history already reaches %v", end)
	}
	var now time.Time
	s := rollup.NewStore(rollup.Options{Now: func() time.Time { return now }})
	macs := make([]string, 0, len(devs))
	for m := range devs {
		macs = append(macs, m)
	}
	sort.Strings(macs)
	for d := start; d.Before(end); d = d.Add(24 * time.Hour) {
		weekend := d.Weekday() == time.Saturday || d.Weekday() == time.Sunday
		for h := 0; h < 24; h++ {
			now = d.Add(time.Duration(h)*time.Hour + 10*time.Minute)
			for i, mac := range macs {
				for j, u := range devs[mac] {
					mb := uint64(1 + (h*7+i*3+j*5+d.Day())%4)
					switch {
					case h >= 18 && h <= 22:
						mb += uint64(40 + 20*j + (d.Day()*13)%50)
					case weekend && h >= 10 && h <= 16:
						mb += uint64(20 + (d.Day()+h)%30)
					case h < 6:
						mb = 0
					}
					if mb == 0 {
						continue
					}
					b := mb << 20
					s.Add(flows.Delta{At: now, MAC: mac, App: u.app, CC: u.cc, Down: b * 9 / 10, Up: b / 10, First: true})
				}
			}
		}
	}
	now = end.Add(2 * time.Minute)
	os.RemoveAll(*work)
	if _, err := rollup.SaveClosed(s, *work); err != nil {
		log.Fatal(err)
	}
	for _, kind := range []string{"days", "hours"} {
		names, _ := os.ReadDir(filepath.Join(*work, kind))
		for _, n := range names {
			dst := filepath.Join(*dir, kind, n.Name())
			if _, err := os.Lstat(dst); err == nil {
				continue // never over a real file
			}
			if err := copyFile(filepath.Join(*work, kind, n.Name()), dst); err != nil {
				log.Fatal(err)
			}
			fmt.Println(dst)
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
