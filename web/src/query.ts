// Routes, periods and the flows search: the parts of the page that turn what
// the person typed or clicked into what the API is asked.

import type { Range } from "./types";

export type Route =
  | { name: "overview" }
  | { name: "clients" }
  | { name: "client"; mac: string }
  | { name: "flows"; q: string }
  | { name: "rules" }
  | { name: "filters" }
  | { name: "security" };

/** Reads a location hash: #/, #/clients, #/client/<mac>, #/flows?q=..., #/rules, #/rules/filters, #/security */
export function parseRoute(hash: string): Route {
  const raw = hash.replace(/^#/, "");
  const [path, query = ""] = raw.split("?", 2);
  const parts = path.split("/").filter(Boolean);
  switch (parts[0]) {
    case "clients":
      return { name: "clients" };
    case "client": {
      let mac = "";
      try {
        mac = decodeURIComponent(parts.slice(1).join("/")).toLowerCase();
      } catch {
        mac = "";
      }
      return mac ? { name: "client", mac } : { name: "clients" };
    }
    case "rules":
      return parts[1] === "filters" ? { name: "filters" } : { name: "rules" };
    case "flows":
      return { name: "flows", q: new URLSearchParams(query).get("q") ?? "" };
    case "security":
      return { name: "security" };
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
export function flowsPath(f: FlowFilter, o: { live: boolean; limit: number; cursor?: string }): string {
  const p = new URLSearchParams();
  for (const k of ["q", "mac", "app", "cc", "dir"] as const) if (f[k]) p.set(k, f[k]);
  if (o.live) p.set("live", "1");
  p.set("limit", String(o.limit));
  if (o.cursor) p.set("cursor", o.cursor);
  return "/api/flows?" + p.toString();
}
