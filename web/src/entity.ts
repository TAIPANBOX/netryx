// Every name in the console is a link: a client, an app, a country, a network
// (AS), a domain, a category, a group or a rule each lead to their own screen,
// and a link from a detail carries where it came from, for the breadcrumbs.
// No DOM here, so node tests cover it.

import { countryName } from "./format";

export type Entity =
  | { kind: "client"; mac: string }
  | { kind: "app"; id: string }
  | { kind: "country"; cc: string }
  | { kind: "network"; asn: number | string }
  | { kind: "domain"; name: string }
  | { kind: "category"; id: string }
  | { kind: "group"; id: string }
  | { kind: "rule"; id: string };

const DOMAIN = /^[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?(\.[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?)*\.?$/;

/** Where a name leads, or nowhere (a folded rest, the house itself, an unknown network). */
export function entityHref(e: Entity): string | undefined {
  switch (e.kind) {
    case "client":
      return `#/client/${/^[0-9a-f:]+$/i.test(e.mac) ? e.mac : encodeURIComponent(e.mac)}`;
    case "app":
      return e.id && e.id !== "other" ? `#/app/${encodeURIComponent(e.id)}` : undefined;
    case "country":
      return /^[A-Z]{2}$/.test(e.cc) ? `#/destination/${e.cc}` : undefined;
    case "network": {
      const n = Number(e.asn);
      return Number.isInteger(n) && n > 0 ? `#/asn/${n}` : undefined;
    }
    case "domain":
      return e.name && e.name.length <= 253 && DOMAIN.test(e.name) ? `#/domain/${encodeURIComponent(e.name)}` : undefined;
    case "category":
      return e.id && !["other", "unknown"].includes(e.id) ? `#/apps?cat=${encodeURIComponent(e.id)}` : undefined;
    case "group":
      return e.id ? `#/group/${encodeURIComponent(e.id)}` : undefined;
    case "rule":
      return e.id ? `#/rules?rule=${encodeURIComponent(e.id)}` : undefined;
  }
}

/** A link that remembers the screen it was followed from. */
export function withFrom(href: string, from: string): string {
  if (!from) return href;
  return href + (href.includes("?") ? "&" : "?") + "from=" + encodeURIComponent(from);
}

export interface Crumb { label: string; href: string }

/** The breadcrumbs before a detail: the path from the screen it was opened from. */
export function crumbsFrom(from: string | undefined, names: { app?: (id: string) => string; group?: (id: string) => string }): Crumb[] {
  if (!from) return [];
  const i = from.indexOf(":");
  const kind = i < 0 ? from : from.slice(0, i);
  const key = i < 0 ? "" : from.slice(i + 1);
  switch (kind) {
    case "app":
      return key ? [{ label: "Apps", href: "#/apps" }, { label: names.app?.(key) ?? key, href: `#/app/${encodeURIComponent(key)}` }] : [];
    case "dest":
      return /^[A-Z]{2}$/.test(key) ? [{ label: "Destinations", href: "#/destinations" }, { label: countryName(key), href: `#/destination/${key}` }] : [];
    case "asn":
      return /^\d+$/.test(key) ? [{ label: "Destinations", href: "#/destinations" }, { label: `AS${key}`, href: `#/asn/${key}` }] : [];
    case "domain":
      return entityHref({ kind: "domain", name: key }) ? [{ label: "Destinations", href: "#/destinations" }, { label: key, href: `#/domain/${encodeURIComponent(key)}` }] : [];
    case "group":
      return key ? [{ label: "Clients", href: "#/clients" }, { label: names.group?.(key) ?? key, href: `#/group/${encodeURIComponent(key)}` }] : [];
    case "dest-root":
      return [{ label: "Destinations", href: "#/destinations" }];
    default:
      return [];
  }
}

/** A screen's address with its filters, so Back returns to them and a link shares them. Empty filters are left out. */
export function filterHash(screen: string, filters: Record<string, string>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(filters)) if (v) p.set(k, v);
  const q = p.toString();
  return `#/${screen}${q ? "?" + q : ""}`;
}

/** The flows of one stretch of a chart: from its start for the seconds it covers. */
export function stretchHref(start: string, seconds: number): string | undefined {
  const t = Date.parse(start);
  if (Number.isNaN(t) || !(seconds > 0)) return undefined;
  const iso = (ms: number) => new Date(ms).toISOString().replace(".000Z", "Z");
  return "#/flows?" + new URLSearchParams({ from: iso(t), to: iso(t + seconds * 1000) }).toString();
}
