// The search behind Cmd+K and "/": every thing the console names, found by
// its name or by what identifies it (an address, a MAC, a hostname, an AS
// number). Pure, so the ranking is tested without a page.

import type { Device, Group, HouseApp, HouseDestinations } from "./types";
import { describeRule, type Rule } from "./rules";
import { entityHref, type Entity } from "./entity";
import { appName as appLabel, category, countryName } from "./format";

/** One thing the search can find: what it is, how it reads, where it leads. */
export interface Hit {
  kind: Entity["kind"];
  label: string;
  /** A second line: what kind of thing, and what else identifies it. */
  sub: string;
  href: string;
  /** What else finds it, lower case: addresses, MAC, hostname, AS number. */
  keys: string;
}

const KIND_ORDER: Entity["kind"][] = ["client", "app", "category", "country", "network", "domain", "group", "rule"];

/** The index of everything named on the console's screens. Folded rows and unknowns are not things. */
export function buildIndex(src: {
  devices: Device[]; apps: HouseApp[]; dest: HouseDestinations; groups: Group[]; rules: Rule[]; appName?: (id: string) => string;
}): Hit[] {
  const out: Hit[] = [];
  const add = (e: Entity, label: string, sub: string, keys: string[] = []) => {
    const href = entityHref(e);
    if (!href || !label) return;
    out.push({ kind: e.kind, label, sub, href, keys: keys.join(" ").toLowerCase() });
  };
  for (const d of src.devices) {
    const name = d.label || d.name || d.hostname || d.mac;
    add({ kind: "client", mac: d.mac }, name, ["client", d.hostname, ...(d.ipv4 ?? [])].filter(Boolean).join(" · "), [d.mac, d.hostname, ...(d.ipv4 ?? []), ...(d.ipv6 ?? [])]);
  }
  const cats = new Set<string>();
  for (const a of src.apps) {
    if (a.other || a.app.id === "other" || a.app.id === "unknown") continue;
    add({ kind: "app", id: a.app.id }, appLabel(a.app.name), ["app", category(a.app.category)].filter(Boolean).join(" · "), [a.app.id, a.app.category]);
    if (a.app.category) cats.add(a.app.category);
  }
  for (const c of cats) add({ kind: "category", id: c }, category(c), "category");
  for (const c of src.dest.countries) {
    if (c.other || !/^[A-Z]{2}$/.test(c.cc)) continue;
    add({ kind: "country", cc: c.cc }, countryName(c.cc), `country · ${c.cc}`, [c.cc]);
  }
  for (const n of src.dest.networks) {
    if (n.other || !n.org) continue;
    add({ kind: "network", asn: n.asn }, n.org, `network · AS${n.asn}`, [n.asn, `as${n.asn}`]);
  }
  for (const d of src.dest.domains) {
    if (d.other) continue;
    add({ kind: "domain", name: d.name }, d.name, d.app ? `domain · ${appLabel(d.app.name)}` : "domain");
  }
  const groupName = (id: string) => src.groups.find((g) => g.id === id)?.name ?? id;
  for (const g of src.groups) add({ kind: "group", id: g.id }, g.name, `group · ${g.members.length} ${g.members.length === 1 ? "device" : "devices"}`, [g.id]);
  const devName = (m: string) => {
    const d = src.devices.find((x) => x.mac === m);
    return d ? d.label || d.name || d.hostname || m : m;
  };
  for (const r of src.rules) add({ kind: "rule", id: r.id }, describeRule(r, { app: src.appName, group: groupName, device: devName }), "rule", [r.id]);
  return out;
}

/** How well q matches a hit: 0 the whole name, 1 its start, 2 a word's start, 3 inside it, 4 what else identifies it; -1 not at all. */
function score(q: string, h: Hit): number {
  const l = h.label.toLowerCase();
  if (l === q) return 0;
  if (l.startsWith(q)) return 1;
  if (l.split(/[^\p{L}\p{N}]+/u).some((w) => w.startsWith(q))) return 2;
  if (l.includes(q)) return 3;
  if (h.keys.includes(q)) return 4;
  return -1;
}

/** The best n things for what was typed, best match first, then by kind, then by name. */
export function searchHits(q: string, index: Hit[], n = 12): Hit[] {
  const t = q.trim().toLowerCase();
  if (!t) return [];
  return index
    .map((h) => ({ h, s: score(t, h) }))
    .filter((x) => x.s >= 0)
    .sort((a, b) => a.s - b.s || KIND_ORDER.indexOf(a.h.kind) - KIND_ORDER.indexOf(b.h.kind) || a.h.label.localeCompare(b.h.label, undefined, { numeric: true }))
    .slice(0, n)
    .map((x) => x.h);
}
