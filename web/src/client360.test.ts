import { test } from "node:test";
import assert from "node:assert/strict";
import { whereConnected, rulesOn, editBody, newForNote, exposureOf, usualSentence, usualRows, type Usual } from "./client360";
import type { Device, NetworkView } from "./types";
import type { Rule } from "./rules";

const dev = (link: Device["link"]): Pick<Device, "mac" | "link" | "group"> => ({ mac: "aa:bb:cc:dd:ee:01", link, group: "kids" });
const net: Pick<NetworkView, "ports" | "wifi"> = {
  ports: [{ name: "lan1", bridge: "br-lan", up: true, speedMbps: 1000, switch: true, devices: [] }],
  wifi: { why: "", note: "", radios: [], aps: [], live: [{ iface: "phy1-ap0", ssid: "Home", band: "5 GHz", channel: 36, clients: [] }] },
};

test("where a device is connected: its port and link speed, or its network, band, channel and signal", () => {
  assert.equal(whereConnected(dev({ kind: "wired", iface: "lan1", ssid: "", band: "", signal: 0 }), net), "Wired · port lan1 · 1 Gbit/s · on the hardware switch");
  assert.equal(whereConnected(dev({ kind: "wifi", iface: "phy1-ap0", ssid: "Home", band: "5 GHz", signal: -48 }), net), 'Wi-Fi "Home" · 5 GHz · channel 36 · -48 dBm, excellent');
  assert.equal(whereConnected(dev({ kind: "wired", iface: "lan9", ssid: "", band: "", signal: 0 }), net), "Wired · port lan9");
  assert.equal(whereConnected(dev({ kind: "", iface: "", ssid: "", band: "", signal: 0 }), undefined), "not seen on a port or an access point");
});

const rule = (p: Partial<Rule>): Rule => ({
  id: "r", enabled: true, what: "app", targets: ["tiktok"], who: "all", members: [], group: "", days: [], from: "", to: "",
  until: null, activeNow: true, hits: 0, applied: true, problem: "", error: "", ...p,
});

test("the rules on a device and where each comes from", () => {
  const rules = [
    rule({ id: "a", who: "device", members: ["aa:bb:cc:dd:ee:01"] }),
    rule({ id: "b", who: "group", group: "kids", members: ["aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"] }),
    rule({ id: "c", who: "all" }),
    rule({ id: "d", who: "device", members: ["aa:bb:cc:dd:ee:09"] }),
  ];
  const got = rulesOn(rules, "AA:BB:CC:DD:EE:01", [{ id: "kids", name: "Kids" }]);
  assert.deepEqual(got.map((x) => [x.rule.id, x.from]), [["a", "this device"], ["b", "group Kids"], ["c", "everyone"]]);
  assert.deepEqual(rulesOn(undefined, "aa:bb:cc:dd:ee:01", []), []);
});

test("a rename or a new group sends only what changed, a blank name is refused", () => {
  assert.deepEqual(editBody({ name: "TV", group: "" }, { name: "Living room TV", group: "" }), { name: "Living room TV" });
  assert.deepEqual(editBody({ name: "TV", group: "" }, { name: "TV", group: "kids" }), { group: "kids" });
  assert.deepEqual(editBody({ name: "TV", group: "kids" }, { name: " TV ", group: "kids" }), null, "nothing changed");
  assert.deepEqual(editBody({ name: "TV", group: "" }, { name: "   ", group: "" }), null, "a blank name is no rename");
});

test("what is new for a device says when it cannot tell, when nothing is new, and since when", () => {
  assert.equal(newForNote({ tooNew: true, knownDays: 3, since: "2026-11-22T00:00:00Z", apps: [], countries: [] }),
    "The router has known this device for 3 days: too new to tell what is new for it.");
  assert.equal(newForNote({ tooNew: true, knownDays: 0, since: "2026-11-22T00:00:00Z", apps: [], countries: [] }),
    "The router has known this device for less than a day: too new to tell what is new for it.");
  assert.equal(newForNote({ tooNew: true, knownDays: 1, since: "2026-11-22T00:00:00Z", apps: [], countries: [] }),
    "The router has known this device for 1 day: too new to tell what is new for it.");
  assert.equal(newForNote({ tooNew: false, knownDays: 20, since: "2026-11-22T00:00:00Z", apps: [], countries: [] }),
    "Nothing new: every app and country it used since yesterday it had used in the 29 days before.");
  assert.equal(newForNote({ tooNew: false, knownDays: 20, since: "2026-11-22T00:00:00Z", apps: [{ app: { id: "tiktok", name: "TikTok", category: "social" }, first: "x", down: 1, up: 0 }], countries: [] }),
    "Used since yesterday and in none of the 29 days before:");
});

test("what reaches a device from the internet: forwards, UPnP mappings and connections from outside", () => {
  const mac = "aa:bb:cc:dd:ee:01";
  const dev = { mac, name: "NAS", online: true };
  const net = { forwards: { why: "", rules: [
    { name: "NAS web", from: "wan", proto: "tcp", port: "8443", destIp: "192.168.1.5", destPort: "443", enabled: true, device: dev },
    { name: "old", from: "wan", proto: "tcp", port: "22", destIp: "192.168.1.5", destPort: "22", enabled: false, device: dev },
    { name: "game", from: "wan", proto: "udp", port: "3074", destIp: "192.168.1.9", destPort: "3074", enabled: true, device: { mac: "other", name: "Xbox", online: true } },
  ], upnp: [{ proto: "udp", port: "51413", destIp: "192.168.1.5", destPort: "51413", desc: "Transmission", device: dev }] } };
  const inbound = [{ cc: "US" }, { cc: "US" }, { cc: "DE" }, { cc: "LAN" }];
  const e = exposureOf(mac, net, inbound);
  assert.equal(e.quiet, false);
  assert.deepEqual(e.lines, [
    "Port 8443/tcp from the internet leads to it, port 443 (forward \"NAS web\")",
    "It opened port 51413/udp for itself through UPnP (\"Transmission\")",
    "3 connections from the internet in the last 24 hours, from US, DE",
  ]);
  const none = exposureOf(mac, { forwards: { why: "", rules: [], upnp: [] } }, [{ cc: "LAN" }]);
  assert.equal(none.quiet, true);
  assert.deepEqual(none.lines, ["Nothing from the internet reaches it: no forwarded port, no UPnP mapping, no connection from outside in the last 24 hours."]);
});

const week = (fill: (wd: number, h: number) => number | null): (number | null)[][] =>
  Array.from({ length: 7 }, (_, wd) => Array.from({ length: 24 }, (_, h) => fill(wd, h)));
const usual = (now: Partial<NonNullable<Usual["now"]>>, u: Partial<Usual> = {}): Usual => ({
  tooNew: false, knownDays: 30, zone: "Europe/London",
  median: week(() => 0), samples: week(() => 4) as number[][],
  now: { from: "2026-11-20T11:00:00Z", to: "2026-11-20T12:00:00Z", weekday: 5, hour: 11, bytes: 0, usual: 0, samples: 4, ...now },
  ...u,
});

test("the usual says both sides: what the last hour moved and what that hour of the week usually moves", () => {
  const MB = 1024 * 1024;
  assert.equal(usualSentence(usual({ bytes: 450 * MB, usual: 120 * MB })),
    "In the last hour, Friday 11:00 to 12:00, it moved 450 MB. On Fridays at that hour it usually moves 120 MB (the median of 4 Fridays). That is 3.8 times as much.");
  assert.equal(usualSentence(usual({ bytes: 30 * MB, usual: 120 * MB, samples: 5 })),
    "In the last hour, Friday 11:00 to 12:00, it moved 30 MB. On Fridays at that hour it usually moves 120 MB (the median of 5 Fridays). That is 25% of it.");
  assert.equal(usualSentence(usual({ bytes: 100 * MB, usual: 120 * MB })),
    "In the last hour, Friday 11:00 to 12:00, it moved 100 MB. On Fridays at that hour it usually moves 120 MB (the median of 4 Fridays). About as usual.");
  assert.equal(usualSentence(usual({ bytes: 5 * MB, usual: 0, weekday: 0, hour: 23 })),
    "In the last hour, Sunday 23:00 to 00:00, it moved 5 MB. On Sundays at that hour it usually moves nothing (the median of 4 Sundays).");
  assert.equal(usualSentence(usual({ bytes: 0, usual: 0 })),
    "In the last hour, Friday 11:00 to 12:00, it moved nothing. On Fridays at that hour it usually moves nothing (the median of 4 Fridays). As usual.");
});

test("the usual says when a side is missing, never reading it as zero", () => {
  assert.equal(usualSentence(usual({ bytes: null, usual: 2048 })),
    "On Fridays from 11:00 to 12:00 it usually moves 2 KB (the median of 4 Fridays); the router did not count the whole of the last hour, so there is nothing to set beside it.");
  assert.equal(usualSentence(usual({ bytes: 2048, usual: null, samples: 1 })),
    "In the last hour, Friday 11:00 to 12:00, it moved 2 KB. The router has counted only 1 Friday at that hour, too few to say what is usual.");
  assert.equal(usualSentence(usual({}, { tooNew: true, knownDays: 4, median: null, samples: null, now: null })),
    "The router has counted this device on 4 days: a usual week needs at least 7.");
  assert.equal(usualSentence(usual({}, { tooNew: true, knownDays: 1, median: null, samples: null, now: null })),
    "The router has counted this device on 1 day: a usual week needs at least 7.");
  assert.equal(usualSentence(usual({}, { tooNew: true, knownDays: 0, median: null, samples: null, now: null })),
    "The router has not counted this device on any day yet: a usual week needs at least 7.");
});

test("the week grid runs Monday to Sunday, shades by the busiest slot, keeps unknown slots apart and marks the hour that closed", () => {
  const u = usual({}, { median: week((wd, h) => (wd === 1 && h === 20 ? 1000 : wd === 2 && h === 3 ? null : wd === 5 && h === 11 ? 250 : 0)) });
  const rows = usualRows(u);
  assert.deepEqual(rows.map((r) => r.day), ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"]);
  assert.equal(rows[0].cells.length, 24);
  const mon20 = rows[0].cells[20];
  assert.equal(mon20.level, 5, "the busiest slot is the darkest step");
  assert.equal(rows[0].cells[0].level, 0, "a quiet slot is step 0");
  assert.equal(rows[1].cells[3].level, -1, "a slot without enough days is unknown, not quiet");
  assert.equal(rows[4].cells[11].level, 3, "a quarter of the busiest: sqrt(0.25) of 5, rounded up");
  assert.ok(rows[4].cells[11].now && !mon20.now, "only the slot of the hour that closed is marked");
  assert.equal(mon20.title, "Monday 20:00 to 21:00: usually 1000 B, the median of 4 days");
  assert.equal(rows[1].cells[3].title, "Tuesday 03:00 to 04:00: not enough days to say (4 counted)");
});
