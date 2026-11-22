// A stretch of time picked on a chart: kept in the address (span=<from>~<to>),
// so Back undoes it and a link carries it, and sent to the API as from and to,
// which replace the period for the series and every list on the screen.

import type { Range } from "./types";
import { RANGES } from "./query";
import { dayMonth, time } from "./format";

export interface Span { from: string; to: string }

// A whole RFC 3339 time in UTC, nothing else: it goes into the API's query.
const ISO = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z$/;

function split(hash: string): [string, URLSearchParams] {
  const raw = hash.replace(/^#/, "");
  const i = raw.indexOf("?");
  return i < 0 ? [raw, new URLSearchParams()] : [raw.slice(0, i), new URLSearchParams(raw.slice(i + 1))];
}

/** The stretch an address names, or null when it names none or a bad one. */
export function parseSpan(hash: string): Span | null {
  const v = split(hash)[1].get("span");
  if (!v) return null;
  const [from, to, extra] = v.split("~");
  if (extra !== undefined || !ISO.test(from ?? "") || !ISO.test(to ?? "")) return null;
  return Date.parse(from) < Date.parse(to) ? { from, to } : null;
}

/** The address with the stretch set, or taken out when s is null. */
export function withSpan(hash: string, s: Span | null): string {
  const [path, qs] = split(hash);
  qs.delete("span");
  // URLSearchParams would escape the colons and the tilde; the address keeps them readable.
  const rest = qs.toString();
  const parts = [rest, s ? `span=${s.from}~${s.to}` : ""].filter(Boolean);
  return `#${path || "/"}${parts.length ? "?" + parts.join("&") : ""}`;
}

/** The query that asks the API for the period, or for the stretch when there is one. */
export function periodQuery(r: Range, s: Span | null): string {
  return s ? `range=${r}&from=${encodeURIComponent(s.from)}&to=${encodeURIComponent(s.to)}` : `range=${r}`;
}

/** The seconds a series step covers: the router reads a stretch at the step of the shortest range that holds it. */
export function periodStep(r: Range, s: Span | null): number {
  const step = (id: Range) => RANGES.find((x) => x.id === id)?.step ?? 600;
  if (!s) return step(r);
  const sec = (Date.parse(s.to) - Date.parse(s.from)) / 1000;
  const fit = RANGES.find((x) => sec <= { "1h": 3600, "24h": 86400, "7d": 604800, "30d": Infinity }[x.id]);
  return fit ? fit.step : step("30d");
}

/** The period whose step a stretch is drawn at: what the axis labels follow. */
export function periodRange(r: Range, s: Span | null): Range {
  const step = periodStep(r, s);
  return RANGES.find((x) => x.step === step)?.id ?? r;
}

/** "22 Nov 09:00 to 10:00", with the second date only when the stretch crosses midnight. */
export function spanLabel(s: Span): string {
  const a = dayMonth(s.from);
  const b = dayMonth(s.to);
  return `${a} ${time(s.from)} to ${a === b ? "" : b + " "}${time(s.to)}`;
}

/** The stretch from the first column brushed to the end of the last. */
export function brushSpan(t: string[], stepSec: number, a: number, b: number): Span | null {
  if (t.length === 0) return null;
  const hold = (i: number) => Math.min(t.length - 1, Math.max(0, i));
  const i = hold(Math.min(a, b));
  const j = hold(Math.max(a, b));
  const from = Date.parse(t[i]);
  const end = Date.parse(t[j]);
  if (Number.isNaN(from) || Number.isNaN(end)) return null;
  const iso = (ms: number) => new Date(ms).toISOString().replace(".000Z", "Z");
  return { from: iso(from), to: iso(end + stepSec * 1000) };
}

/** Sets the stretch in the address: a new history entry, so Back undoes it, without starting the screen afresh. */
export function setSpan(s: Span | null) {
  history.pushState(null, "", withSpan(location.hash, s));
  window.dispatchEvent(new Event("netryx-span"));
}
