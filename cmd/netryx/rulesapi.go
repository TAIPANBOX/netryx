package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/netip"
	"netryx/internal/alerts"
	"os"
	"strings"
	"time"

	"netryx/internal/api"
	"netryx/internal/inventory"
	"netryx/internal/rules"
	"netryx/internal/uciconf"
)

// rulesAPI is the router side of the console's write routes. A change is
// the `uci -q batch` text the agent would write, on stdin and never in argv,
// and then the engine reloads, so one path turns a request into a rule.
type rulesAPI struct {
	eng    *rules.Engine
	conf   string // /etc/config/netryx
	labels string // <state>/labels.json
	run    rules.Runner
	inv    *inventorySource
	// agentPending is openwrt-mcp's record of the applies it has not had
	// confirmed (<its state>/pending.json); "" checks nothing.
	agentPending string
	// adblockConf and adblockInit are adblock-fast's config and init script
	// ("" means the usual paths); background runs its restart (nil: a goroutine).
	adblockConf, adblockInit string
	background               func(func())
	// alerts is the alert store, triesSeen whether the capture reads the
	// log group country tries are copied to, fsys the router's files (for
	// /etc/config/wireless and the switch ports in sysfs).
	alerts    *alerts.Store
	triesSeen bool
	fsys      fs.FS
}

func (r *rulesAPI) UsageOf(mac string) []rules.LimitUse { return r.eng.UsageOf(mac) }
func (r *rulesAPI) SafeDNS() (bool, string)             { return r.eng.SafeDNSState() }

func (r *rulesAPI) adblockPaths() (conf, init string) {
	conf, init = r.adblockConf, r.adblockInit
	if conf == "" {
		conf = "/etc/config/adblock-fast"
	}
	if init == "" {
		init = "/etc/init.d/adblock-fast"
	}
	return conf, init
}

// adblockSections reads adblock-fast's config; nil, ErrNoAdblock when it is
// not installed (no config or no init script).
func (r *rulesAPI) adblockSections() ([]uciconf.Section, error) {
	conf, init := r.adblockPaths()
	if _, err := os.Stat(init); err != nil {
		return nil, api.ErrNoAdblock
	}
	b, err := os.ReadFile(conf)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, api.ErrNoAdblock
	}
	if err != nil {
		return nil, err
	}
	return uciconf.Parse(bytes.NewReader(b))
}

// Adblock lists adblock-fast's sources: its anonymous file_url sections, in
// file order, named file_url_<n> by their place (docs/stage2b-measured.md, 4).
func (r *rulesAPI) Adblock() (api.AdblockState, error) {
	secs, err := r.adblockSections()
	if errors.Is(err, api.ErrNoAdblock) {
		return api.AdblockState{}, nil
	}
	if err != nil {
		return api.AdblockState{}, err
	}
	st := api.AdblockState{Installed: true}
	n := 0
	for _, s := range secs {
		if s.Type != "file_url" {
			continue
		}
		on := len(s.Options["enabled"]) > 0 && s.Options["enabled"][0] == "1"
		st.Sources = append(st.Sources, api.AdblockSource{ID: fmt.Sprintf("file_url_%d", n), Name: first(s, "name"), URL: first(s, "url"), Enabled: on})
		n++
	}
	return st, nil
}

func first(s uciconf.Section, k string) string {
	if v := s.Options[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// SetAdblock switches the source at place n, only if its url is still the one
// the page listed; enabling a source also turns adblock-fast on. Only enabled
// options change. adblock-fast's own restart (which restarts the router's DNS,
// about 20 s) runs in the background.
func (r *rulesAPI) SetAdblock(id, url string, enabled bool) error {
	secs, err := r.adblockSections()
	if err != nil {
		return err
	}
	var n int
	if _, err := fmt.Sscanf(id, "file_url_%d", &n); err != nil || id != fmt.Sprintf("file_url_%d", n) || n < 0 {
		return api.ErrNotSource
	}
	i, found := 0, false
	for _, s := range secs {
		if s.Type != "file_url" {
			continue
		}
		if i == n {
			if first(s, "url") != url {
				return api.ErrSourceMoved
			}
			found = true
			break
		}
		i++
	}
	if !found {
		return api.ErrNotSource
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := r.run(ctx, nil, "", "uci", "-q", "changes", "adblock-fast")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "" {
		return api.ErrPendingChanges
	}
	on := "0"
	if enabled {
		on = "1"
	}
	batch := fmt.Sprintf("set adblock-fast.@file_url[%d].enabled='%s'\n", n, on)
	if enabled {
		batch += "set adblock-fast.config.enabled='1'\n"
	}
	batch += "commit adblock-fast\n"
	if _, err := r.run(ctx, nil, batch, "uci", "-q", "batch"); err != nil {
		return err
	}
	_, init := r.adblockPaths()
	restart := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := r.run(ctx, nil, "", init, "restart"); err != nil {
			log.Printf("adblock-fast did not restart: %v", err)
		}
	}
	if r.background != nil {
		r.background(restart)
	} else {
		go restart()
	}
	return nil
}

func (r *rulesAPI) State() []rules.RuleState { return r.eng.State() }
func (r *rulesAPI) Known() rules.Known       { return r.eng.Known() }

// Sections reads the config as it is now; a config that does not exist yet
// is an empty one.
func (r *rulesAPI) Sections() ([]uciconf.Section, error) {
	b, err := os.ReadFile(r.conf)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return uciconf.Parse(bytes.NewReader(b))
}

// LabelName is the name labels.json holds for a MAC, "" when none or when
// the file cannot be read.
func (r *rulesAPI) LabelName(mac string) string {
	labels, err := inventory.LoadLabels(r.labels)
	if err != nil {
		log.Printf("device labels could not be read, the name is not carried over: %v", err)
		return ""
	}
	return labels[mac].Name
}

// Write commits the changes and reloads the engine. uci changes somebody
// else left uncommitted would be committed with ours, unvalidated, so they
// stop the write first, and so does an apply of the router agent that waits
// for its confirmation (R39). labels.json is never rewritten: a UCI name
// only shadows a label, so a rollback that erases the section leaves the
// owner's name where it was.
func (r *rulesAPI) Write(changes []uciconf.Change) error {
	text, err := uciconf.Batch(changes)
	if err != nil {
		return err
	}
	if agentApplyPending(r.agentPending) {
		log.Printf("the openwrt-mcp agent is waiting for a netryx change to be confirmed: the write is held")
		return api.ErrAgentPending
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pending, err := r.run(ctx, nil, "", "uci", "-q", "changes", "netryx")
	if err != nil {
		log.Printf("uci changes failed: %v", err)
		return err
	}
	if strings.TrimSpace(string(pending)) != "" {
		log.Printf("uci has unsaved netryx changes, the write is refused: %.200s", strings.TrimSpace(string(pending)))
		return api.ErrPendingChanges
	}
	if _, err := r.run(ctx, nil, text, "uci", "-q", "batch"); err != nil {
		log.Printf("uci batch failed: %v", err)
		return err
	}
	if r.inv != nil {
		r.inv.invalidate()
	}
	if err := r.eng.Reload(); err != nil {
		return fmt.Errorf("%w: %v", api.ErrNotApplied, err)
	}
	return nil
}

// Cut ends one connection through the engine's delete.
func (r *rulesAPI) Cut(mac string, local, remote netip.AddrPort, proto uint8, reply bool) (string, error) {
	return r.eng.Cut(mac, local, remote, proto, reply)
}

// agentApplyPending reads openwrt-mcp's pending.json: the applies it has
// made and not had confirmed, each with the configs its rollback restores
// from a snapshot. One that names netryx would put the file back as it was
// before it, erasing whatever is written now. A missing or empty file is
// nothing pending; a file that cannot be read, or holds what does not parse,
// cannot say nothing is pending, so it counts as pending (R43: fail closed).
func agentApplyPending(path string) bool {
	if path == "" {
		return false
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		log.Printf("agent state %s could not be read (%v): the write waits", path, err)
		return true
	}
	if strings.TrimSpace(string(b)) == "" {
		return false
	}
	var pending []struct {
		Configs []string `json:"configs"`
	}
	if err := json.Unmarshal(b, &pending); err != nil {
		log.Printf("agent state %s could not be parsed (%v): the write waits", path, err)
		return true
	}
	for _, p := range pending {
		for _, c := range p.Configs {
			if c == "netryx" {
				return true
			}
		}
	}
	return false
}

// deviceSections reads the name and group each `device` section gives a
// hardware address. A config that is missing or unreadable gives none.
func deviceSections(path string) (map[string]inventory.Label, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	secs, err := uciconf.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	out := map[string]inventory.Label{}
	for _, s := range secs {
		if s.Type != "device" || rules.AgentOwned(s.Name) || len(s.Options["mac"]) == 0 {
			continue
		}
		hw, err := net.ParseMAC(s.Options["mac"][0])
		if err != nil || len(hw) != 6 {
			continue
		}
		var l inventory.Label
		if v := s.Options["name"]; len(v) > 0 {
			l.Name = v[0]
		}
		if v := s.Options["group"]; len(v) > 0 {
			l.Group = v[0]
		}
		out[hw.String()] = l
	}
	return out, nil
}

// securityOf is each isolated or held device's standing for the console.
func securityOf(cfg rules.Config, pending []rules.Pending, held map[string]bool, links map[string]inventory.Link, w rules.WirelessConfig, sw func(string) bool) map[string]api.DeviceSecurity {
	out := map[string]api.DeviceSecurity{}
	onPort := map[string]int{}
	for _, l := range links {
		if l.Kind == "wired" && l.Iface != "" {
			onPort[l.Iface]++
		}
	}
	cover := func(mac string) *rules.Isolation {
		l := links[mac]
		iso := rules.IsolationOf(l, w, sw, onPort[l.Iface])
		return &iso
	}
	for _, d := range cfg.Devices {
		if d.Isolate {
			out[d.MAC] = api.DeviceSecurity{Isolate: true, Isolation: cover(d.MAC)}
		}
	}
	for _, p := range pending {
		if !held[p.MAC] {
			// Listed, but the kernel does not hold it (quarantine off,
			// no house interface): never shown as held.
			continue
		}
		ds := out[p.MAC]
		ds.Quarantined, ds.LikelySame = true, p.LikelySame
		if ds.Isolation == nil {
			ds.Isolation = cover(p.MAC)
		}
		out[p.MAC] = ds
	}
	return out
}

// Security is each isolated or held device's standing (api.Rules).
func (r *rulesAPI) Security() map[string]api.DeviceSecurity {
	var cfg rules.Config
	if secs, err := r.Sections(); err == nil {
		cfg = rules.FromSections(secs, r.Known(), time.Now())
	}
	links := map[string]inventory.Link{}
	if r.inv != nil {
		for _, d := range r.inv.Devices() {
			links[d.MAC] = d.Link
		}
	}
	fsys := r.fsys
	return securityOf(cfg, r.eng.Pending(), r.eng.HeldDevices(), links, readWireless(fsys), func(p string) bool { return onHardwareSwitch(fsys, p) })
}

// Pending is the devices in quarantine (api.Rules).
func (r *rulesAPI) Pending() []rules.Pending { return r.eng.Pending() }

// AlertsState is the alerts and how they reach the owner (api.Rules).
func (r *rulesAPI) AlertsState() api.AlertsState {
	st := api.AlertsState{CountryTriesSeen: r.triesSeen}
	if r.alerts != nil {
		st.Alerts = r.alerts.List()
	}
	st.TelegramReady, st.TelegramProblem = telegramState(r.eng.Alerts().Channel, func() (alerts.Bot, string) {
		return alerts.ReadBot(alerts.HermesConfig, os.Stat)
	})
	st.QuarantineActive, st.GraceEnds, st.QuarantineProblem = r.eng.QuarantineState()
	return st
}

// telegramState says whether alerts go out through Telegram and, when they do
// not, why. With the console chosen it still says whether Hermes's bot was
// found, so the owner knows what choosing Telegram would do.
func telegramState(channel string, readBot func() (alerts.Bot, string)) (bool, string) {
	_, why := readBot()
	if channel != "console" {
		return why == "", why
	}
	if why == "" {
		return false, "alerts stay in the console, as configured; Hermes's bot is ready if you choose Telegram"
	}
	return false, "alerts stay in the console, as configured; for Telegram: " + why
}
