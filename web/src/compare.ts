// Two or three clients side by side: which to compare, the address that
// shares the comparison, and the rows that tell them apart.

import type { Device } from "./types";
import { appName, bytes, rate } from "./format";
import type { Entity } from "./entity";

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

/** One cell: a text, a link where it names a thing, a note under it, or a list of linked names. */
export interface Cell {
  text: string;
  e?: Entity;
  note?: string;
  list?: { text: string; e: Entity }[];
}

/** The rows of the comparison, one cell per client in the order given. Now is the window's average, as on Clients. */
export function compareRows(devs: Device[], groupName: (id: string) => string, win: string, countries: Record<string, string[]> = {}): { label: string; cells: Cell[] }[] {
  const row = (label: string, f: (d: Device) => Cell) => ({ label, cells: devs.map(f) });
  const text = (f: (d: Device) => string) => (d: Device): Cell => ({ text: f(d) });
  return [
    row("State", text((d) => (d.online ? "online" : "offline"))),
    row(`Now ↓, ${win} avg`, (d) => ({ text: rate(d.recent?.down ?? 0), note: `right now ${rate(d.rate?.down ?? 0)}` })),
    row(`Now ↑, ${win} avg`, (d) => ({ text: rate(d.recent?.up ?? 0), note: `right now ${rate(d.rate?.up ?? 0)}` })),
    row("24 h ↓", text((d) => bytes(d.last24h?.down ?? 0))),
    row("24 h ↑", text((d) => bytes(d.last24h?.up ?? 0))),
    row("Busiest app", (d) => (d.topApp ? { text: appName(d.topApp.name), e: { kind: "app", id: d.topApp.id } } : { text: "-" })),
    row("Countries", (d) => {
      const list = (countries[d.mac] ?? []).filter((cc) => /^[A-Z]{2}$/.test(cc)).map((cc) => ({ text: cc, e: { kind: "country", cc } as Entity }));
      return list.length ? { text: "", list } : { text: String(d.countryCount ?? 0) };
    }),
    row("Group", (d) => (d.group ? { text: groupName(d.group), e: { kind: "group", id: d.group } } : { text: "no group" })),
    row("Connected", text(connected)),
    row("IPv4", text((d) => (d.ipv4 ?? []).join(", ") || "-")),
    row("Vendor", text((d) => d.vendor || "-")),
  ];
}
