import { test } from "node:test";
import assert from "node:assert/strict";
import { linesSentences, lineBars, type Lines } from "./lines";

const MiB = 1024 * 1024;
const GiB = 1024 * MiB;
const ldays = (f: (i: number) => number | null) =>
  Array.from({ length: 30 }, (_, i) => ({ date: `2026-${i < 11 ? "10" : "11"}-${String(i < 11 ? 21 + i : i - 10).padStart(2, "0")}`, primary: f(i) === null ? null : 10 * GiB, backup: f(i) }));
const lines = (l: Partial<Lines>): Lines => ({
  primary: [{ name: "wan", proto: "dhcp", device: "eth1", up: true }],
  backup: [{ name: "lte", proto: "qmi", device: "wwan0", up: false }],
  days: ldays((i) => (i === 0 ? null : 3 * MiB)), usedDays: 0, usedMin: 32 * MiB, backupBytes: 87 * MiB,
  share: null, busiest: null, unknownDays: 1, ...l,
});

test("the backup line says on how many days it carried traffic, how much, and its share of the lines on those days", () => {
  assert.deepEqual(linesSentences(lines({ usedDays: 3, backupBytes: 4.5 * GiB, share: 0.12, busiest: { date: "2026-11-18", primary: 10 * GiB, backup: 3 * GiB } })), [
    "On 3 of the last 29 days known the backup line lte, a mobile modem, carried traffic: 4.5 GB, 12% of what the lines carried on those days. The most on 18 Nov: 3 GB.",
    "1 day is not known: no close before it, the router off for part of it, or a counter restarted.",
  ]);
  assert.deepEqual(linesSentences(lines({ unknownDays: 1 })), [
    "The backup line lte, a mobile modem, carried no traffic in the last 29 days known (under 32 MB a day, which its checks move).",
    "1 day is not known: no close before it, the router off for part of it, or a counter restarted.",
  ]);
  assert.deepEqual(linesSentences(lines({ backup: [{ name: "lte", proto: "qmi", device: "wwan0", up: true }, { name: "wwan", proto: "dhcp", device: "wlan0", up: true }], usedDays: 1, backupBytes: GiB, share: null, busiest: { date: "2026-11-02", primary: null, backup: GiB }, unknownDays: 4 }))[0],
    "On 1 of the last 26 days known the backup lines lte, a mobile modem, and wwan, a second uplink, carried traffic: 1 GB; what the primary line carried on that day is not known. The most on 2 Nov: 1 GB.");
});

test("without a backup line it says so, and names the line there is", () => {
  assert.deepEqual(linesSentences(lines({ backup: [] })), ["No backup line: the only internet line is wan."]);
  assert.deepEqual(linesSentences(lines({ backup: [], primary: [] })), ["No backup line, and the configured internet line was not found."]);
});

test("the backup line's days are drawn oldest first, a used day apart from a day of checks, an unknown day as no bar", () => {
  const bars = lineBars(lines({ days: ldays((i) => (i === 0 ? null : i === 9 ? 2 * GiB : i === 10 ? GiB : 3 * MiB)) }));
  assert.equal(bars.length, 30);
  assert.deepEqual([bars[0].height, bars[0].title], [null, "21 Oct: not known"]);
  assert.equal(bars[9].height, 1);
  assert.equal(bars[10].height, 0.5);
  assert.ok(bars[9].used && !bars[1].used);
  assert.equal(bars[9].title, "30 Oct: 2 GB on the backup, 10 GB on the primary");
  assert.equal(bars[1].title, "22 Oct: 3 MB on the backup, 10 GB on the primary");
});
