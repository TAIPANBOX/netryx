// The house's lines over the last 30 days, for the Network screen: what the
// backup line carried on the days it was used, set beside what the lines
// carried then. Kept apart from network.ts, which the first screen imports.

import { bytes } from "./format";

export interface Line { name: string; proto: string; device: string; up: boolean }
export interface LineDay { date: string; primary: number | null; backup: number | null }
export interface Lines {
  primary: Line[]; backup: Line[]; days: LineDay[]; usedDays: number; usedMin: number;
  backupBytes: number; share: number | null; busiest: LineDay | null; unknownDays: number;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const dayOf = (date: string) => `${Number(date.slice(8, 10))} ${MONTHS[Number(date.slice(5, 7)) - 1]}`;
const MODEM = new Set(["qmi", "mbim", "ncm", "modemmanager", "3g", "directip", "wwan"]);
const lineName = (l: Line) => `${l.name}, ${MODEM.has(l.proto) ? "a mobile modem" : "a second uplink"}`;
// A list of items that carry commas themselves takes one before its "and".
const and = (xs: string[]) =>
  xs.length < 2 ? xs.join("") : `${xs.slice(0, -1).join(", ")}${xs.some((x) => x.includes(",")) ? "," : ""} and ${xs[xs.length - 1]}`;

/** What the backup line did this month: on how many days it carried traffic,
 *  how much, and its share of what the lines carried on those days; a day
 *  that cannot be known is counted apart, never as a quiet one. */
export function linesSentences(l: Lines): string[] {
  if (l.backup.length === 0) {
    return [l.primary.length ? `No backup line: the only internet line is ${and(l.primary.map((p) => p.name))}.` : "No backup line, and the configured internet line was not found."];
  }
  const who = `backup line${l.backup.length === 1 ? "" : "s"} ${and(l.backup.map(lineName))}`;
  const known = l.days.length - l.unknownDays;
  let first: string;
  if (l.usedDays === 0) {
    first = `The ${who}, carried no traffic in the last ${known} days known (under ${bytes(l.usedMin)} a day, which its checks move).`;
  } else {
    const those = l.usedDays === 1 ? "that day" : "those days";
    const share = l.share === null ? `; what the primary line carried on ${those} is not known` : `, ${Math.round(l.share * 100)}% of what the lines carried on ${those}`;
    first = `On ${l.usedDays} of the last ${known} days known the ${who}, carried traffic: ${bytes(l.backupBytes)}${share}.`;
    if (l.busiest && l.busiest.backup !== null) first += ` The most on ${dayOf(l.busiest.date)}: ${bytes(l.busiest.backup)}.`;
  }
  const out = [first];
  if (l.unknownDays === 1) out.push("1 day is not known: no close before it, the router off for part of it, or a counter restarted.");
  else if (l.unknownDays > 1) out.push(`${l.unknownDays} days are not known: no close before them, the router off for part of them, or a counter restarted.`);
  return out;
}

/** The backup line's days as bars, oldest first: the busiest sets the scale,
 *  a day it was used apart from a day of its checks, an unknown day no bar. */
export function lineBars(l: Lines): { date: string; height: number | null; used: boolean; title: string }[] {
  const peak = Math.max(0, ...l.days.map((d) => d.backup ?? 0));
  return l.days.map((d) => {
    if (d.backup === null) return { date: d.date, height: null, used: false, title: `${dayOf(d.date)}: not known` };
    const prim = d.primary === null ? "the primary not known" : `${bytes(d.primary)} on the primary`;
    return { date: d.date, height: peak > 0 ? d.backup / peak : 0, used: d.backup >= l.usedMin, title: `${dayOf(d.date)}: ${bytes(d.backup)} on the backup, ${prim}` };
  });
}
