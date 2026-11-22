// What the client card's "Now" mode works out before it draws: the five
// minute chart on an even time axis, sparklines, and the tiles. Kept apart from
// Preact so node can test it.

import type { App, Live, LivePoint } from "./types";
import { appName, category, countryName, rate, ratePair, share, time } from "./format";
import type { IconManifest } from "./icons";
import { isRealCountry } from "./view";

export interface LiveCols {
  /** The right edge of the chart (the newest point), ms since the epoch. */
  end: number;
  colSec: number;
  /** The top apps by download over the window, largest first. */
  keys: string[];
  /** Download per key per column, bytes per second. */
  down: Record<string, number[]>;
  otherDown: number[];
  /** Whether apps past the top were folded into otherDown. */
  other: boolean;
  /** Upload of every app together per column, bytes per second. */
  up: number[];
}

/** The server's key for a device's apps past its busiest. */
const OTHER = "other";

const sum = (r: Record<string, number> | null | undefined) => {
  let s = 0;
  for (const v of Object.values(r ?? {})) s += v;
  return s;
};

/**
 * Lays the live ring onto even columns over the last `windowSec` seconds,
 * ending at the newest point. Each point is a rate over its own stepSec before
 * its time (2 s while somebody watches, 5 s before that), so it covers the
 * columns that span overlaps, weighted by the overlap: a rate is never divided
 * by a step, nor stretched over one it did not cover, and the bytes a column
 * shows are the bytes the points said moved in it. Seconds no point covers are
 * zero. The `top` apps by download keep their own layers; the rest fold into
 * one.
 */
export function liveChart(series: LivePoint[] | null, top: number, windowSec = 300, colSec = 2): LiveCols {
  const pts = (series ?? [])
    .map((p) => ({ p, t: Date.parse(p.t) }))
    .filter((x) => !Number.isNaN(x.t) && x.p.stepSec > 0);
  const empty: LiveCols = { end: 0, colSec, keys: [], down: {}, otherDown: [], other: false, up: [] };
  if (pts.length === 0) return empty;
  const end = Math.max(...pts.map((x) => x.t));
  const n = Math.round(windowSec / colSec);
  const start = end - n * colSec * 1000;

  // The server folds a device's apps past its busiest into the key "other";
  // it always joins the rest, so the chart never has two layers that both
  // mean "the rest".
  const totals = new Map<string, number>();
  let folded = false;
  for (const { p, t } of pts) {
    if (t <= start) continue;
    for (const [k, v] of Object.entries(p.down ?? {})) {
      if (k === OTHER) folded = true;
      else totals.set(k, (totals.get(k) ?? 0) + v * p.stepSec);
    }
  }
  const ranked = [...totals.entries()].sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1)).map(([k]) => k);
  const keys = ranked.slice(0, top);
  const kept = new Set(keys);
  const down: Record<string, number[]> = {};
  for (const k of keys) down[k] = new Array(n).fill(0);
  const otherDown = new Array(n).fill(0);
  const up = new Array(n).fill(0);

  const colMs = colSec * 1000;
  for (const { p, t } of pts) {
    const from = t - p.stepSec * 1000;
    if (t <= start) continue;
    const upRate = sum(p.up);
    let rest = 0;
    for (const [k, v] of Object.entries(p.down ?? {})) if (!kept.has(k)) rest += v;
    const first = Math.max(0, Math.floor((from - start) / colMs));
    const last = Math.min(n - 1, Math.ceil((t - start) / colMs) - 1);
    for (let c = first; c <= last; c++) {
      const c0 = start + c * colMs;
      const overlap = Math.max(0, Math.min(t, c0 + colMs) - Math.max(from, c0));
      if (overlap <= 0) continue;
      const w = overlap / colMs;
      for (const k of keys) down[k][c] += (p.down?.[k] ?? 0) * w;
      otherDown[c] += rest * w;
      up[c] += upRate * w;
    }
  }
  return { end, colSec, keys, down, otherDown, other: folded || ranked.length > top, up };
}

/** The chart's time labels: five minutes back to now. */
export const LIVE_TICKS = ["-5 min", "-4", "-3", "-2", "-1", "now"];

/** A sparkline in a 120 by 28 box: the highest value 2 from the top, zero 2 from the bottom. */
export function sparkPath(vals: number[] | null, w = 120, h = 28): string {
  const v = vals ?? [];
  if (v.length === 0) return "";
  const pts = v.length === 1 ? [v[0], v[0]] : v;
  const max = Math.max(0, ...pts);
  const step = w / (pts.length - 1);
  const span = h - 4;
  return pts
    .map((x, i) => `${i ? "L" : "M"}${(i * step).toFixed(1)} ${(h - 2 - (max > 0 ? (Math.max(0, x) / max) * span : 0)).toFixed(1)}`)
    .join(" ");
}

export interface LiveSummary {
  apps: number;
  /** The busiest app now and its share of every app's rate, down and up. */
  topName: string;
  topShare: number;
  /** Real countries only: not the local network, not the unknown "--". */
  countries: number;
  mainCC: string;
  /** Among the connections the router sent (the busiest 50). */
  tcp: number;
  udp: number;
}

export function liveSummary(l: Live): LiveSummary {
  const apps = l.apps ?? [];
  const all = apps.reduce((a, x) => a + x.down + x.up, 0);
  const top = [...apps].sort((a, b) => b.down + b.up - (a.down + a.up))[0];
  const real = (l.places ?? []).filter(isRealCountry);
  const byCC = new Map<string, number>();
  for (const p of real) byCC.set(p.cc, (byCC.get(p.cc) ?? 0) + p.down + p.up);
  const main = [...byCC.entries()].sort((a, b) => b[1] - a[1])[0];
  const conns = l.conns ?? [];
  return {
    apps: apps.length,
    topName: top ? top.app.name : "",
    topShare: top && all > 0 ? (top.down + top.up) / all : 0,
    countries: byCC.size,
    mainCC: main ? main[0] : "",
    tcp: conns.filter((c) => c.proto === 6).length,
    udp: conns.filter((c) => c.proto === 17).length,
  };
}

/** A place's name: the local network and the unknown are said, never shown as countries. */
export function placeName(p: { cc: string; other?: boolean }): string {
  if (p.cc === "LAN") return countryName("LAN");
  if (!isRealCountry(p)) return "unknown";
  return countryName(p.cc);
}

/** Where a live app's busiest connection goes: "DE · Netflix". */
export function whereText(cc: string, org: string): string {
  if (cc === "LAN" || !isRealCountry({ cc })) return placeName({ cc });
  return `${cc} · ${org || countryName(cc)}`;
}


/**
 * A chart layer's name. "unknown" gathers every unnamed organisation, so it
 * is just "Unknown"; an app seen in this view keeps its name after it leaves
 * the lists; one with no name says its category; anything else its id.
 */
export function layerLabel(id: string, seen: Map<string, App>, manifest?: IconManifest | null): string {
  if (id === "unknown") return "Unknown";
  const a = seen.get(id);
  if (a && a.name) return appName(a.name);
  // An app that left the apps list before this view saw it is named from the
  // catalog the icon manifest carries, never shown as its raw id.
  const m = manifest && Object.prototype.hasOwnProperty.call(manifest, id) ? manifest[id] : undefined;
  if (m && m.name) return m.name;
  if (a && a.category) return category(a.category);
  return id;
}

/**
 * The line under the active apps tile: the busiest app and its share while
 * anything moves, and no "busiest at 0%" when nothing does.
 */
export function appsTileSub(s: { apps: number; topName: string; topShare: number }): string {
  if (s.apps === 0) return "none";
  if (s.topName && s.topShare > 0) return `${appName(s.topName)} busiest, ${share(s.topShare)}`;
  return "all idle now";
}

/**
 * Keys for the connections list that follow a connection between polls when
 * the rows reorder: app, name, port and protocol, and a count for the second
 * and later connection alike in all four.
 */
export function connKeys(conns: { app: { id: string }; name: string; port: number; proto: number }[]): string[] {
  const n = new Map<string, number>();
  return conns.map((c) => {
    const k = `${c.app.id}|${c.name}|${c.port}|${c.proto}`;
    const i = (n.get(k) ?? 0) + 1;
    n.set(k, i);
    return i === 1 ? k : `${k}#${i}`;
  });
}

/** What the router answered a cut. */
export interface CutAnswer {
  cut?: boolean;
  held?: boolean;
  note?: string;
}

/** The line shown where the Cut button was: a cut the router could not hold
 * says so, with the router's reason. */
export function cutNote(a: CutAnswer): string {
  if (a.held !== false) return "Cut.";
  return `Cut, but ${a.note || "the connection may come back on its next packet"}.`;
}

/**
 * The Speed tile of a device's Now: the average over the window picked leads
 * (a steady figure), the last seconds beside it; before the router has an
 * average, the last seconds alone, said so.
 */
export function speedTile(recent: { down: number; up: number } | undefined, now: { down: number; up: number }, win: string): { value: string; sub: string } {
  if (!recent) {
    const p = ratePair(now.down, now.up);
    return { value: `↓ ${p.down} · ↑ ${p.up}`, sub: `${p.unit}, the last few seconds (no average yet)` };
  }
  const p = ratePair(recent.down, recent.up);
  return { value: `↓ ${p.down} · ↑ ${p.up}`, sub: `${p.unit}, average over ${win} · right now ↓ ${rate(now.down)}` };
}

export interface HourSpark { stepSec: number; known: number; bytes: number[]; since?: string }

/** A list row's last hour as a path, from the first counted step on, with its peak rate. */
export function hourSpark(s: HourSpark | undefined, w = 120, h = 28): { d: string; peak: number; label: string } {
  const v = s?.bytes ?? [];
  const n = v.length;
  if (!s || n === 0) return { d: "", peak: 0, label: "" };
  const known = Math.max(0, s.known);
  if (known >= n) return { d: "", peak: 0, label: "the router has not counted this hour yet" };
  const pts = n === 1 ? [v[0], v[0]] : v;
  const step = w / (pts.length - 1);
  const from = n === 1 ? 0 : known;
  const max = Math.max(0, ...pts.slice(from));
  const span = h - 4;
  const d = pts
    .slice(from)
    .map((x, j) => `${j ? "L" : "M"}${((from + j) * step).toFixed(1)} ${(h - 2 - (max > 0 ? (Math.max(0, x) / max) * span : 0)).toFixed(1)}`)
    .join(" ");
  const peak = s.stepSec > 0 ? max / s.stepSec : 0;
  const when = known > 0 && s.since ? `since ${time(s.since)}` : "last hour";
  return { d, peak, label: `${when}, peak ${rate(peak)}` };
}
