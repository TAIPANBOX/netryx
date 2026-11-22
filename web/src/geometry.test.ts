import { test } from "node:test";
import assert from "node:assert/strict";
import type { SeriesPoint } from "./types";
import { areaPath, splitSeries, cumulate, sankeyLayout, timeTicks, pickColumn } from "./geometry";

test("an area runs from the axis through every point and back", () => {
  assert.equal(areaPath([0, 30, 60], 60, 170, 160, -1), "M0 170 L0.0 170.0 L500.0 90.0 L1000.0 10.0 L1000 170 Z");
  // Upload hangs below the axis.
  assert.equal(areaPath([5, 10], 10, 170, 80, 1), "M0 170 L0.0 210.0 L1000.0 250.0 L1000 170 Z");
  // One point is a flat band, not a division by zero.
  assert.equal(areaPath([10], 10, 100, 50, -1), "M0 100 L0.0 50.0 L1000.0 50.0 L1000 100 Z");
  // Nothing to scale against draws on the axis.
  assert.equal(areaPath([0, 0], 0, 100, 50, -1), "M0 100 L0.0 100.0 L1000.0 100.0 L1000 100 Z");
  assert.equal(areaPath([], 10, 100, 50, -1), "");
});

const series: SeriesPoint[] = [
  { t: "2026-11-20T10:00:00Z", down: { a: 600, b: 300, c: 60, d: 6 }, up: { a: 60, b: 6 } },
  { t: "2026-11-20T10:10:00Z", down: { a: 0, b: 1200, c: 0 }, up: { c: 600 } },
];

test("a split series keeps the top keys and folds the rest into one layer without losing a byte", () => {
  const s = splitSeries(series, 600, 2);
  assert.deepEqual(s.keys, ["b", "a"]); // b has the most download over the period
  assert.equal(s.other, true);
  // Bytes in a step become bytes per second.
  assert.deepEqual(s.down.b, [0.5, 2]);
  assert.deepEqual(s.down.a, [1, 0]);
  assert.deepEqual(s.otherDown, [0.11, 0]);
  assert.deepEqual(s.up, [0.11, 1]);
  // Every step's layers add up to the step's total.
  for (let i = 0; i < series.length; i++) {
    const total = Object.values(series[i].down).reduce((x, y) => x + y, 0) / 600;
    const layers = s.down.a[i] + s.down.b[i] + s.otherDown[i];
    assert.ok(Math.abs(layers - total) < 1e-9, `step ${i}: ${layers} != ${total}`);
  }
  assert.equal(s.totalDown, 2166);
  assert.equal(s.totalUp, 666);
  assert.deepEqual(s.peakDown, { rate: 2, at: "2026-11-20T10:10:00Z" });
});

test("a series with few keys has no other layer", () => {
  const s = splitSeries([{ t: "x", down: { total: 60 }, up: { total: 6 } }], 60, 4);
  assert.deepEqual(s.keys, ["total"]);
  assert.equal(s.other, false);
  assert.deepEqual(s.otherDown, [0]);
});

test("stacked layers accumulate from the bottom", () => {
  assert.deepEqual(cumulate([[1, 2], [3, 4], [5, 6]]), [[1, 2], [4, 6], [9, 12]]);
  assert.deepEqual(cumulate([]), []);
});

test("a Sankey conserves width at every node and keeps nodes apart and inside", () => {
  const nodes = [
    { id: "dev", col: 0, label: "TV", color: "#fff" },
    { id: "yt", col: 1, label: "YouTube", color: "#4C9AFF" },
    { id: "nf", col: 1, label: "Netflix", color: "#9BD0FF" },
    { id: "GB", col: 2, label: "GB", color: "#2F6FD6" },
    { id: "US", col: 2, label: "US", color: "#2F6FD6" },
  ];
  const links = [
    { from: "dev", to: "yt", value: 800 },
    { from: "dev", to: "nf", value: 200 },
    { from: "yt", to: "GB", value: 600 },
    { from: "yt", to: "US", value: 200 },
    { from: "nf", to: "GB", value: 200 },
  ];
  const box = { height: 300, top: 30, bottom: 20, gap: 14 };
  const out = sankeyLayout(nodes, links, box);
  const byId = Object.fromEntries(out.nodes.map((n) => [n.id, n]));
  // Width out of a node equals its height, and so does width in.
  const sum = (xs: number[]) => xs.reduce((a, b) => a + b, 0);
  for (const id of ["dev", "yt", "nf"]) {
    const w = sum(out.links.filter((l) => l.from === id).map((l) => l.w));
    assert.ok(Math.abs(w - byId[id].h) < 1e-6, `${id}: out ${w} vs height ${byId[id].h}`);
  }
  for (const id of ["yt", "nf", "GB", "US"]) {
    const w = sum(out.links.filter((l) => l.to === id).map((l) => l.w));
    assert.ok(Math.abs(w - byId[id].h) < 1e-6, `${id}: in ${w} vs height ${byId[id].h}`);
  }
  // The tallest column fills the box; nodes in a column never overlap.
  for (const col of [0, 1, 2]) {
    const ns = out.nodes.filter((n) => n.col === col).sort((a, b) => a.y - b.y);
    for (let i = 1; i < ns.length; i++) assert.ok(ns[i].y >= ns[i - 1].y + ns[i - 1].h + box.gap - 1e-6);
    const last = ns[ns.length - 1];
    assert.ok(last.y + last.h <= box.height - box.bottom + 1e-6, `column ${col} overflows`);
    assert.ok(ns[0].y >= box.top - 1e-6);
  }
  assert.ok(out.links.every((l) => l.d.startsWith("M") && l.d.includes(" C")));
});

test("a Sankey with nothing in it draws nothing", () => {
  const out = sankeyLayout([{ id: "dev", col: 0, label: "x", color: "#fff" }], [], { height: 300, top: 30, bottom: 20, gap: 14 });
  assert.deepEqual(out.links, []);
});

test("time ticks span the series and end at now", () => {
  const ticks = timeTicks(["2026-11-20T00:00:00Z", "2026-11-20T06:00:00Z", "2026-11-20T12:00:00Z", "2026-11-20T18:00:00Z", "2026-11-21T00:00:00Z"], "24h");
  assert.deepEqual(ticks, ["00:00", "06:00", "12:00", "18:00", "now"]);
  const week = timeTicks(["2026-11-13T00:00:00Z", "2026-11-20T00:00:00Z"], "7d");
  assert.deepEqual(week, ["13 Nov", "now"]);
  assert.deepEqual(timeTicks([], "1h"), []);
});

test("a catch-all key is folded into the one other layer, never a layer of its own", () => {
  const s = splitSeries([{ t: "x", down: { other: 900, a: 100, b: 50, c: 10 }, up: {} }], 1, 2, ["other"]);
  assert.deepEqual(s.keys, ["a", "b"]);
  assert.equal(s.other, true);
  assert.deepEqual(s.otherDown, [910]);
  const only = splitSeries([{ t: "x", down: { other: 5 }, up: {} }], 1, 4, ["other"]);
  assert.deepEqual(only.keys, []);
  assert.equal(only.other, true);
  assert.deepEqual(only.otherDown, [5]);
});

test("a point is divided by the seconds it covers, never by the range's usual step", () => {
  // An hour from before a restart, then two minutes after it, in the 1 h range.
  const mixed: SeriesPoint[] = [
    { t: "2026-11-20T10:00:00Z", stepSec: 3600, down: { total: 3600 * 5 }, up: { total: 3600 } },
    { t: "2026-11-20T11:00:00Z", stepSec: 60, down: { total: 120 }, up: { total: 60 } },
    { t: "2026-11-20T11:01:00Z", stepSec: 60, down: { total: 180 }, up: { total: 0 } },
  ];
  const s = splitSeries(mixed, 60, 1);
  // The hour is 5 B/s, drawn as wide as the sixty minutes it covers.
  assert.equal(s.down.total.length, 62);
  assert.ok(s.down.total.slice(0, 60).every((v) => v === 5), `hour: ${s.down.total.slice(0, 3)}`);
  assert.deepEqual(s.down.total.slice(60), [2, 3]);
  assert.ok(s.up.slice(0, 60).every((v) => v === 1));
  assert.equal(s.t.length, 62);
  assert.equal(s.t[1], "2026-11-20T10:01:00.000Z");
  // Totals are bytes, whatever the steps.
  assert.equal(s.totalDown, 3600 * 5 + 300);
  assert.deepEqual(s.peakDown, { rate: 5, at: "2026-11-20T10:00:00Z" });
});

test("a click on a chart picks the point drawn nearest to it", () => {
  // Points sit at i/(n-1) of the width, the first at the left edge and the last at the right.
  assert.equal(pickColumn(0, 5), 0);
  assert.equal(pickColumn(1, 5), 4);
  assert.equal(pickColumn(0.15, 5), 1); // x=0.15 is nearer the point at 0.25 than the one at 0
  assert.equal(pickColumn(0.6, 5), 2); // x=0.6 is nearer the point at 0.5 than the one at 0.75
  assert.equal(pickColumn(0.5, 1), 0);
  assert.equal(pickColumn(-0.1, 5), 0);
  assert.equal(pickColumn(1.2, 5), 4);
});
