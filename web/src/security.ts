// The Security panel's data: the owner's alerts (spec 6.9), the devices in
// quarantine (6.8), and how alerts reach the owner. No DOM here, so node tests
// cover it.

import { bytes, countryName, date, time } from "./format";

export interface Alert { id: string; kind: string; mac?: string; country?: string; bytes?: number; count?: number; at: string; sent?: string; problem?: string }
export interface AlertSettings { newDevice: boolean; blockedCountry: boolean; upload: boolean; channel: "telegram" | "console"; quarantine: boolean }
export interface QuarantineState { active: boolean; graceEnds: number | null; problem: string }
export interface AlertsState { alerts: Alert[]; telegram: { ready: boolean; problem: string }; countryTriesSeen: boolean; settings: AlertSettings; quarantineState?: QuarantineState }

/** A device as the panel needs it: quarantined ones carry a hint. */
export interface HeldDevice { mac: string; hostname: string; likelySame?: string }

function who(mac: string | undefined, name: (mac: string) => string): string {
  return (mac && name(mac)) || mac || "";
}

/** "12:05 New device on the network: Camera", one line in the router zone. */
export function alertText(a: Alert, name: (mac: string) => string): string {
  const w = who(a.mac, name);
  let t: string;
  switch (a.kind) {
    case "new_device":
      t = `New device on the network: ${w}`;
      break;
    case "blocked_country":
      t = `Connection attempt to blocked country ${countryName(a.country ?? "")}: ${w}`;
      break;
    case "upload":
      t = `Unusually large upload: ${w}, ${bytes(a.bytes ?? 0)} in an hour`;
      break;
    case "quarantine_full":
      t = "Too many new devices: the quarantine list is full";
      break;
    case "new_devices":
      t = `Many new devices at once: ${a.count ?? 0}, none can reach the house`;
      break;
    case "quarantine_off":
      t = "Quarantine is off: the list of known devices cannot be read";
      break;
    case "baseline_rebuilt":
      t = "The list of known devices was rebuilt: for 24 hours new devices join it without quarantine";
      break;
    default:
      t = a.kind;
  }
  return `${time(a.at)} ${t}`;
}

/** A held device's line. The likely device comes from the hostname the
 * device gives itself, so it is said to be a hint, not a fact. */
export function pendingText(d: HeldDevice, name: (mac: string) => string): string {
  const head = d.hostname ? `${d.hostname} (${d.mac})` : d.mac;
  if (!d.likelySame) return head;
  return `${head}. It may be ${who(d.likelySame, name)} with a new address: the name the device gives itself says so`;
}

/** How alerts reach the owner, beside the list. */
export function telegramText(s: AlertsState): string {
  return s.telegram.ready ? "Telegram: alerts go to the Hermes bot" : `Telegram: ${s.telegram.problem}`;
}

/** The PUT /api/alerts body; an unknown channel is sent as telegram. */
export function alertsBody(s: AlertSettings): Omit<AlertSettings, "quarantine"> {
  return { newDevice: !!s.newDevice, blockedCountry: !!s.blockedCountry, upload: !!s.upload, channel: s.channel === "console" ? "console" : "telegram" };
}

/** The overview's Alerts tile. */
export function alertsTile(s: AlertsState | undefined, now: number): { value: string; sub: string } {
  if (!s) return { value: "-", sub: "no data" };
  const day = s.alerts.filter((a) => now - Date.parse(a.at) <= 24 * 3600 * 1000);
  const unsent = day.filter((a) => !a.sent).length;
  return { value: String(day.length), sub: unsent ? `in 24 h · ${unsent} not sent` : "in 24 h" };
}

/** What the quarantine does now (spec 3: never shown as holding when it does not). */
export function quarantineText(s: AlertsState): string {
  const q = s.quarantineState;
  if (!q) return "";
  if (q.active) return "Quarantine is on: new devices cannot reach the house until you approve them";
  if (q.graceEnds && !q.problem) {
    const iso = new Date(q.graceEnds * 1000).toISOString();
    return `First day: new devices join the known ones, quarantine starts at ${time(iso)} on ${date(iso)}`;
  }
  return `Quarantine is off: ${q.problem}`;
}
