// journal-load writes made-up interventions through the journal on a router,
// one at a time with a pause longer than the kernel's dirty expiry, so each
// reaches the disk on its own as a day's spread-out interventions would, and
// says what that cost: the bytes this process caused to be written
// (/proc/self/io write_bytes) and the sectors the disk took meanwhile. With
// -n 0 it only waits the same time and reads the disk, as a baseline. Lab
// only: it is never packaged, and it writes its own file, never the journal
// netryx keeps.
//
//	journal-load -path /etc/netryx/interventions-load.jsonl -n 50 -every 35s -disk sda
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"netryx/internal/journal"
)

func ioWriteBytes() int64 {
	f, err := os.Open("/proc/self/io")
	if err != nil {
		return -1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "write_bytes: "); ok {
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
	}
	return -1
}

func sectorsWritten(disk string) int64 {
	b, err := os.ReadFile("/sys/block/" + disk + "/stat")
	if err != nil {
		return -1
	}
	f := strings.Fields(string(b))
	if len(f) < 7 {
		return -1
	}
	n, _ := strconv.ParseInt(f[6], 10, 64)
	return n
}

func lifetimeKB(dev string) int64 {
	if dev == "" {
		return 0
	}
	b, err := os.ReadFile("/sys/fs/f2fs/" + dev + "/lifetime_write_kbytes")
	if err != nil {
		return -1
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

func main() {
	path := flag.String("path", "/etc/netryx/interventions-load.jsonl", "the file to write: never the journal netryx keeps")
	n := flag.Int("n", 50, "how many interventions; 0 only waits, as a baseline")
	every := flag.Duration("every", 35*time.Second, "the pause between two")
	disk := flag.String("disk", "sda", "the disk whose written sectors are read")
	f2fs := flag.String("f2fs", "", "an f2fs device name (loop0): also read its lifetime_write_kbytes")
	fsync := flag.Bool("fsync", false, "fsync the file after every append")
	batch := flag.Bool("batch", false, "keep the interventions in memory and append them in one write at the end, as an hourly buffer would")
	flag.Parse()
	if strings.HasSuffix(*path, "/interventions.jsonl") {
		log.Fatal("refusing to write the journal netryx keeps")
	}
	s0, w0, t0, k0 := sectorsWritten(*disk), ioWriteBytes(), time.Now(), lifetimeKB(*f2fs)
	var buf []journal.Entry
	var st journal.Stats
	if *n > 0 {
		j, err := journal.Open(*path, time.Now)
		if err != nil {
			log.Fatal(err)
		}
		for i := 0; i < *n; i++ {
			mac := fmt.Sprintf("02:4e:58:00:aa:%02x", i)
			var e journal.Entry
			switch i % 10 {
			case 0, 1, 2, 3:
				e = journal.Entry{Kind: journal.Blocked, MAC: mac, Rule: "agent_tiktok_kids", Name: "tiktok, instagram", By: "agent"}
			case 4, 5:
				e = journal.Entry{Kind: journal.Allow, ID: fmt.Sprintf("agent_more_%d", i), MAC: mac, Rule: "agent_games", Name: "roblox, minecraft", By: "agent", Until: time.Now().Add(30 * time.Minute)}
			case 6:
				e = journal.Entry{Kind: journal.LimitSpent, MAC: mac, Rule: "games_limit", Name: "roblox, minecraft", By: "router", Until: time.Now().Add(10 * time.Hour)}
			case 7:
				e = journal.Entry{Kind: journal.Approved, MAC: mac, By: "owner"}
			case 8:
				e = journal.Entry{Kind: journal.Isolated, MAC: mac, By: "owner"}
			default:
				e = journal.Entry{Kind: journal.Cut, MAC: mac, By: "owner", Detail: "tcp 149.154.167.50:443 (telegram)"}
			}
			if *batch {
				e.At = time.Now()
				buf = append(buf, e)
			} else {
				if _, err := j.Add(e); err != nil {
					log.Fatal(err)
				}
				if *fsync {
					if f, err := os.Open(*path); err == nil {
						f.Sync()
						f.Close()
					}
				}
			}
			time.Sleep(*every)
		}
		if *batch {
			// One write of every line, as a buffer flushed once would.
			var b []byte
			for _, e := range buf {
				line, _ := json.Marshal(e)
				b = append(append(b, line...), '\n')
			}
			f, err := os.OpenFile(*path, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				log.Fatal(err)
			}
			f.Write(b)
			f.Close()
			st.Appends, st.Bytes = 1, len(b)
		} else {
			st = j.Stats()
		}
	} else {
		time.Sleep(time.Duration(50) * *every)
	}
	// The last append is still dirty for the kernel's expiry: wait it out.
	time.Sleep(40 * time.Second)
	fi, _ := os.Stat(*path)
	size := int64(0)
	if fi != nil {
		size = fi.Size()
	}
	fmt.Printf("n %d every %v fsync %v batch %v over %v: process write_bytes %d, disk %s sectors %d (%d KiB), f2fs lifetime %d KiB, appended %d writes %d bytes, file %d bytes\n",
		*n, *every, *fsync, *batch, time.Since(t0).Round(time.Second), ioWriteBytes()-w0, *disk, sectorsWritten(*disk)-s0, (sectorsWritten(*disk)-s0)/2, lifetimeKB(*f2fs)-k0, st.Appends, st.Bytes, size)
}
