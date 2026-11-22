import { test } from "node:test";
import assert from "node:assert/strict";
import { whereConnected, rulesOn, editBody } from "./client360";
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
