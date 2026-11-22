import { test } from "node:test";
import assert from "node:assert/strict";
import { compareHash, compareRows, pickCompare } from "./compare";
import { parseRoute } from "./query";
import { bytes } from "./format";
import type { Device } from "./types";

const dev = (o: Partial<Device>): Device => ({
  mac: "aa:bb:cc:dd:ee:01", name: "", hostname: "", vendor: "", label: "", group: "", random: false, ipv4: [], ipv6: [],
  link: { kind: "wifi", iface: "wl0", ssid: "home", band: "5g", signal: -50 } as Device["link"], first: "", last: "", online: true,
  rate: { up: 0, down: 0 }, last24h: { down: 0, up: 0 }, topApp: null, countryCount: 0, ...o,
}) as Device;

test("two or three clients are compared side by side, from a shareable address", () => {
  assert.equal(compareHash(["aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"]), "#/compare?mac=aa%3Abb%3Acc%3Add%3Aee%3A01&mac=aa%3Abb%3Acc%3Add%3Aee%3A02");
  assert.deepEqual(parseRoute(compareHash(["aa:bb:cc:dd:ee:01", "AA:BB:CC:DD:EE:02"])), { name: "compare", macs: ["aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"] });
  // At most three, never twice the same, never a word that is not a MAC.
  assert.deepEqual(parseRoute("#/compare?mac=aa:bb:cc:dd:ee:01&mac=aa:bb:cc:dd:ee:01&mac=x&mac=aa:bb:cc:dd:ee:02&mac=aa:bb:cc:dd:ee:03&mac=aa:bb:cc:dd:ee:04"), {
    name: "compare", macs: ["aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:03"],
  });
});

test("picking clients to compare stops at three and a second pick takes one back", () => {
  let s: string[] = [];
  s = pickCompare(s, "a");
  s = pickCompare(s, "b");
  s = pickCompare(s, "c");
  assert.deepEqual(pickCompare(s, "d"), ["a", "b", "c"]); // a fourth is refused
  assert.deepEqual(pickCompare(s, "b"), ["a", "c"]);
});

test("the comparison lines up what tells devices apart, each name a link", () => {
  const a = dev({ mac: "aa:bb:cc:dd:ee:01", label: "Sam's phone", group: "kids", last24h: { down: 2048, up: 10 }, recent: { down: 125, up: 0 }, rate: { down: 9000, up: 0 }, topApp: { id: "instagram", name: "Instagram", category: "social" }, countryCount: 3 });
  const b = dev({ mac: "aa:bb:cc:dd:ee:02", label: "Work laptop", online: false, link: { kind: "wired", iface: "lan1", ssid: "", band: "", signal: 0 } as Device["link"] });
  const rows = compareRows([a, b], (g) => (g === "kids" ? "Kids" : g), "3 min", { "aa:bb:cc:dd:ee:01": ["US", "NL", "--"] });
  const row = (l: string) => rows.find((r) => r.label === l)?.cells;
  const texts = (l: string) => row(l)?.map((c) => c.list ? c.list.map((x) => x.text).join(",") : c.text);
  assert.deepEqual(texts("State"), ["online", "offline"]);
  assert.deepEqual(texts("24 h ↓"), [bytes(2048), bytes(0)]);
  // Now is the window's average, as on Clients; right now is said beside it.
  assert.deepEqual(texts("Now ↓, 3 min avg"), ["1 kbit/s", "0 bit/s"]);
  assert.equal(row("Now ↓, 3 min avg")?.[0].note, "right now 72 kbit/s");
  assert.deepEqual(texts("Busiest app"), ["Instagram", "-"]);
  assert.deepEqual(row("Busiest app")?.[0].e, { kind: "app", id: "instagram" });
  assert.equal(row("Busiest app")?.[1].e, undefined);
  assert.deepEqual(texts("Group"), ["Kids", "no group"]);
  assert.deepEqual(row("Group")?.[0].e, { kind: "group", id: "kids" });
  // Countries are the places themselves, each a link; without the list, the count.
  assert.deepEqual(texts("Countries"), ["US,NL", "0"]);
  assert.deepEqual(row("Countries")?.[0].list?.[0].e, { kind: "country", cc: "US" });
  assert.deepEqual(texts("Connected"), ["Wi-Fi home", "wired lan1"]);
  assert.equal(rows[0].label, "State");
});
