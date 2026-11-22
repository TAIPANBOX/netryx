import { test } from "node:test";
import assert from "node:assert/strict";
import { connMap } from "./connmap";
import { sankeyLayout } from "./geometry";
import type { FlowRow } from "./types";

const flow = (p: Partial<FlowRow>): FlowRow => ({
  end: "0001-01-01T00:00:00Z", start: "2026-11-22T20:00:00Z", mac: "aa:bb:cc:dd:ee:01", client: "Laptop", dir: "out",
  local: "", remote: "", proto: 6, name: "", nameSrc: "", app: { id: "unknown", name: "Unknown", category: "unknown" },
  cc: "", asn: 0, org: "", up: 0, down: 0, evicted: false, ...p,
});

const yt = { id: "youtube", name: "YouTube", category: "video" };
const gh = { id: "github", name: "GitHub", category: "dev" };

test("the map runs device, apps, domains, networks, countries, by what moved over the window", () => {
  const m = connMap("Laptop", [
    flow({ app: yt, name: "www.youtube.com", org: "GOOGLE", asn: 15169, cc: "US", winDown: 900, winUp: 100 }),
    flow({ app: yt, name: "i.ytimg.com", org: "GOOGLE", asn: 15169, cc: "US", winDown: 400, winUp: 0 }),
    flow({ app: gh, name: "github.com", org: "GITHUB", asn: 36459, cc: "US", winDown: 200, winUp: 50 }),
    flow({ app: gh, name: "quiet.example", org: "X", asn: 1, cc: "DE", winDown: 0, winUp: 0, down: 999 }),
  ]);
  const cols = (c: number) => m.nodes.filter((n) => n.col === c).map((n) => n.label);
  assert.deepEqual(cols(0), ["Laptop"]);
  assert.deepEqual(cols(1), ["YouTube", "GitHub"]);
  assert.deepEqual(cols(2), ["www.youtube.com", "i.ytimg.com", "github.com"]);
  assert.deepEqual(cols(3), ["GOOGLE", "GITHUB"]);
  assert.deepEqual(cols(4), ["United States"]);
  const value = (from: string, to: string) => m.links.find((l) => l.from === from && l.to === to)?.value;
  assert.equal(value("dev", "app:youtube"), 1400);
  assert.equal(value("domain:www.youtube.com", "net:15169"), 1000);
  assert.equal(value("net:15169", "cc:US"), 1400);
  assert.ok(!m.nodes.some((n) => n.label === "quiet.example"), "a flow that moved nothing in the window is not on the map");
});

test("every node leads somewhere: the app, the domain's flows, the network's country, the country", () => {
  const m = connMap("Laptop", [flow({ app: yt, name: "www.youtube.com", org: "GOOGLE", asn: 15169, cc: "US", winDown: 10 })]);
  assert.equal(m.href["app:youtube"], "#/app/youtube");
  assert.equal(m.href["domain:www.youtube.com"], "#/flows?q=www.youtube.com");
  assert.equal(m.href["net:15169"], "#/destination/US");
  assert.equal(m.href["cc:US"], "#/destination/US");
  assert.equal(m.href["dev"], undefined);
});

test("each column keeps its busiest few and folds the rest", () => {
  const rows = Array.from({ length: 9 }, (_, i) =>
    flow({ app: { id: `a${i}`, name: `App ${i}`, category: "other" }, name: `d${i}.example`, org: `ORG${i}`, asn: 100 + i, cc: "US", winDown: 1000 - i }),
  );
  const m = connMap("Laptop", rows, 4);
  assert.equal(m.nodes.filter((n) => n.col === 1).length, 5, "four apps and the rest");
  assert.ok(m.nodes.some((n) => n.id === "app:*" && n.label === "5 other apps"));
  const total = m.links.filter((l) => l.from === "dev").reduce((a, l) => a + l.value, 0);
  assert.equal(total, rows.reduce((a, r) => a + (r.winDown ?? 0), 0), "folding loses no byte");
  assert.equal(m.href["app:*"], undefined);
});

test("no flow moved over the window: an empty map", () => {
  assert.deepEqual(connMap("Laptop", [flow({ winDown: 0 })]).nodes, []);
});

test("five columns are laid out across the width, one scale for all", () => {
  const m = connMap("Laptop", [flow({ app: yt, name: "www.youtube.com", org: "GOOGLE", asn: 15169, cc: "US", winDown: 10 })]);
  const out = sankeyLayout(m.nodes, m.links, { height: 300, top: 30, bottom: 20, gap: 14 }, 5, 1000);
  const xs = [0, 1, 2, 3, 4].map((c) => out.nodes.find((n) => n.col === c)?.x);
  assert.deepEqual(xs, [20, 222.5, 425, 627.5, 830], "the last column leaves room for its labels on the right");
  const three = sankeyLayout([{ id: "a", col: 0, label: "a", color: "" }, { id: "b", col: 2, label: "b", color: "" }], [{ from: "a", to: "b", value: 1 }], { height: 300, top: 30, bottom: 20, gap: 14 });
  assert.deepEqual(three.nodes.map((n) => n.x), [20, 560], "three columns as before");
});

test("a name that carries under 3% of the window folds into the rest, so small flows never pile up", () => {
  const rows = [
    flow({ app: yt, name: "big.example", org: "BIG", asn: 1, cc: "US", winDown: 9700 }),
    flow({ app: gh, name: "tiny.example", org: "TINY", asn: 2, cc: "DE", winDown: 200 }),
    flow({ app: { id: "x", name: "X", category: "other" }, name: "small.example", org: "SMALL", asn: 3, cc: "FR", winDown: 100 }),
  ];
  const m = connMap("Laptop", rows);
  const labels = (c: number) => m.nodes.filter((n) => n.col === c).map((n) => n.label);
  assert.deepEqual(labels(1), ["YouTube", "2 other apps"]);
  assert.deepEqual(labels(2), ["big.example", "2 other domains"]);
  assert.deepEqual(labels(4), ["United States", "2 other countries"]);
});
