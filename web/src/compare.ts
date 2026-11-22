// Two or three clients side by side: which to compare, the address that
// shares the comparison, and the rows that tell them apart.

import type { Device } from "./types";
import { appName, bytes, rate } from "./format";

/** How many clients a comparison holds. */
export const COMPARE_MAX = 3;

/** The comparison's address: one mac per client, in the order picked. */
export function compareHash(macs: string[]): string {
  const p = new URLSearchParams();
  for (const m of macs.slice(0, COMPARE_MAX)) p.append("mac", m);
  return `#/compare?${p.toString()}`;
}

/** A pick toggles a client; a fourth is refused rather than pushing one out unseen. */
export function pickCompare(sel: string[], mac: string): string[] {
  if (sel.includes(mac)) return sel.filter((m) => m !== mac);
  return sel.length >= COMPARE_MAX ? sel : [...sel, mac];
}

function connected(d: Device): string {
  if (d.link?.kind === "wifi") return `Wi-Fi ${d.link.ssid || d.link.iface || ""}`.trim();
  if (d.link?.kind === "wired") return `wired ${d.link.iface || ""}`.trim();
  return d.link?.kind || "-";
}

/** The rows of the comparison, one value per client in the order given. */
export function compareRows(devs: Device[], groupName: (id: string) => string): { label: string; values: string[] }[] {
  const row = (label: string, f: (d: Device) => string) => ({ label, values: devs.map(f) });
  return [
    row("State", (d) => (d.online ? "online" : "offline")),
    row("Now ↓", (d) => rate(d.recent?.down ?? 0)),
    row("Now ↑", (d) => rate(d.recent?.up ?? 0)),
    row("24 h ↓", (d) => bytes(d.last24h?.down ?? 0)),
    row("24 h ↑", (d) => bytes(d.last24h?.up ?? 0)),
    row("Busiest app", (d) => (d.topApp ? appName(d.topApp.name) : "-")),
    row("Countries", (d) => String(d.countryCount ?? 0)),
    row("Group", (d) => (d.group ? groupName(d.group) : "no group")),
    row("Connected", connected),
    row("IPv4", (d) => (d.ipv4 ?? []).join(", ") || "-"),
    row("Vendor", (d) => d.vendor || "-"),
  ];
}
