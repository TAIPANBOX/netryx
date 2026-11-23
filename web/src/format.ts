import { zone } from "./zone";
// How the console says numbers, sizes, rates and times. Formatting is done
// here by hand rather than by Intl.NumberFormat, so the router's page reads the
// same in every browser and the tests do not depend on an ICU version.

const SEP = ","; // thousands separator: "1,418"

function group(int: string): string {
  return int.replace(/\B(?=(\d{3})+(?!\d))/g, SEP);
}

/** A whole number with English grouping: 1,418. */
export function num(n: number): string {
  const r = Math.round(n);
  return (r < 0 ? "-" : "") + group(String(Math.abs(r)));
}

// Under 100 a value keeps one decimal, and drops it when it is ".0": three
// significant digits are enough on a chart legend and a table cell.
function short(v: number): string {
  if (v >= 99.95) return String(Math.round(v)); // at most four digits: no group
  const s = v.toFixed(1);
  return s.endsWith(".0") ? s.slice(0, -2) : s;
}

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

/** Bytes in binary units, 1536 -> "1.5 KB". */
export function bytes(n: number): string {
  let v = Math.max(0, n || 0);
  let i = 0;
  while (v >= 1024 && i < BYTE_UNITS.length - 1) {
    v /= 1024;
    i++;
  }
  return (i === 0 ? String(Math.round(v)) : short(v)) + " " + BYTE_UNITS[i];
}

const RATE_UNITS = ["bit/s", "kbit/s", "Mbit/s", "Gbit/s", "Tbit/s"];

// rateUnit picks the decimal unit for a rate in bits per second.
function rateUnit(bits: number): number {
  let i = 0;
  while (bits >= 1000 && i < RATE_UNITS.length - 1) {
    bits /= 1000;
    i++;
  }
  return i;
}

/** A rate given in bytes per second, said in bits: 39e6 -> "312 Mbit/s". */
export function rate(bytesPerSec: number): string {
  const bits = Math.max(0, bytesPerSec || 0) * 8;
  const i = rateUnit(bits);
  return short(bits / 1000 ** i) + " " + RATE_UNITS[i];
}

/** Rates said in the unit ref (bytes per second) would be said in: "now" beside an average. */
export function ratesIn(ref: number, ...bytesPerSec: number[]): string[] {
  const i = rateUnit(Math.max(0, ref || 0) * 8);
  return bytesPerSec.map((v) => short((Math.max(0, v || 0) * 8) / 1000 ** i));
}

/** Down and up in one unit, chosen by the larger, for "↓ 312 · ↑ 41 Mbit/s". */
export function ratePair(down: number, up: number): { down: string; up: string; unit: string } {
  const d = Math.max(0, down || 0) * 8;
  const u = Math.max(0, up || 0) * 8;
  const i = rateUnit(Math.max(d, u));
  return { down: short(d / 1000 ** i), up: short(u / 1000 ** i), unit: RATE_UNITS[i] };
}

/** A fraction as a whole percent; a sliver above zero is "<1%", not "0%". */
export function share(f: number): string {
  if (!(f > 0)) return "0%";
  if (f < 0.005) return "<1%";
  return Math.round(f * 100) + "%";
}

// One formatter for every clock reading: the router's own zone (zone.ts),
// whatever the browser is set to.
const fmts = new Map<string, Intl.DateTimeFormat>();
function parts(iso: string): Record<string, string> | null {
  if (isZeroTime(iso)) return null;
  const z = zone();
  let f = fmts.get(z);
  if (!f) {
    f = new Intl.DateTimeFormat("en-GB", {
      timeZone: z, year: "numeric", month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
    });
    fmts.set(z, f);
  }
  const out: Record<string, string> = {};
  for (const p of f.formatToParts(new Date(iso))) out[p.type] = p.value;
  return out;
}

/** True for an absent time: empty, unparseable, or Go's zero time. */
export function isZeroTime(iso: string): boolean {
  if (!iso) return true;
  const t = Date.parse(iso);
  return Number.isNaN(t) || t < Date.UTC(1971, 0, 1);
}

/** "21:14" in the router zone. */
export function time(iso: string): string {
  const p = parts(iso);
  return p ? `${p.hour}:${p.minute}` : "-";
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function dm(p: Record<string, string>): string {
  return `${Number(p.day)} ${MONTHS[Number(p.month) - 1] ?? p.month}`;
}

/** "2 Mar" in the router zone. */
export function dayMonth(iso: string): string {
  const p = parts(iso);
  return p ? dm(p) : "-";
}

/** "2 Mar 2026" in the router zone. */
export function date(iso: string): string {
  const p = parts(iso);
  return p ? `${dm(p)} ${p.year}` : "-";
}

/** "20 Nov 2026 21:14:06" in the router zone. */
export function dateTime(iso: string): string {
  const p = parts(iso);
  return p ? `${dm(p)} ${p.year} ${p.hour}:${p.minute}:${p.second}` : "-";
}

const SPANS: [number, string][] = [[86400, "d"], [3600, "h"], [60, "min"], [1, "s"]];

/** Seconds as the two largest units: "2 h 10 min", "12 d 4 h". */
export function duration(sec: number): string {
  const s = Math.max(0, Math.floor(sec || 0));
  for (let i = 0; i < SPANS.length; i++) {
    const [size, name] = SPANS[i];
    if (s < size && size !== 1) continue;
    const q = Math.floor(s / size);
    const next = SPANS[i + 1];
    const q2 = next ? Math.floor((s - q * size) / next[0]) : 0;
    return q2 > 0 ? `${q} ${name} ${q2} ${next[1]}` : `${q} ${name}`;
  }
  return "0 s";
}

/** "5g" -> "5 GHz". */
export function band(b: string): string {
  const m = /^(\d+(?:[.,]\d+)?)\s*(?:g|ghz)$/i.exec((b || "").trim());
  return m ? m[1].replace(",", ".") + " GHz" : b || "";
}

const CATEGORIES: Record<string, string> = {
  video: "Video", social: "Social media", messaging: "Messaging", calls: "Video calls",
  cloud: "Cloud", gaming: "Gaming", music: "Music", ai: "AI assistants", ads: "Ads and trackers",
  shopping: "Shopping", news: "News", travel: "Travel", payments: "Payments",
  smarthome: "Smart home", dev: "Development", doh: "DNS over HTTPS", vpn: "VPN",
  lan: "Local network", other: "Other", unknown: "Unknown",
};

/** A category id in words; an id the console does not know passes through. */
export function category(id: string): string {
  return CATEGORIES[id] ?? id ?? "";
}

/** The classifier's app name; an empty one is "Unknown". */
export function appName(name: string): string {
  return name || "Unknown";
}

let regions: Intl.DisplayNames | null | undefined;
/** A country code as the browser names it in English, or the code itself. */
export function countryName(cc: string): string {
  // Traffic between two devices at home: it never crossed the line.
  if (cc === "LAN") return "Local network";
  if (!/^[A-Z]{2}$/.test(cc || "")) return cc || "";
  if (regions === undefined) {
    try {
      regions = new Intl.DisplayNames(["en"], { type: "region" });
    } catch {
      regions = null;
    }
  }
  try {
    return regions?.of(cc) ?? cc;
  } catch {
    return cc;
  }
}

/** The vendor half of a MAC, for a line with no room for all of it. */
export function macShort(mac: string): string {
  return /^([0-9a-f]{2}:){5}[0-9a-f]{2}$/i.test(mac) ? mac.slice(0, 8).toUpperCase() + ":…" : mac;
}

/** "1 client", "5 clients": the noun agrees with the count. */
export function plural(n: number, one: string, many: string): string {
  return `${num(n)} ${Math.abs(Math.round(n)) === 1 ? one : many}`;
}

/** Milliseconds: "12.5 ms". */
export function ms(v: number): string {
  return short(Math.max(0, v || 0)) + " ms";
}

/** A percent already scaled to 0..100: "2.3%". */
export function pct(v: number): string {
  return short(Math.max(0, v || 0)) + "%";
}

const PROTOS: Record<number, string> = { 6: "tcp", 17: "udp", 1: "icmp", 58: "icmp" };

/** "tcp/443" from a remote "host:port" and an IP protocol number. */
export function port(remote: string, proto: number): string {
  const m = /:(\d+)$/.exec(remote || "");
  const name = PROTOS[proto];
  const p = m && m[1] !== "0" ? m[1] : "";
  if (name && p) return `${name}/${p}`;
  return name ?? String(proto);
}

/** Direction as the arrow the table shows: out is up, from the house. */
export function dirArrow(dir: string): string {
  return dir === "out" ? "↑" : dir === "in" ? "↓" : dir === "local" ? "⇄" : "";
}

const SOURCES: Record<string, string> = {
  sni: "SNI", quic: "QUIC", dns: "DNS", ech: "hidden (ECH)", none: "no name",
};

/** Where a connection's name came from, as the person reads it. */
export function nameSource(src: string): string {
  return SOURCES[src] ?? (src || "");
}

/** A client's name as the console shows it; the router's own traffic is "Router". */
export function clientName(c: { mac: string; name: string }): string {
  if (c.mac === "router") return "Router";
  return c.name || c.mac;
}

/** The "new devices" line: null until the router has watched for a day. */
export function newDevices(n: number | null): string {
  if (n === null) return "shown after a day";
  return `${num(n)} new in 24 h`;
}

/** "15:42:18" in the router zone: when the live card last heard from the router. */
export function clock(iso: string): string {
  const p = parts(iso);
  return p ? `${p.hour}:${p.minute}:${p.second}` : "-";
}

/** The categories a rule may name: every one the console can show except the ones that are not about an app. */
export const BLOCKABLE_CATEGORIES = Object.keys(CATEGORIES).filter((c) => !["unknown", "lan", "other"].includes(c));

/** The name a group is shown by: its own name from the group list, else its id. */
export function groupName(id: string, groups: { id: string; name: string }[]): string {
  return groups.find((g) => g.id === id)?.name || id;
}
