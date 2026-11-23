// The client's full view: where it is connected (with the router's own
// wiring) and which rules reach it and from where. No DOM here, so node tests
// cover it.

import type { Device, NetworkView } from "./types";
import type { Rule } from "./rules";
import { appliesTo } from "./rules";
import { groupName } from "./format";
import { signalText, speedText } from "./network";

/** Where a device is connected: its port and the port's link, or its Wi-Fi network, band, channel and signal. */
export function whereConnected(d: Pick<Device, "mac" | "link" | "group">, net: Pick<NetworkView, "ports" | "wifi"> | undefined): string {
  const l = d.link;
  if (l.kind === "wifi") {
    const ap = net?.wifi.live.find((a) => a.iface === l.iface);
    return [
      `Wi-Fi${l.ssid || ap?.ssid ? ` "${l.ssid || ap?.ssid}"` : ""}`,
      l.band || ap?.band || "",
      ap?.channel ? `channel ${ap.channel}` : "",
      signalText(l.signal),
    ].filter(Boolean).join(" · ");
  }
  if (l.kind === "wired" || l.iface) {
    const port = net?.ports.find((p) => p.name === l.iface);
    return [
      "Wired",
      l.iface ? `port ${l.iface}` : "",
      port ? speedText(port.speedMbps) : "",
      port?.switch ? "on the hardware switch" : "",
    ].filter(Boolean).join(" · ");
  }
  return "not seen on a port or an access point";
}

/** The rules that reach a device, and from where: its own, one of its group's, or everyone's. */
export function rulesOn(rules: Rule[] | undefined, mac: string, groups: { id: string; name: string }[]): { rule: Rule; from: string }[] {
  const m = mac.toLowerCase();
  return (rules ?? [])
    .filter((r) => appliesTo(r, m))
    .map((r) => ({ rule: r, from: r.who === "device" ? "this device" : r.who === "group" ? `group ${groupName(r.group, groups)}` : "everyone" }));
}

/** What a rename or a new group sends: only what changed; a blank name is no rename. Null when nothing is to be sent. */
export function editBody(was: { name: string; group: string }, now: { name: string; group: string }): Record<string, string> | null {
  const out: Record<string, string> = {};
  const name = now.name.trim();
  if (name && name !== was.name) out.name = name;
  if (now.group !== was.group) out.group = now.group;
  return Object.keys(out).length ? out : null;
}

export interface NewFor {
  tooNew: boolean; knownDays: number; since: string;
  apps: { app: { id: string; name: string; category: string }; first: string; down: number; up: number }[];
  countries: { cc: string; first: string; down: number; up: number }[];
}

/** The line above what is new for a device. */
export function newForNote(n: NewFor): string {
  if (n.tooNew) {
    const known = n.knownDays < 1 ? "less than a day" : `${n.knownDays} day${n.knownDays === 1 ? "" : "s"}`;
    return `The router has known this device for ${known}: too new to tell what is new for it.`;
  }
  if (n.apps.length === 0 && n.countries.length === 0) return "Nothing new: every app and country it used since yesterday it had used in the 29 days before.";
  return "Used since yesterday and in none of the 29 days before:";
}

type ExposureNet = { forwards: { why: string; rules: { name: string; proto: string; port: string; destPort: string; enabled: boolean; device: { mac: string } | null }[]; upnp?: { proto: string; port: string; destPort: string; desc: string; device: { mac: string } | null }[] } };

/** What reaches a device from the internet. */
export function exposureOf(mac: string, net: ExposureNet | undefined, inbound: { cc: string }[]): { quiet: boolean; lines: string[] } {
  const lines: string[] = [];
  for (const f of net?.forwards.rules ?? []) {
    if (f.enabled && f.device?.mac === mac) lines.push(`Port ${f.port}/${f.proto} from the internet leads to it, port ${f.destPort} (forward "${f.name}")`);
  }
  for (const u of net?.forwards.upnp ?? []) {
    if (u.device?.mac === mac) lines.push(`It opened port ${u.port}/${u.proto} for itself through UPnP ("${u.desc}")`);
  }
  const outside = inbound.filter((f) => f.cc !== "LAN");
  if (outside.length > 0) {
    const ccs = [...new Set(outside.map((f) => f.cc).filter((c) => c && c !== "--"))].slice(0, 3);
    lines.push(`${outside.length} connection${outside.length === 1 ? "" : "s"} from the internet in the last 24 hours${ccs.length ? ", from " + ccs.join(", ") : ""}`);
  }
  if (lines.length === 0) return { quiet: true, lines: ["Nothing from the internet reaches it: no forwarded port, no UPnP mapping, no connection from outside in the last 24 hours."] };
  return { quiet: false, lines };
}
