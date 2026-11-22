// The Network screen: how the router is wired: port speeds, Wi-Fi signal,
// WireGuard handshakes and where a forward leads. No DOM here, so node tests
// cover it.

import type { NetForward } from "./types";

/** A port's link speed: 1000 is "1 Gbit/s"; 0 (no link, or unknown) is "". */
export function speedText(mbps: number): string {
  if (!mbps || mbps < 0) return "";
  return mbps >= 1000 ? `${+(mbps / 1000).toFixed(1)} Gbit/s` : `${mbps} Mbit/s`;
}

/** A Wi-Fi client's signal with a word for it; 0 (unknown) is "". */
export function signalText(dbm: number): string {
  if (!dbm) return "";
  const word = dbm >= -55 ? "excellent" : dbm >= -67 ? "good" : dbm >= -75 ? "fair" : "weak";
  return `${dbm} dBm, ${word}`;
}

/** How long ago a WireGuard peer last shook hands, or "never". */
export function handshakeText(iso: string | null, nowMs: number): string {
  if (!iso) return "never";
  const s = Math.max(0, Math.round((nowMs - Date.parse(iso)) / 1000));
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  const d = Math.floor(s / 86400);
  return `${d} ${d === 1 ? "day" : "days"} ago`;
}

/** A peer that shook hands in the last three minutes is talking: WireGuard renews every two. */
export function peerConnected(iso: string | null, nowMs: number): boolean {
  return !!iso && nowMs - Date.parse(iso) <= 180_000;
}

/** Where a forward leads: the device by name with its address, the address, or the router itself. */
export function forwardTarget(f: Pick<NetForward, "destIp" | "destPort" | "device">): string {
  const where = !f.destIp ? "the router" : f.device ? `${f.device.name} (${f.destIp})` : f.destIp;
  return `${where}:${f.destPort}`;
}

/** A public key, shortened to its ends for recognising it. */
export function shortKey(k: string): string {
  return k.length > 12 ? `${k.slice(0, 8)}…${k.slice(-4)}` : k;
}
