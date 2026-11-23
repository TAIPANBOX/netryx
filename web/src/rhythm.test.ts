import { test } from "node:test";
import assert from "node:assert/strict";
import { usualSentence, usualRows, rhythmSentences, rhythmBars, type Usual, type Rhythm } from "./rhythm";

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

const MiB = 1024 * 1024;
const rdays = (f: (i: number) => { bytes: number | null; whole: boolean }) =>
  Array.from({ length: 30 }, (_, i) => ({ date: `2026-${i < 11 ? "10" : "11"}-${String(i < 11 ? 21 + i : i - 10).padStart(2, "0")}`, ...f(i) }));
const rhythm = (r: Partial<Rhythm>): Rhythm => {
  const days = r.days ?? rdays(() => ({ bytes: 100 * MiB, whole: true }));
  return {
    confidence: "normal", wholeDays: 30, medianDay: 100 * MiB, days, yesterday: days[days.length - 1],
    timesMedian: 1, trend: "steady", busiestShare: 0.04, awake: [], zone: "Europe/London", ...r,
  };
};

test("the rhythm sets yesterday against the device's own median day, both sides named", () => {
  assert.deepEqual(rhythmSentences(rhythm({ wholeDays: 28, yesterday: { date: "2026-11-19", bytes: 850 * MiB, whole: true }, timesMedian: 8.5, trend: "rising" })), [
    "Its median day over the last 28 whole days is 100 MB; yesterday, 19 Nov, it moved 850 MB, 8.5 times its median day.",
    "Over the last 7 days it moved more than in the 7 before.",
  ]);
  assert.deepEqual(rhythmSentences(rhythm({ medianDay: 0, yesterday: { date: "2026-11-19", bytes: 10 * MiB, whole: true }, timesMedian: null, trend: "falling" })), [
    "Its median day over the last 30 whole days is nothing; yesterday, 19 Nov, it moved 10 MB.",
    "Over the last 7 days it moved less than in the 7 before.",
  ]);
  assert.deepEqual(rhythmSentences(rhythm({ trend: "unknown", awake: [{ hour: 3, bytes: 6 * MiB }, { hour: 4, bytes: 2 * MiB }] })), [
    "Its median day over the last 30 whole days is 100 MB; yesterday, 19 Nov, it moved 100 MB, the same as its median day.",
    "Yesterday it moved 8 MB at 03:00 and 04:00, hours in which it usually moves nothing.",
  ]);
});

test("the rhythm refuses a comparison it cannot make, and says why", () => {
  assert.deepEqual(rhythmSentences(rhythm({ confidence: "no_data", medianDay: null, timesMedian: null })),
    ["The daily files hold no day of this device yet, so there is nothing to compare."]);
  assert.deepEqual(rhythmSentences(rhythm({ confidence: "too_new", wholeDays: 4, timesMedian: null, trend: "unknown" })),
    ["The router has counted 4 whole days of this device; yesterday, 19 Nov, it moved 100 MB. A usual day needs at least 7, so it is not compared."]);
  assert.deepEqual(rhythmSentences(rhythm({ confidence: "too_new", wholeDays: 1, timesMedian: null, trend: "unknown", yesterday: { date: "2026-11-19", bytes: null, whole: false } })),
    ["The router has counted 1 whole day of this device; yesterday, 19 Nov, has no file. A usual day needs at least 7, so it is not compared."]);
  assert.deepEqual(rhythmSentences(rhythm({ yesterday: { date: "2026-11-19", bytes: 40 * MiB, whole: false }, timesMedian: null, trend: "steady" })), [
    "Its median day over the last 30 whole days is 100 MB; yesterday, 19 Nov, the router was not counting all day (it moved 40 MB while it was), so it is not compared.",
    "Over the last 7 days it moved about as much as in the 7 before.",
  ]);
});

test("the rhythm's days are drawn oldest first, yesterday marked, a day with no file apart from a quiet one", () => {
  const bars = rhythmBars(rhythm({ days: rdays((i) => (i === 3 ? { bytes: null, whole: false } : i === 5 ? { bytes: 50 * MiB, whole: false } : i === 7 ? { bytes: 0, whole: true } : { bytes: (i === 29 ? 400 : 100) * MiB, whole: true })) }));
  assert.equal(bars.length, 30);
  assert.equal(bars[0].date, "2026-10-21");
  assert.equal(bars[29].height, 1, "the busiest day sets the scale");
  assert.equal(bars[0].height, 0.25);
  assert.deepEqual([bars[3].kind, bars[3].height], ["unknown", null]);
  assert.equal(bars[5].kind, "partial");
  assert.deepEqual([bars[7].kind, bars[7].height], ["whole", 0]);
  assert.ok(bars[29].yesterday && !bars[28].yesterday);
  assert.equal(bars[3].title, "24 Oct: nothing counted for this device");
  assert.equal(bars[5].title, "26 Oct: 50 MB, the router was not counting all day");
  assert.equal(bars[29].title, "19 Nov: 400 MB (yesterday)");
});
