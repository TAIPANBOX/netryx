import { test } from "node:test";
import assert from "node:assert/strict";
import { whereConnected, rulesOn, editBody, newForNote, exposureOf } from "./client360";
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
