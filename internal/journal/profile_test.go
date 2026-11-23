//go:build heavyprofile

package journal

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// appendCost is what one append costs the flash when it reaches the disk on
// its own (interventions spread out, as a day's are): measured on f2fs with
// OpenWrt's mount options, 200 KiB for 21 appends, about 10 KiB each
// (docs/journal-measured.md).
const appendCost = 10 << 10

// The journal's cost to flash under a busy house: 50 interventions a day for
// 30 days (blocks, allowances, spent limits, approvals, isolations and cuts),
// each append counted at appendCost (the worst case, when no two appends
// share a writeback) and each rewrite as its whole size plus one append.
// NETRYX_JOURNAL_BUDGET_KB is the budget a day.
func TestJournalProfile(t *testing.T) {
	budget := 640
	if v := os.Getenv("NETRYX_JOURNAL_BUDGET_KB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatal(err)
		}
		budget = n
	}
	path := filepath.Join(t.TempDir(), "interventions.jsonl")
	c := &clock{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	s, err := Open(path, c.Now)
	if err != nil {
		t.Fatal(err)
	}
	const days, perDay = 30, 50
	step := 24 * time.Hour / perDay
	written := 0
	for d := 0; d < days; d++ {
		for i := 0; i < perDay; i++ {
			c.t = c.t.Add(step)
			mac := fmt.Sprintf("02:4e:58:00:%02x:%02x", d%256, i)
			var e Entry
			switch i % 10 {
			case 0, 1, 2, 3:
				e = Entry{Kind: Blocked, MAC: mac, Rule: "agent_tiktok_kids", Name: "tiktok, instagram", By: "agent"}
			case 4, 5:
				e = Entry{Kind: Allow, ID: fmt.Sprintf("agent_more_%d_%d", d, i), MAC: mac, Rule: "agent_games", Name: "roblox, minecraft", By: "agent", Until: c.t.Add(30 * time.Minute)}
			case 6:
				e = Entry{Kind: LimitSpent, MAC: mac, Rule: "games_limit", Name: "roblox, minecraft", By: "router", Until: c.t.Add(10 * time.Hour)}
			case 7:
				e = Entry{Kind: Approved, MAC: mac, By: "owner"}
			case 8:
				e = Entry{Kind: Isolated, MAC: mac, By: "owner"}
			default:
				e = Entry{Kind: Cut, MAC: mac, By: "owner", Detail: "tcp 149.154.167.50:443 (telegram)"}
			}
			ok, err := s.Add(e)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				written++
			}
		}
	}
	if written != days*perDay {
		t.Fatalf("%d interventions written, want %d", written, days*perDay)
	}
	st := s.Stats()
	flash := (st.Appends+1)*appendCost + st.RewriteBytes + st.Rewrites*appendCost
	perDayKB := float64(flash) / days / 1024
	fi, _ := os.Stat(path)
	t.Logf("journal written a day: %.1f KB (budget %d KB); appended %d lines, %d bytes; rewrites %d, %d bytes; file %d bytes",
		perDayKB, budget, st.Appends, st.Bytes, st.Rewrites, st.RewriteBytes, fi.Size())
	if perDayKB > float64(budget) {
		t.Errorf("the journal is over its budget: %.1f KB a day, budget %d KB", perDayKB, budget)
	}
}
