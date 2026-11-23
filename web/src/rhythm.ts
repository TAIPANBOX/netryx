// A device's usual week and rhythm, from the daily files: the helpers behind
// two cards of the client's full view. Kept apart from client360.ts, which the
// first screen imports, so they are loaded only with that view.

import { bytes } from "./format";

export interface Usual {
  tooNew: boolean; knownDays: number; zone: string;
  median: (number | null)[][] | null; samples: number[][] | null;
  now: { from: string; to: string; weekday: number; hour: number; bytes: number | null; usual: number | null; samples: number } | null;
}

const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const two = (h: number) => String(h % 24).padStart(2, "0");
const hourSpan = (h: number) => `${two(h)}:00 to ${two(h + 1)}:00`;
const moved = (n: number) => (n > 0 ? bytes(n) : "nothing");
const days = (n: number, wd: number) => `${n} ${WEEKDAYS[wd]}${n === 1 ? "" : "s"}`;

/** The line under a device's usual week: the hour that just closed beside the
 *  usual of its slot, each side said, a missing side said as missing. */
export function usualSentence(u: Usual): string {
  if (u.tooNew || !u.now) {
    const on = u.knownDays < 1 ? "has not counted this device on any day yet" : `has counted this device on ${u.knownDays} day${u.knownDays === 1 ? "" : "s"}`;
    return `The router ${on}: a usual week needs at least 7.`;
  }
  const n = u.now;
  const day = WEEKDAYS[n.weekday];
  const usual = n.usual === null ? null : `it usually moves ${moved(n.usual)} (the median of ${days(n.samples, n.weekday)})`;
  if (n.bytes === null) {
    if (usual === null) return `The router did not count the whole of the last hour, and has counted only ${days(n.samples, n.weekday)} at that hour, too few to say what is usual.`;
    return `On ${day}s from ${hourSpan(n.hour)} ${usual}; the router did not count the whole of the last hour, so there is nothing to set beside it.`;
  }
  const last = `In the last hour, ${day} ${hourSpan(n.hour)}, it moved ${moved(n.bytes)}.`;
  if (n.usual === null) return `${last} The router has counted only ${days(n.samples, n.weekday)} at that hour, too few to say what is usual.`;
  let verdict = "";
  if (n.usual === 0) {
    verdict = n.bytes === 0 ? " As usual." : "";
  } else {
    const r = n.bytes / n.usual;
    verdict = r >= 1.5 ? ` That is ${r.toFixed(r < 10 ? 1 : 0)} times as much.` : r <= 2 / 3 ? ` That is ${Math.round(r * 100)}% of it.` : " About as usual.";
  }
  return `${last} On ${day}s at that hour ${usual}.${verdict}`;
}
/** The week grid, Monday first: each slot's shade step (0 quiet to 5 the
 *  busiest slot, on a square-root scale; -1 too few days to say), whether it
 *  is the slot of the hour that just closed, and what it says on hover. */
export function usualRows(u: Usual): { day: string; cells: { level: number; now: boolean; title: string }[] }[] {
  const med = u.median ?? [];
  let max = 0;
  for (const row of med) for (const v of row) if (v !== null && v > max) max = v;
  return [1, 2, 3, 4, 5, 6, 0].map((wd) => ({
    day: WEEKDAYS[wd].slice(0, 3),
    cells: Array.from({ length: 24 }, (_, h) => {
      const v = med[wd]?.[h] ?? null;
      const n = u.samples?.[wd]?.[h] ?? 0;
      const at = `${WEEKDAYS[wd]} ${hourSpan(h)}`;
      return {
        level: v === null ? -1 : v === 0 || max === 0 ? 0 : Math.ceil(5 * Math.sqrt(v / max)),
        now: !!u.now && u.now.weekday === wd && u.now.hour === h,
        title: v === null ? `${at}: not enough days to say (${n} counted)` : `${at}: usually ${moved(v)}, the median of ${n} days`,
      };
    }),
  }));
}

export interface RhythmDay { date: string; bytes: number | null; whole: boolean }
export interface Rhythm {
  confidence: "no_data" | "too_new" | "normal"; wholeDays: number; medianDay: number | null;
  days: RhythmDay[]; yesterday: RhythmDay; timesMedian: number | null;
  trend: "rising" | "falling" | "steady" | "unknown"; busiestShare: number;
  awake: { hour: number; bytes: number }[]; zone: string;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
/** A daily file's UTC date as "19 Nov", never shifted by a zone. */
const dayOf = (date: string) => `${Number(date.slice(8, 10))} ${MONTHS[Number(date.slice(5, 7)) - 1]}`;

/** What a device's rhythm says: yesterday against its own median day with
 *  both sides named, the week's trend, and the hours it was awake in that it
 *  usually sleeps through; a comparison that cannot be made is refused. */
export function rhythmSentences(r: Rhythm): string[] {
  if (r.confidence === "no_data") return ["The daily files hold no day of this device yet, so there is nothing to compare."];
  const y = r.yesterday;
  const yday = `yesterday, ${dayOf(y.date)},`;
  if (r.confidence === "too_new") {
    const what = y.bytes === null ? `${yday} has no file` : `${yday} it moved ${moved(y.bytes)}`;
    return [`The router has counted ${r.wholeDays} whole day${r.wholeDays === 1 ? "" : "s"} of this device; ${what}. A usual day needs at least 7, so it is not compared.`];
  }
  const median = `Its median day over the last ${r.wholeDays} whole days is ${moved(r.medianDay ?? 0)}`;
  let first: string;
  if (y.bytes === null) first = `${median}; ${yday} has no file, so it is not compared.`;
  else if (!y.whole) first = `${median}; ${yday} the router was not counting all day (it moved ${moved(y.bytes)} while it was), so it is not compared.`;
  else if (r.timesMedian === null) first = `${median}; ${yday} it moved ${moved(y.bytes)}.`;
  else {
    const t = Number(r.timesMedian.toFixed(r.timesMedian < 10 ? 1 : 0));
    first = `${median}; ${yday} it moved ${moved(y.bytes)}, ${t === 1 ? "the same as" : `${t} times`} its median day.`;
  }
  const out = [first];
  const trend = { rising: "more than", falling: "less than", steady: "about as much as", unknown: "" }[r.trend];
  if (trend) out.push(`Over the last 7 days it moved ${trend} in the 7 before.`);
  if (r.awake.length) {
    const hours = r.awake.map((a) => `${two(a.hour)}:00`);
    const list = hours.length === 1 ? hours[0] : `${hours.slice(0, -1).join(", ")} and ${hours[hours.length - 1]}`;
    const sum = r.awake.reduce((n, a) => n + a.bytes, 0);
    out.push(`Yesterday it moved ${moved(sum)} at ${list}, hour${hours.length === 1 ? "" : "s"} in which it usually moves nothing.`);
  }
  return out;
}

/** A device's days as bars, oldest first: the busiest day sets the scale, a
 *  day with no file has no bar (never a quiet one), a partly counted day is
 *  apart from a whole one, and yesterday, the day compared, is marked. */
export function rhythmBars(r: Rhythm): { date: string; height: number | null; kind: "whole" | "partial" | "unknown"; yesterday: boolean; title: string }[] {
  const peak = Math.max(0, ...r.days.map((d) => d.bytes ?? 0));
  return r.days.map((d, i) => {
    const yesterday = i === r.days.length - 1;
    const kind = d.bytes === null ? "unknown" : d.whole ? "whole" : "partial";
    const title =
      d.bytes === null
        ? `${dayOf(d.date)}: nothing counted for this device`
        : `${dayOf(d.date)}: ${moved(d.bytes)}${d.whole ? "" : ", the router was not counting all day"}${yesterday ? " (yesterday)" : ""}`;
    return { date: d.date, height: d.bytes === null ? null : peak > 0 ? d.bytes / peak : 0, kind, yesterday, title };
  });
}
