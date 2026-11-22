// The client's connection map: device, then the apps, the domains they
// named, the networks (AS) behind them and the countries, banded by what each
// live flow moved over the window. Each column keeps its busiest few and
// folds the rest; every node but the device and a folded rest leads to a
// screen. No DOM here, so node tests cover it.

import type { FlowRow } from "./types";
import type { SankeyLink, SankeyNode } from "./geometry";
import { appName, countryName } from "./format";
import { LAYER_COLORS, OTHER_COLOR } from "./view";

export interface ConnMap { nodes: SankeyNode[]; links: SankeyLink[]; href: Record<string, string> }

const NEUTRAL = "#7FB2F0";
/** A name that carries less of the window than this folds into the rest: small bands would pile their labels up. */
const MIN_SHARE = 0.03;
const KINDS = [
  { id: "app", one: "app", other: "apps" },
  { id: "domain", one: "domain", other: "domains" },
  { id: "net", one: "network", other: "networks" },
  { id: "cc", one: "country", other: "countries" },
] as const;

type Key = { id: string; label: string; href?: string };

function keysOf(f: FlowRow): Key[] {
  const domain = f.name || "";
  const net = f.asn ? String(f.asn) : f.org || "";
  const cc = /^[A-Z]{2}$/.test(f.cc) ? f.cc : "";
  return [
    { id: f.app.id || "unknown", label: appName(f.app.name), href: `#/app/${encodeURIComponent(f.app.id || "unknown")}` },
    domain ? { id: domain, label: domain, href: `#/flows?q=${encodeURIComponent(domain)}` } : { id: "", label: "no name" },
    net ? { id: net, label: f.org || `AS${f.asn}`, href: cc ? `#/destination/${cc}` : undefined } : { id: "", label: "unknown network" },
    cc ? { id: cc, label: countryName(cc), href: `#/destination/${cc}` } : { id: "", label: f.cc === "LAN" ? "Local network" : "unknown country" },
  ];
}

/** The map from the live flows of one device; `top` nodes a column before the rest is folded. */
export function connMap(name: string, rows: FlowRow[], top = 5): ConnMap {
  const flows = rows
    .map((f) => ({ v: (f.winDown ?? 0) + (f.winUp ?? 0), keys: keysOf(f) }))
    .filter((f) => f.v > 0);
  if (flows.length === 0) return { nodes: [], links: [], href: {} };
  // The busiest keys of each column; the rest fold into one node.
  const total = flows.reduce((a, f) => a + f.v, 0);
  const kept = KINDS.map((_, c) => {
    const sum = new Map<string, number>();
    for (const f of flows) sum.set(f.keys[c].id, (sum.get(f.keys[c].id) ?? 0) + f.v);
    return new Set(
      [...sum.entries()]
        .filter(([, v]) => v >= MIN_SHARE * total)
        .sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1))
        .slice(0, top)
        .map(([k]) => k),
    );
  });
  const nodeId = (c: number, k: Key) => `${KINDS[c].id}:${kept[c].has(k.id) ? k.id : "*"}`;
  const nodes = new Map<string, SankeyNode & { v: number }>();
  const folded = KINDS.map(() => new Set<string>());
  const href: Record<string, string> = {};
  const links = new Map<string, SankeyLink>();
  const link = (from: string, to: string, v: number) => {
    const k = from + ">" + to;
    const l = links.get(k);
    if (l) l.value += v;
    else links.set(k, { from, to, value: v });
  };
  nodes.set("dev", { id: "dev", col: 0, label: name, color: "#E6EDF3", v: 0 });
  const appColor = new Map<string, string>();
  for (const f of flows) {
    let prev = "dev";
    f.keys.forEach((k, c) => {
      const id = nodeId(c, k);
      if (id.endsWith(":*")) folded[c].add(k.id);
      let n = nodes.get(id);
      if (!n) {
        const color = c === 0 ? (id.endsWith(":*") ? OTHER_COLOR : (appColor.set(id, LAYER_COLORS[appColor.size % LAYER_COLORS.length]), appColor.get(id)!)) : NEUTRAL;
        n = { id, col: c + 1, label: k.label, color, v: 0, ...(c === 0 && !id.endsWith(":*") ? { icon: k.id, name: k.label } : {}) };
        nodes.set(id, n);
        if (!id.endsWith(":*") && k.href && k.id) href[id] = k.href;
      }
      n.v += f.v;
      link(prev, id, f.v);
      prev = id;
    });
  }
  for (const [c, kind] of KINDS.entries()) {
    const n = nodes.get(`${kind.id}:*`);
    if (n) n.label = `${folded[c].size} other ${folded[c].size === 1 ? kind.one : kind.other}`;
  }
  // Each column busiest first, the folded rest last.
  const ordered = [...nodes.values()].sort((a, b) => a.col - b.col || Number(a.id.endsWith(":*")) - Number(b.id.endsWith(":*")) || b.v - a.v);
  return { nodes: ordered.map(({ v: _v, ...n }) => n), links: [...links.values()], href };
}
