import { test } from "node:test";
import assert from "node:assert/strict";
import type { AppUsage, FlowRow, Wan } from "./types";
import { countryList, isRealCountry, lineQuality, sankeyOf, flowKey, selectFlow } from "./view";

test("a client's countries leave out the unknown country", () => {
  assert.equal(countryList(["US", "--", "IE"]), "US, IE");
  assert.equal(countryList(["--"]), "");
  assert.equal(countryList(null), "");
  assert.equal(countryList(["", "GB"]), "GB");
});

const wan = (o: Partial<Wan>): Wan => ({
  up: true, device: "eth1", ipv4: "", ipv6: "", uptimeSec: 0, rxRate: 0, txRate: 0,
  latencyMs: 9, jitterMs: 1.2, lossPct: 0, ...o,
});

test("an unmeasured line says so neutrally and shows no loss figure", () => {
  const q = lineQuality(wan({ latencyMs: null, jitterMs: null, lossPct: 0 }));
  assert.equal(q.value, "no data");
  assert.equal(q.bad, false);
  assert.ok(!q.sub.includes("0%"), `sub shows a loss figure: ${q.sub}`);
  assert.ok(!q.sub.includes("loss"), `sub shows a loss figure: ${q.sub}`);
});

test("a measured line shows latency, loss and jitter, coloured only when it loses packets", () => {
  assert.deepEqual(lineQuality(wan({})), { value: "9 ms", sub: "loss 0% · jitter 1.2 ms", bad: false });
  assert.deepEqual(lineQuality(wan({ lossPct: 2.5 })), { value: "9 ms", sub: "loss 2.5% · jitter 1.2 ms", bad: true });
  assert.deepEqual(lineQuality(wan({ jitterMs: null })), { value: "9 ms", sub: "loss 0%", bad: false });
});

const usage = (id: string, bytes: number, places: { cc: string; b: number; other?: boolean }[], other = false): AppUsage => ({
  app: { id, name: id, category: "" }, down: bytes, up: 0, flows: 1, share: 0, other,
  domains: [], places: places.map((p) => ({ cc: p.cc, org: "", down: p.b, up: 0, other: p.other })),
});

test("the Sankey folds service places into one 'others' country, never a country of their own", () => {
  const s = sankeyOf("TV", [
    usage("yt", 1000, [{ cc: "GB", b: 600 }, { cc: "--", b: 300 }, { cc: "other", b: 100, other: true }]),
    usage("nf", 500, [{ cc: "US", b: 500 }]),
  ]);
  const countries = s.nodes.filter((n) => n.col === 2).map((n) => n.id);
  assert.deepEqual(countries.sort(), ["cc:*", "cc:GB", "cc:US"]);
  const toOther = s.links.filter((l) => l.to === "cc:*").reduce((a, l) => a + l.value, 0);
  assert.equal(toOther, 400);
  const other = s.nodes.find((n) => n.id === "cc:*")!;
  assert.match(other.label, /^others /);
  // Shares are of all place bytes, so the named ones are not inflated.
  assert.equal(s.nodes.find((n) => n.id === "cc:GB")!.label, "GB 40%");
});

test("the Sankey puts a catch-all app in the 'others' band and keeps five countries at most", () => {
  const s = sankeyOf("TV", [
    usage("a", 100, [{ cc: "AA", b: 100 }]), usage("b", 90, [{ cc: "BB", b: 90 }]), usage("c", 80, [{ cc: "CC", b: 80 }]),
    usage("other", 500, [{ cc: "DD", b: 500 }], true), usage("e", 10, [{ cc: "EE", b: 10 }, { cc: "FF", b: 5 }]),
  ]);
  const apps = s.nodes.filter((n) => n.col === 1).map((n) => n.id);
  assert.deepEqual(apps, ["app:a", "app:b", "app:c", "app:*"]);
  assert.ok(s.nodes.filter((n) => n.col === 2).length <= 5);
  assert.deepEqual(sankeyOf("TV", []), { nodes: [], links: [] });
});

const flow = (o: Partial<FlowRow>): FlowRow => ({
  end: "0001-01-01T00:00:00Z", start: "2026-11-20T10:00:00Z", mac: "aa:bb:cc:dd:ee:01", client: "TV", dir: "out",
  local: "192.168.1.10:50000", remote: "203.0.113.5:443", proto: 6, name: "x", nameSrc: "sni",
  app: { id: "a", name: "A", category: "" }, cc: "GB", asn: 1, org: "", up: 1, down: 2, evicted: false, ...o,
});

test("a flow's key survives a refresh that moves it and changes its byte counts", () => {
  const a = flow({});
  assert.equal(flowKey(a), flowKey({ ...a, down: 999, up: 5, end: "2026-11-20T10:05:00Z" }));
  assert.notEqual(flowKey(a), flowKey({ ...a, local: "192.168.1.10:50001" }));
  assert.notEqual(flowKey(a), flowKey({ ...a, start: "2026-11-20T10:00:01Z" }));
  assert.notEqual(flowKey(a), flowKey({ ...a, proto: 17 }));
});

test("the selected flow is found by its key, and a vanished one stays shown as ended", () => {
  const a = flow({}), b = flow({ local: "192.168.1.11:1" }), c = flow({ local: "192.168.1.12:2" });
  // The list reorders on refresh: the selection follows the flow, not the index.
  assert.deepEqual(selectFlow([b, a, c], flowKey(a), null), { row: a, ended: false });
  const moved = { ...a, down: 50 };
  assert.deepEqual(selectFlow([moved, b], flowKey(a), a), { row: moved, ended: false });
  assert.deepEqual(selectFlow([b, c], flowKey(a), a), { row: a, ended: true });
  // Nothing chosen yet: the first row.
  assert.deepEqual(selectFlow([b, c], "", null), { row: b, ended: false });
  assert.deepEqual(selectFlow([], "", null), { row: null, ended: false });
});

test("the house's own traffic is not a country, and the Sankey names it", () => {
  assert.equal(countryList(["US", "LAN"]), "US");
  const s = sankeyOf("NAS", [
    { app: { id: "smb", name: "SMB", category: "" }, down: 900, up: 100, flows: 1, share: 1, domains: [], places: [{ cc: "LAN", org: "", down: 900, up: 100 }] },
  ]);
  const node = s.nodes.find((n) => n.id === "cc:LAN");
  assert.ok(node && node.label.startsWith("Local network"), JSON.stringify(s.nodes));
});

test("a real country is a place: not the unknown, not the house, not a catch-all row", () => {
  const rows = [{ cc: "US" }, { cc: "LAN" }, { cc: "--" }, { cc: "GB", other: true }, { cc: "" }];
  assert.deepEqual(rows.filter(isRealCountry).map((r) => r.cc), ["US"]);
});

test("the Sankey's app nodes carry their app id for the icon; the folded 'others' gets the other tile", () => {
  const s = sankeyOf("TV", [
    usage("yt", 400, [{ cc: "GB", b: 400 }]), usage("nf", 300, [{ cc: "US", b: 300 }]),
    usage("sp", 200, [{ cc: "SE", b: 200 }]), usage("tg", 100, [{ cc: "NL", b: 100 }]),
  ]);
  const apps = s.nodes.filter((n) => n.col === 1);
  assert.deepEqual(apps.map((n) => n.icon), ["yt", "nf", "sp", "other"]);
  assert.equal(apps[0].name, "yt");
});
