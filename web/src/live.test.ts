import { test } from "node:test";
import assert from "node:assert/strict";
import type { Live, LivePoint } from "./types";
import { time } from "./format";
import { liveChart, sparkPath, hourSpark, liveSummary, whereText, placeName, layerLabel, connKeys, appsTileSub, cutNote, speedTile } from "./live";
import type { App } from "./types";

const END = Date.parse("2026-11-20T12:00:00Z");
const at = (secBeforeEnd: number) => new Date(END - secBeforeEnd * 1000).toISOString();
const pt = (secBeforeEnd: number, stepSec: number, down: Record<string, number>, up: Record<string, number> = {}): LivePoint => ({
  t: at(secBeforeEnd), stepSec, down, up,
});

test("a 2 s ring fills five minutes as 150 even columns, newest on the right", () => {
  const series: LivePoint[] = [];
  for (let i = 0; i < 150; i++) series.push(pt((149 - i) * 2, 2, { a: i }));
  const c = liveChart(series, 3);
  assert.equal(c.up.length, 150);
  assert.equal(c.down.a.length, 150);
  assert.equal(c.down.a[149], 149);
  assert.equal(c.down.a[0], 0);
  assert.equal(c.down.a[75], 75);
  assert.equal(c.end, END);
});

test("a point is drawn over its own stepSec at its rate: a 5 s point is not divided, nor taken as 2 s", () => {
  const c = liveChart([pt(0, 5, { a: 100 })], 3);
  assert.equal(c.down.a[149], 100);
  assert.equal(c.down.a[148], 100);
  assert.equal(c.down.a[147], 50); // half of that column lies inside the point's 5 s
  assert.equal(c.down.a[146], 0);
});

test("seconds with no point are zero, and points older than five minutes are left out", () => {
  const c = liveChart([pt(400, 2, { a: 999 }), pt(20, 2, { a: 10 }), pt(0, 2, { a: 30 })], 3);
  assert.equal(c.down.a.length, 150);
  assert.equal(c.down.a[149], 30);
  assert.equal(c.down.a[139], 10);
  assert.equal(c.down.a[145], 0);
  assert.equal(c.down.a.reduce((s, v) => s + v, 0), 40);
});

test("the three busiest apps get layers and the rest fold into one; upload is every app together", () => {
  const c = liveChart([
    pt(2, 2, { a: 400, b: 300, c: 200, d: 100 }, { a: 5, d: 7 }),
    pt(0, 2, { a: 400, b: 300, c: 200, d: 50 }, { b: 1 }),
  ], 3);
  assert.deepEqual(c.keys, ["a", "b", "c"]);
  assert.equal(c.other, true);
  assert.equal(c.otherDown[148], 100);
  assert.equal(c.otherDown[149], 50);
  assert.equal(c.up[148], 12);
  assert.equal(c.up[149], 1);
  const two = liveChart([pt(0, 2, { a: 1, b: 2 })], 3);
  assert.deepEqual(two.keys, ["b", "a"]);
  assert.equal(two.other, false);
});

test("no ring yet means no columns", () => {
  assert.equal(liveChart(null, 3).up.length, 0);
  assert.equal(liveChart([], 3).up.length, 0);
});

test("a sparkline spans 120 by 28, its highest point near the top and zero on the bottom line", () => {
  assert.equal(sparkPath([0, 1, 2]), "M0.0 26.0 L60.0 14.0 L120.0 2.0");
  assert.equal(sparkPath([0, 0, 0]), "M0.0 26.0 L60.0 26.0 L120.0 26.0");
  assert.equal(sparkPath([5]), "M0.0 2.0 L120.0 2.0");
  assert.equal(sparkPath([]), "");
  assert.equal(sparkPath(null), "");
});

const live: Live = {
  at: "2026-11-20T12:00:00Z", stepSec: 2, rate: { down: 4710, up: 1060 }, flows: 5,
  apps: [
    { app: { id: "netflix", name: "Netflix", category: "video" }, down: 4000, up: 150, flows: 2, domain: "video.example", src: "sni", cc: "DE", org: "Netflix", spark: [] },
    { app: { id: "smb", name: "Files", category: "lan" }, down: 500, up: 500, flows: 1, domain: "", src: "sni", cc: "LAN", org: "", spark: [] },
    { app: { id: "github", name: "GitHub", category: "dev" }, down: 200, up: 400, flows: 1, domain: "git.example", src: "sni", cc: "US", org: "GitHub", spark: [] },
    { app: { id: "unknown", name: "Unknown", category: "unknown" }, down: 10, up: 10, flows: 1, domain: "", src: "sni", cc: "--", org: "", spark: [] },
  ],
  conns: [
    { app: { id: "netflix", name: "Netflix", category: "video" }, name: "video.example", src: "sni", cc: "DE", org: "Netflix", asn: 1, proto: 6, port: 443, down: 3000, up: 100, bytesDown: 0, bytesUp: 0, durSec: 90, local: "192.168.1.5:50000", remote: "203.0.113.9:443" },
    { app: { id: "netflix", name: "Netflix", category: "video" }, name: "cdn.example", src: "quic", cc: "DE", org: "Netflix", asn: 1, proto: 17, port: 443, down: 1000, up: 50, bytesDown: 0, bytesUp: 0, durSec: 90, local: "192.168.1.5:50000", remote: "203.0.113.9:443" },
    { app: { id: "smb", name: "Files", category: "lan" }, name: "", src: "", cc: "LAN", org: "", asn: 0, proto: 6, port: 445, down: 500, up: 500, bytesDown: 0, bytesUp: 0, durSec: 30, local: "192.168.1.5:50001", remote: "192.168.1.9:445" },
  ],
  places: [
    { cc: "DE", org: "Netflix", down: 4000, up: 150, other: false },
    { cc: "LAN", org: "", down: 500, up: 500, other: false },
    { cc: "US", org: "GitHub", down: 200, up: 400, other: false },
    { cc: "--", org: "", down: 10, up: 10, other: true },
  ],
  series: [],
};

test("the live tiles: app count, the busiest app's share, real countries only, the main one, TCP and UDP", () => {
  const s = liveSummary(live);
  assert.equal(s.apps, 4);
  assert.equal(s.topName, "Netflix");
  assert.equal(Math.round(s.topShare * 1000), 719); // 4150 of 5770 bytes/s
  assert.equal(s.countries, 2); // DE and US: not the local network, not the unknown "--"
  assert.equal(s.mainCC, "DE");
  assert.equal(s.tcp, 2);
  assert.equal(s.udp, 1);
});

test("a device that only talks inside the house has no country and no busiest app share of nothing", () => {
  const s = liveSummary({ ...live, apps: [], conns: null, places: [{ cc: "LAN", org: "", down: 5, up: 5 }] });
  assert.equal(s.apps, 0);
  assert.equal(s.topName, "");
  assert.equal(s.topShare, 0);
  assert.equal(s.countries, 0);
  assert.equal(s.mainCC, "");
  assert.equal(s.tcp, 0);
});

test("where a live app goes: the local network and the unknown are named, never shown as countries", () => {
  assert.equal(whereText("LAN", ""), "Local network");
  assert.equal(whereText("--", ""), "unknown");
  assert.equal(whereText("", ""), "unknown");
  assert.equal(whereText("DE", "Netflix"), "DE · Netflix");
  assert.match(whereText("US", ""), /^US · /);
  assert.equal(placeName({ cc: "LAN" }), "Local network");
  assert.equal(placeName({ cc: "--", other: true }), "unknown");
  assert.equal(placeName({ cc: "DE", other: true }), "unknown");
});

test("the server's own 'other' key always folds into the rest, never a layer of its own", () => {
  const c = liveChart([pt(0, 2, { a: 400, b: 300, c: 200, d: 100, other: 50 })], 3);
  assert.deepEqual(c.keys, ["a", "b", "c"]);
  assert.equal(c.other, true);
  assert.equal(c.otherDown[149], 150);
  const big = liveChart([pt(0, 2, { a: 1, other: 999 })], 3);
  assert.deepEqual(big.keys, ["a"]);
  assert.equal(big.other, true);
  assert.equal(big.otherDown[149], 999);
});

test("a live layer is named by the app, unknown reads Unknown, a forgotten id falls back", () => {
  const seen = new Map<string, App>([
    ["youtube", { id: "youtube", name: "YouTube", category: "video" }],
    ["unknown", { id: "unknown", name: "Unknown · Fastly", category: "unknown" }],
    ["noname", { id: "noname", name: "", category: "travel" }],
  ]);
  assert.equal(layerLabel("youtube", seen), "YouTube");
  assert.equal(layerLabel("unknown", seen), "Unknown");
  assert.equal(layerLabel("unknown", new Map()), "Unknown");
  assert.equal(layerLabel("noname", seen), "Travel");
  assert.equal(layerLabel("gone", seen), "gone");
  // An app never seen by name in this view is named from the icon manifest.
  const manifest = { netflix: { kind: "fav", color: "", name: "Netflix" }, bare: { kind: "letter", color: "" } };
  assert.equal(layerLabel("netflix", seen, manifest), "Netflix");
  assert.equal(layerLabel("bare", seen, manifest), "bare");
  assert.equal(layerLabel("youtube", seen, manifest), "YouTube");
});

test("live connections keep the same key between polls, and two alike ones still differ", () => {
  const c = (name: string, port = 443, proto = 6, id = "youtube") => ({ app: { id, name: "", category: "" }, name, port, proto });
  assert.deepEqual(connKeys([c("a.example"), c("b.example"), c("a.example"), c("a.example", 443, 17)]), [
    "youtube|a.example|443|6", "youtube|b.example|443|6", "youtube|a.example|443|6#2", "youtube|a.example|443|17",
  ]);
  // The busiest connection moves down a row: its key moves with it.
  assert.deepEqual(connKeys([c("b.example"), c("a.example")]), ["youtube|b.example|443|6", "youtube|a.example|443|6"]);
});

test("a cut the router could not hold says so", () => {
  assert.equal(cutNote({ cut: true, held: true }), "Cut.");
  assert.equal(
    cutNote({ cut: true, held: false, note: "the router could not hold the cut: the connection may come back on its next packet" }),
    "Cut, but the router could not hold the cut: the connection may come back on its next packet.",
  );
  assert.equal(cutNote({ cut: true, held: false }), "Cut, but the connection may come back on its next packet.");
});

test("the apps tile names the busiest app only while something moves", () => {
  assert.equal(appsTileSub({ apps: 3, topName: "YouTube", topShare: 0.62 }), "YouTube busiest, 62%");
  assert.equal(appsTileSub({ apps: 3, topName: "unknown", topShare: 0 }), "all idle now");
  assert.equal(appsTileSub({ apps: 0, topName: "", topShare: 0 }), "none");
});

test("the Speed tile leads with the window's average and shows the last seconds beside it", () => {
  assert.deepEqual(speedTile({ down: 175000, up: 400 }, { down: 0, up: 0 }, "3 min"), {
    value: "↓ 1.4 · ↑ 0",
    sub: "Mbit/s, average over 3 min · right now ↓ 0 bit/s",
  });
  assert.deepEqual(speedTile(undefined, { down: 1000, up: 0 }, "3 min").sub, "kbit/s, the last few seconds (no average yet)");
});

test("a list's hour is drawn only from where the router began counting", () => {
  // Five steps over 120 px: a step is 30 px. The first two were never counted.
  const s = hourSpark({ stepSec: 120, known: 2, bytes: [0, 0, 0, 120, 240], since: "2026-11-22T21:40:00Z" }, 120, 28);
  assert.equal(s.d, "M60.0 26.0 L90.0 14.0 L120.0 2.0");
  assert.equal(s.peak, 2); // bytes per second: 240 B over 120 s
  // A short line after a restart says when counting began.
  assert.equal(s.label, `since ${time("2026-11-22T21:40:00Z")}, peak 16 bit/s`);
  assert.equal(hourSpark({ stepSec: 120, known: 0, bytes: [0, 120], since: "2026-11-22T21:40:00Z" }).label, "last hour, peak 8 bit/s");
  // Nothing counted yet: nothing drawn, and it says so.
  const none = hourSpark({ stepSec: 120, known: 5, bytes: [0, 0, 0, 0, 0] });
  assert.equal(none.d, "");
  assert.equal(none.label, "the router has not counted this hour yet");
  // A quiet hour is a flat line on the floor, not a missing one.
  assert.equal(hourSpark({ stepSec: 120, known: 0, bytes: [0, 0, 0] }, 120, 28).d, "M0.0 26.0 L60.0 26.0 L120.0 26.0");
  assert.equal(hourSpark(undefined).d, "");
});
