// Routes, periods and the flows search: the parts of the page that turn what
// the person typed or clicked into what the API is asked.

import type { Range } from "./types";

export type Route =
  | { name: "overview" }
  | { name: "clients"; q?: string }
  | { name: "client"; mac: string; from?: string }
  | { name: "flows"; q: string; from?: string; to?: string }
  | { name: "rules"; rule?: string }
  | { name: "filters" }
  | { name: "security" }
  | { name: "apps"; cat?: string; q?: string }
  | { name: "app"; id: string }
  | { name: "destinations" }
  | { name: "destination"; cc: string }
  | { name: "network" }
  | { name: "asn"; asn: number; from: string }
  | { name: "domain"; domain: string; from: string }
  | { name: "group"; id: string; from: string };

/** Only the keys that carry a value: a route without filters stays as it was. */
function some<T extends object>(r: T, extra: Record<string, string | null>): T {
  const out = { ...r } as Record<string, unknown>;
  for (const [k, v] of Object.entries(extra)) if (v) out[k] = v;
  return out as T;
}

/** Reads a location hash: #/, #/clients?q=, #/client/<mac>?from=, #/flows?q=&from=&to=, #/rules?rule=, #/rules/filters, #/security, #/apps?cat=&q=, #/app/<id>, #/destinations, #/destination/<cc>, #/network, #/asn/<n>, #/domain/<name>, #/group/<id> */
export function parseRoute(hash: string): Route {
  const raw = hash.replace(/^#/, "");
  const [path, query = ""] = raw.split("?", 2);
  const qs = new URLSearchParams(query);
  const parts = path.split("/").filter(Boolean);
  const part = (i: number) => {
    try {
      return decodeURIComponent(parts.slice(i).join("/"));
    } catch {
      return "";
    }
  };
  switch (parts[0]) {
    case "clients":
      return some({ name: "clients" }, { q: qs.get("q") });
    case "client": {
      const mac = part(1).toLowerCase();
      return mac ? some({ name: "client", mac }, { from: qs.get("from") }) : { name: "clients" };
    }
    case "rules":
      return parts[1] === "filters" ? { name: "filters" } : some({ name: "rules" }, { rule: qs.get("rule") });
    case "flows":
      return some({ name: "flows", q: qs.get("q") ?? "" }, { from: qs.get("from"), to: qs.get("to") });
    case "security":
      return { name: "security" };
    case "apps":
      return some({ name: "apps" }, { cat: qs.get("cat"), q: qs.get("q") });
    case "app": {
      const id = part(1);
      return /^[a-z0-9][a-z0-9._+-]{0,63}$/.test(id) ? { name: "app", id } : { name: "apps" };
    }
    case "destinations":
      return { name: "destinations" };
    case "destination": {
      const cc = (parts[1] ?? "").toUpperCase();
      return /^[A-Z]{2}$/.test(cc) ? { name: "destination", cc } : { name: "destinations" };
    }
    case "network":
      return { name: "network" };
    case "asn": {
      const n = Number(parts[1]);
      return Number.isInteger(n) && n > 0 && n < 2 ** 32 ? { name: "asn", asn: n, from: qs.get("from") ?? "" } : { name: "destinations" };
    }
    case "domain": {
      const d = part(1);
      return d && d.length <= 253 && /^[A-Za-z0-9_.-]+$/.test(d) && !d.includes("..") ? { name: "domain", domain: d, from: qs.get("from") ?? "" } : { name: "destinations" };
    }
    case "group": {
      const id = part(1);
      return /^[a-z][a-z0-9_]{0,31}$/.test(id) ? { name: "group", id, from: qs.get("from") ?? "" } : { name: "clients" };
    }
    default:
      return { name: "overview" };
  }
}

export const RANGES: { id: Range; label: string; step: number }[] = [
  { id: "1h", label: "1 h", step: 60 },
  { id: "24h", label: "24 h", step: 600 },
  { id: "7d", label: "7 days", step: 3600 },
  { id: "30d", label: "30 days", step: 86400 },
];

export type Win = "1m" | "3m" | "10m";
/** The windows the Now columns average over. */
export const WINDOWS: { id: Win; label: string }[] = [
  { id: "1m", label: "1 min" },
  { id: "3m", label: "3 min" },
  { id: "10m", label: "10 min" },
];
export function windowLabel(w: Win): string {
  return WINDOWS.find((x) => x.id === w)?.label ?? w;
}

export function stepOf(r: Range): number {
  return RANGES.find((x) => x.id === r)?.step ?? 600;
}

export interface FlowFilter { q: string; mac: string; app: string; cc: string; dir: string }

const MAC = /^([0-9a-f]{2}:){5}[0-9a-f]{2}$/i;

/**
 * Splits a search like `client:"Yard camera" dir:out zoom` into the API's
 * filters and free text. A filter the API would refuse stays in the text, so
 * nothing typed silently disappears.
 */
export function parseFlowQuery(text: string, devices: { mac: string; name: string }[] = []): FlowFilter {
  const f: FlowFilter = { q: "", mac: "", app: "", cc: "", dir: "" };
  const rest: string[] = [];
  const re = /(\w+):"([^"]*)"|(\w+):(\S+)|"([^"]*)"|(\S+)/g;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    const key = (m[1] ?? m[3] ?? "").toLowerCase();
    const val = m[2] ?? m[4] ?? m[5] ?? m[6] ?? "";
    if (!key) {
      rest.push(val);
      continue;
    }
    if (key === "mac" && MAC.test(val)) f.mac = val.toLowerCase();
    else if (key === "app") f.app = val;
    else if (key === "cc" && /^[a-z]{2}$/i.test(val)) f.cc = val.toUpperCase();
    else if (key === "dir" && ["out", "in", "local"].includes(val)) f.dir = val;
    else if (key === "client") {
      const d = devices.find((x) => x.name.toLowerCase() === val.toLowerCase());
      if (d) f.mac = d.mac;
      else rest.push(val);
    } else rest.push(m[0]);
  }
  f.q = rest.join(" ");
  return f;
}

/** The /api/flows URL for a filter; empty filters are left out. */
export function flowsPath(f: FlowFilter, o: { live: boolean; limit: number; cursor?: string; window?: Win; from?: string; to?: string; asn?: string }): string {
  const p = new URLSearchParams();
  for (const k of ["q", "mac", "app", "cc", "dir"] as const) if (f[k]) p.set(k, f[k]);
  if (o.live) p.set("live", "1");
  if (o.live && o.window) p.set("window", o.window);
  // A stretch of time is asked of the journal only.
  if (!o.live && o.from) p.set("from", o.from);
  if (!o.live && o.to) p.set("to", o.to);
  if (o.asn && /^\d+$/.test(o.asn)) p.set("asn", o.asn);
  p.set("limit", String(o.limit));
  if (o.cursor) p.set("cursor", o.cursor);
  return "/api/flows?" + p.toString();
}
