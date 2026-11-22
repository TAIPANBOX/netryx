// What the screens decide before they draw: which countries to name, how to
// word the line's quality, the Sankey's nodes and bands, which flow is
// selected. Kept apart from Preact so node can test it.

import type { AppUsage, FlowRow, Wan } from "./types";
import type { SankeyLink, SankeyNode } from "./geometry";
import { appName, countryName, ms, pct, share } from "./format";

export const LAYER_COLORS = ["#4C9AFF", "#2F6FD6", "#9BD0FF", "#7FB2F0"];
export const OTHER_COLOR = "#3A4658";
export const UP_COLOR = "#F5A524";

/** The rollup's catch-all country: traffic it could not place. */
const UNKNOWN_CC = "--";

/** The country of traffic between two devices at home: not a place. */
const LAN_CC = "LAN";

/**
 * Whether a country row is a place the device talked to: not the unknown
 * "--", not the house's own "LAN", not a catch-all row. The overview's lists
 * and the client card's count share it.
 */
export function isRealCountry(c: { cc: string; other?: boolean }): boolean {
  return !c.other && !!c.cc && c.cc !== UNKNOWN_CC && c.cc !== LAN_CC;
}

/** A client's countries for a line of text, real countries only. */
export function countryList(ccs: string[] | null): string {
  return (ccs ?? []).filter((cc) => isRealCountry({ cc })).join(", ");
}

/**
 * The line-quality tile. An unmeasured line (no probe answered yet) is said
 * plainly and without colour, and its 0% loss is not shown, because nothing
 * was measured. Colour is kept for a measured loss.
 */
export function lineQuality(w: Wan): { value: string; sub: string; bad: boolean } {
  if (w.latencyMs === null) return { value: "no data", sub: "latency not measured yet", bad: false };
  const sub = `loss ${pct(w.lossPct)}` + (w.jitterMs === null ? "" : ` · jitter ${ms(w.jitterMs)}`);
  return { value: ms(w.latencyMs), sub, bad: w.lossPct > 0 };
}

const COUNTRY_COLOR = "#2F6FD6";
const SANKEY_APP_COLORS = [LAYER_COLORS[0], LAYER_COLORS[2], LAYER_COLORS[3]];

/**
 * Client -> its top three apps (the rest, and the catch-all app, as "others")
 * -> the top four countries those apps reached (the rest, the unknown "--"
 * and catch-all places as one "others"). Widths are bytes both ways.
 */
export function sankeyOf(name: string, apps: AppUsage[]): { nodes: SankeyNode[]; links: SankeyLink[] } {
  const total = apps.reduce((a, x) => a + x.down + x.up, 0);
  if (total === 0) return { nodes: [], links: [] };
  const sorted = [...apps].sort((a, b) => b.down + b.up - (a.down + a.up));
  const named = sorted.filter((a) => !a.other);
  const top = named.slice(0, 3);
  const rest = [...named.slice(3), ...sorted.filter((a) => a.other)];

  const isService = (p: { cc: string; other?: boolean }) => !!p.other || !p.cc || p.cc === UNKNOWN_CC;
  const cc = new Map<string, number>();
  let all = 0;
  for (const a of apps) {
    for (const p of a.places ?? []) {
      all += p.down + p.up;
      if (!isService(p)) cc.set(p.cc, (cc.get(p.cc) ?? 0) + p.down + p.up);
    }
  }
  const topCC = [...cc.entries()].sort((a, b) => b[1] - a[1]).slice(0, 4).map(([k]) => k);
  const bucket = (p: { cc: string; other?: boolean }) => (!isService(p) && topCC.includes(p.cc) ? "cc:" + p.cc : "cc:*");

  const nodes: SankeyNode[] = [{ id: "dev", col: 0, label: name, color: "#E6EDF3" }];
  const links: SankeyLink[] = [];
  const perBucket = new Map<string, number>();
  const addApp = (id: string, label: string, color: string, group: AppUsage[], icon?: string) => {
    const v = group.reduce((a, x) => a + x.down + x.up, 0);
    nodes.push({ id, col: 1, label: `${label} ${share(v / total)}`, color, ...(icon ? { icon, name: label } : {}) });
    links.push({ from: "dev", to: id, value: v });
    const per = new Map<string, number>();
    for (const a of group) {
      for (const p of a.places ?? []) {
        const k = bucket(p);
        per.set(k, (per.get(k) ?? 0) + p.down + p.up);
        perBucket.set(k, (perBucket.get(k) ?? 0) + p.down + p.up);
      }
    }
    for (const [k, val] of per) links.push({ from: id, to: k, value: val });
  };
  top.forEach((a, i) => addApp("app:" + a.app.id, appName(a.app.name), SANKEY_APP_COLORS[i], [a], a.app.id));
  if (rest.length) addApp("app:*", "others", "#93A1B3", rest, "other");
  const denom = all || 1;
  for (const c of topCC) {
    const label = c === LAN_CC ? countryName(c) : c;
    nodes.push({ id: "cc:" + c, col: 2, label: `${label} ${share((cc.get(c) ?? 0) / denom)}`, color: COUNTRY_COLOR });
  }
  if ((perBucket.get("cc:*") ?? 0) > 0) {
    nodes.push({ id: "cc:*", col: 2, label: `others ${share((perBucket.get("cc:*") ?? 0) / denom)}`, color: COUNTRY_COLOR });
  }
  return { nodes, links };
}

/**
 * What identifies a flow across refreshes: its two ends, protocol, device and
 * start. Byte counts and the end time change while it lives; these do not.
 */
export function flowKey(f: FlowRow): string {
  return [f.mac, f.local, f.remote, f.proto, f.start].join("|");
}

/**
 * The flow the detail panel shows: the row with the chosen key wherever it
 * moved, or, when it left the list, the last details seen, marked as ended.
 * With nothing chosen, the first row.
 */
export function selectFlow(rows: FlowRow[], key: string, last: FlowRow | null): { row: FlowRow | null; ended: boolean } {
  if (!key) return { row: rows[0] ?? null, ended: false };
  const found = rows.find((r) => flowKey(r) === key);
  if (found) return { row: found, ended: false };
  return last ? { row: last, ended: true } : { row: rows[0] ?? null, ended: false };
}
