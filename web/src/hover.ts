// The small card a name shows when the pointer rests on it: which thing a
// link leads to, where its few facts are read, and what the card says.
// Pure, so every kind's card is tested without a page.

import type { Entity } from "./entity";
import { parseRoute } from "./query";
import { appName, bytes, category, countryName, plural, share } from "./format";
import { describeRule, stateOf, type Rule } from "./rules";

/** The thing a console link leads to, or null for a screen that is not one thing. */
export function hoverEntity(href: string): Entity | null {
  if (!href.startsWith("#/")) return null;
  const r = parseRoute(href);
  switch (r.name) {
    case "client":
      return { kind: "client", mac: r.mac };
    case "app":
      return { kind: "app", id: r.id };
    case "destination":
      return { kind: "country", cc: r.cc };
    case "asn":
      return { kind: "network", asn: r.asn };
    case "domain":
      return { kind: "domain", name: r.domain };
    case "group":
      return { kind: "group", id: r.id };
    case "rules":
      return r.rule ? { kind: "rule", id: r.rule } : null;
    case "apps":
      return r.cat && !r.q ? { kind: "category", id: r.cat } : null;
    default:
      return null;
  }
}

/** Where a card's facts are read: the thing's own read, over the last day. */
export function hoverPath(e: Entity): string {
  switch (e.kind) {
    case "client":
      return `/api/devices/${e.mac}`;
    case "app":
      return `/api/apps/${encodeURIComponent(e.id)}?range=24h`;
    case "country":
      return `/api/destinations/${e.cc}?range=24h`;
    case "network":
      return `/api/networks/${e.asn}?range=24h`;
    case "domain":
      return `/api/domains/${encodeURIComponent(e.name)}?range=24h`;
    case "group":
      return "/api/groups";
    case "rule":
      return "/api/rules";
    case "category":
      return "/api/apps?range=24h";
  }
}

// The reads are typed loosely: a card shows what is there and nothing else.
type Any = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any
const day = (d: Any) => `24 h: ↓ ${bytes(d.down ?? 0)} · ↑ ${bytes(d.up ?? 0)}`;
const len = (x: unknown) => (Array.isArray(x) ? x.length : 0);

/** What a card says: its title and a few short lines. */
export function hoverSummary(e: Entity, data: unknown, now = Date.now()): { title: string; lines: string[] } {
  const d = (data ?? {}) as Any;
  switch (e.kind) {
    case "client": {
      const where = [d.online ? "online" : "offline", ...(d.ipv4 ?? []).slice(0, 1)].join(" · ");
      const lines = [where, `24 h: ↓ ${bytes(d.last24h?.down ?? 0)} · ↑ ${bytes(d.last24h?.up ?? 0)}`];
      if (d.topApp) lines.push(`busiest app: ${appName(d.topApp.name)}`);
      if (d.countryCount) lines.push(plural(d.countryCount, "country", "countries"));
      return { title: d.label || d.name || d.hostname || e.mac, lines };
    }
    case "app":
      return {
        title: appName(d.app?.name ?? e.id),
        lines: [category(d.app?.category ?? "") || "no category", day(d), `${share(d.share ?? 0)} of the house's traffic`, plural(d.clientCount ?? 0, "client", "clients")],
      };
    case "country":
      return {
        title: countryName(e.cc),
        lines: [`${share(d.share ?? 0)} of what left the house`, day(d), `${plural(d.clientCount ?? 0, "client", "clients")} · ${plural(len(d.networks), "network", "networks")}`],
      };
    case "network": {
      const ccs = (d.countries ?? []).map((c: Any) => countryName(c.cc)).slice(0, 3).join(", ");
      return {
        title: d.org || `AS${e.asn}`,
        lines: [`AS${e.asn}${ccs ? ` · reached in ${ccs}` : ""}`, day(d), `${plural(len(d.clients), "client", "clients")} · ${plural(len(d.apps), "app", "apps")}`],
      };
    }
    case "domain": {
      const apps = (d.apps ?? []).map((a: Any) => appName(a.app?.name ?? "")).filter(Boolean).slice(0, 3).join(", ");
      return { title: e.name, lines: [day(d), apps ? `named for ${apps}` : "no app named", plural(len(d.clients), "client", "clients")] };
    }
    case "group": {
      const g = (Array.isArray(data) ? data : []).find((x: Any) => x.id === e.id);
      return g ? { title: g.name, lines: [`group of ${plural(len(g.members), "device", "devices")}`] } : { title: e.id, lines: ["this group no longer exists"] };
    }
    case "category": {
      const n = (Array.isArray(data) ? data : []).filter((a: Any) => a.app?.category === e.id).length;
      return { title: category(e.id), lines: [`${plural(n, "app", "apps")} the house used in 24 h`] };
    }
    case "rule": {
      const r = (Array.isArray(data) ? data : []).find((x: Any) => x.id === e.id) as Rule | undefined;
      if (!r) return { title: e.id, lines: ["this rule no longer exists"] };
      return { title: describeRule(r), lines: [stateOf(r, now).text, `blocked ${plural(r.hits, "time", "times")}`] };
    }
  }
}
