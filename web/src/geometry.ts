// The arithmetic behind the charts, kept apart from Preact so it can be tested
// in node: paths, stacking, the top-N split of a series, and the Sankey layout.

import type { SeriesPoint, Range } from "./types";
import { dayMonth, time } from "./format";


/**
 * An area from the axis at `base` through every value and back. `dir` -1
 * draws upward (download, above the axis), +1 downward (upload, below it).
 * The x axis is 0..width; values are scaled so `max` reaches `h` pixels.
 */
export function areaPath(vals: number[], max: number, base: number, h: number, dir: 1 | -1, width = 1000): string {
  if (vals.length === 0) return "";
  const pts = vals.length === 1 ? [vals[0], vals[0]] : vals;
  const step = width / (pts.length - 1);
  let d = `M0 ${base}`;
  for (let i = 0; i < pts.length; i++) {
    const y = max > 0 ? base + dir * (pts[i] / max) * h : base;
    d += ` L${(i * step).toFixed(1)} ${y.toFixed(1)}`;
  }
  return d + ` L${width} ${base} Z`;
}

export interface Split {
  t: string[];
  /** The top keys by download over the period, largest first. */
  keys: string[];
  /** Download per key per step, in bytes per second. */
  down: Record<string, number[]>;
  /** Whether keys past the top were folded into otherDown. */
  other: boolean;
  otherDown: number[];
  /** Upload of every key together per step, in bytes per second. */
  up: number[];
  totalDown: number;
  totalUp: number;
  peakDown: { rate: number; at: string };
}

/**
 * Turns a series of per-step byte counts into per-second rates, keeping the
 * `top` keys with the most download and folding the rest into one layer.
 * Bytes are summed before dividing, so the layers add up to the step's total.
 * Keys in `fold` (the rollup's catch-all) always go to the other layer, so a
 * chart never has two layers that both mean "the rest".
 *
 * Each point is divided by the seconds it covers (`stepSec`, from the API),
 * never by the range's usual step: after a restart the hours before it come
 * back as hour points among minute ones. A point longer than `stepSec` is
 * drawn as that many columns at its rate, so the time axis stays even.
 */
export function splitSeries(series: SeriesPoint[], stepSec: number, top: number, fold: string[] = []): Split {
  const totals = new Map<string, number>();
  let totalDown = 0;
  let totalUp = 0;
  for (const p of series) {
    for (const [k, v] of Object.entries(p.down)) {
      totals.set(k, (totals.get(k) ?? 0) + v);
      totalDown += v;
    }
    for (const v of Object.values(p.up)) totalUp += v;
  }
  const folded = fold.some((k) => totals.has(k));
  const ranked = [...totals.entries()].filter(([k]) => !fold.includes(k)).sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1)).map(([k]) => k);
  const keys = ranked.slice(0, top);
  const kept = new Set(keys);
  const nominal = stepSec > 0 ? stepSec : 1;
  const t: string[] = [];
  const down: Record<string, number[]> = {};
  for (const k of keys) down[k] = [];
  const otherDown: number[] = [];
  const up: number[] = [];
  const peakDown = { rate: 0, at: "" };
  for (const p of series) {
    const div = p.stepSec && p.stepSec > 0 ? p.stepSec : nominal;
    const cols = Math.max(1, Math.round(div / nominal));
    let rest = 0;
    for (const [k, v] of Object.entries(p.down)) if (!kept.has(k)) rest += v;
    const upRate = Object.values(p.up).reduce((a, b) => a + b, 0) / div;
    const downRate = Object.values(p.down).reduce((a, b) => a + b, 0) / div;
    if (downRate > peakDown.rate) {
      peakDown.rate = downRate;
      peakDown.at = p.t;
    }
    const at = Date.parse(p.t);
    for (let i = 0; i < cols; i++) {
      t.push(i === 0 || Number.isNaN(at) ? p.t : new Date(at + i * nominal * 1000).toISOString());
      for (const k of keys) down[k].push((p.down[k] ?? 0) / div);
      otherDown.push(rest / div);
      up.push(upRate);
    }
  }
  return { t, keys, down, other: folded || ranked.length > top, otherDown, up, totalDown, totalUp, peakDown };
}

/** Running sums of layers listed bottom first: what each stacked edge is drawn at. */
export function cumulate(layers: number[][]): number[][] {
  const out: number[][] = [];
  for (const l of layers) {
    const prev = out[out.length - 1];
    out.push(l.map((v, i) => v + (prev ? prev[i] : 0)));
  }
  return out;
}

/** Up to five labels evenly along the series; the last one is "now" unless the series ends in the past (a stretch picked on a chart). */
export function timeTicks(ts: string[], range: Range, endsNow = true): string[] {
  if (ts.length === 0) return [];
  const n = Math.min(5, ts.length);
  const out: string[] = [];
  for (let i = 0; i < n; i++) {
    const idx = n === 1 ? ts.length - 1 : Math.round((i * (ts.length - 1)) / (n - 1));
    if (i === n - 1 && endsNow) {
      out.push("now");
    } else if (range === "1h" || range === "24h") {
      out.push(time(ts[idx]));
    } else {
      out.push(time(ts[idx]) === "-" ? "-" : dayMonth(ts[idx]));
    }
  }
  return out;
}


/** icon is the app id whose tile the node shows (app nodes only). */
export interface SankeyNode { id: string; col: number; label: string; color: string; icon?: string; name?: string }
export interface SankeyLink { from: string; to: string; value: number }
export interface PlacedNode extends SankeyNode { x: number; y: number; h: number; value: number }
export interface PlacedLink extends SankeyLink { d: string; w: number; color: string }

const COLS = [20, 280, 560];
const NODE_W = 10;

/** Where each column stands: the three-column chart as it always was, more columns spread evenly over the width. */
function columns(cols: number, width: number): number[] {
  if (cols === 3 && width === 600) return COLS;
  // Every column but the first labels its nodes on its right, the last too:
  // it stands far enough in to leave them room.
  const last = width - 170;
  return Array.from({ length: cols }, (_, c) => 20 + (c * (last - 20)) / Math.max(1, cols - 1));
}

/**
 * Lays out a Sankey of `cols` columns (three by default) in a box `width` wide. A node is as tall as the
 * larger of what flows into it and out of it; one scale serves every column,
 * so equal bytes are equal widths anywhere on the chart.
 */
export function sankeyLayout(
  nodes: SankeyNode[], links: SankeyLink[],
  box: { height: number; top: number; bottom: number; gap: number },
  cols = 3, width = 600,
): { nodes: PlacedNode[]; links: PlacedLink[] } {
  const COLS = columns(cols, width);
  const live = links.filter((l) => l.value > 0);
  const inSum = new Map<string, number>();
  const outSum = new Map<string, number>();
  for (const l of live) {
    outSum.set(l.from, (outSum.get(l.from) ?? 0) + l.value);
    inSum.set(l.to, (inSum.get(l.to) ?? 0) + l.value);
  }
  const value = (id: string) => Math.max(inSum.get(id) ?? 0, outSum.get(id) ?? 0);
  const avail = box.height - box.top - box.bottom;
  let scale = Infinity;
  for (let c = 0; c < COLS.length; c++) {
    const col = nodes.filter((n) => n.col === c && value(n.id) > 0);
    const total = col.reduce((a, n) => a + value(n.id), 0);
    if (total > 0) scale = Math.min(scale, (avail - box.gap * (col.length - 1)) / total);
  }
  if (!Number.isFinite(scale) || scale <= 0) return { nodes: [], links: [] };

  const placed: PlacedNode[] = [];
  for (let c = 0; c < COLS.length; c++) {
    let y = box.top;
    for (const n of nodes.filter((n) => n.col === c && value(n.id) > 0)) {
      const h = value(n.id) * scale;
      placed.push({ ...n, x: COLS[c] ?? 0, y, h, value: value(n.id) });
      y += h + box.gap;
    }
  }
  const at = new Map(placed.map((n) => [n.id, n]));
  // Links leave a node in the order of their targets and enter in the order
  // of their sources, which keeps the bands from crossing at the nodes.
  const ordered = live.filter((l) => at.has(l.from) && at.has(l.to));
  const outOff = new Map<string, number>();
  const inOff = new Map<string, number>();
  const outOrder = [...ordered].sort((a, b) => at.get(a.to)!.y - at.get(b.to)!.y);
  const inOrder = [...ordered].sort((a, b) => at.get(a.from)!.y - at.get(b.from)!.y);
  const srcY = new Map<SankeyLink, number>();
  const dstY = new Map<SankeyLink, number>();
  for (const l of outOrder) {
    const w = l.value * scale;
    const o = outOff.get(l.from) ?? 0;
    srcY.set(l, at.get(l.from)!.y + o + w / 2);
    outOff.set(l.from, o + w);
  }
  for (const l of inOrder) {
    const w = l.value * scale;
    const o = inOff.get(l.to) ?? 0;
    dstY.set(l, at.get(l.to)!.y + o + w / 2);
    inOff.set(l.to, o + w);
  }
  const out: PlacedLink[] = ordered.map((l) => {
    const s = at.get(l.from)!;
    const t = at.get(l.to)!;
    const x1 = s.x + NODE_W;
    const x2 = t.x;
    const y1 = srcY.get(l)!;
    const y2 = dstY.get(l)!;
    const m = (x1 + x2) / 2;
    const r = (v: number) => v.toFixed(1);
    return {
      ...l, w: l.value * scale,
      // A band takes the colour of the app it belongs to: the source in the
      // second column, the target in the first.
      color: s.col === 0 ? t.color : s.color,
      d: `M${r(x1)} ${r(y1)} C${r(m)} ${r(y1)} ${r(m)} ${r(y2)} ${r(x2)} ${r(y2)}`,
    };
  });
  return { nodes: placed, links: out };
}

/** The column a click at frac (0 left, 1 right) of a chart lands nearest to. */
export function pickColumn(frac: number, n: number): number {
  if (n <= 1) return 0;
  return Math.min(n - 1, Math.max(0, Math.round(frac * (n - 1))));
}

export interface CrossRow { label: string; color: string; rate: number }

/**
 * What the crosshair says at a column: the download of every visible layer
 * that moved, busiest first, their sum and the upload. Null off the chart.
 */
export function crosshairRows(col: number, layers: { label: string; color: string; values: number[] }[], up: number[], hidden: string[]): { down: number; up: number; rows: CrossRow[] } | null {
  if (col < 0 || col >= up.length) return null;
  const rows: CrossRow[] = [];
  let down = 0;
  for (const l of layers) {
    const rate = l.values[col] ?? 0;
    if (hidden.includes(l.label) || rate <= 0) continue;
    rows.push({ label: l.label, color: l.color, rate });
    down += rate;
  }
  rows.sort((a, b) => b.rate - a.rate);
  return { down, up: up[col] ?? 0, rows };
}

/** The column whose step holds the time ms, or -1 when no column does. */
export function columnOfTime(t: string[], stepSec: number, ms: number): number {
  if (t.length === 0) return -1;
  const first = Date.parse(t[0]);
  const i = Math.floor((ms - first) / (stepSec * 1000));
  return i >= 0 && i < t.length ? i : -1;
}

export interface Marker { x: number; text: string; kind: string; at: string }

/** Events on the time axis (0 to 1000 wide, as the charts draw), the ones inside it only. */
export function markersOn(t: string[], stepSec: number, ev: { at: string; text: string; kind: string }[]): Marker[] {
  if (t.length === 0) return [];
  const start = Date.parse(t[0]);
  const span = t.length * stepSec * 1000;
  const out: Marker[] = [];
  for (const e of ev) {
    const d = Date.parse(e.at) - start;
    if (Number.isNaN(d) || d < 0 || d >= span) continue;
    out.push({ x: Math.round((1000 * d) / span), text: e.text, kind: e.kind, at: e.at });
  }
  return out;
}

/**
 * The hidden layers after a legend click (toggle one) or double click (that
 * one alone, or all again when it already is). The last visible layer is
 * never hidden.
 */
export function legendAfter(hidden: string[], key: string, how: "toggle" | "isolate", all: string[]): string[] {
  const others = all.filter((k) => k !== key);
  if (how === "isolate") {
    const alone = !hidden.includes(key) && others.every((k) => hidden.includes(k));
    return alone ? [] : others;
  }
  if (hidden.includes(key)) return hidden.filter((k) => k !== key);
  return others.every((k) => hidden.includes(k)) ? hidden : [...hidden, key];
}

/** The busiest column, the first of equals; -1 when nothing moved. */
export function peakColumn(v: number[]): number {
  let best = -1;
  for (let i = 0; i < v.length; i++) if (v[i] > 0 && (best < 0 || v[i] > v[best])) best = i;
  return best;
}
