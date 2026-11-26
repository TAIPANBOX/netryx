// Rules as the console shows and sends them: what the API's rule says in a
// sentence, the form that becomes its body, the pause lengths, and the places
// the console returns to after a login. No DOM here, so node tests cover it.

import { appName, category, countryName, dayMonth, time } from "./format";
import { atWall, wall, zone } from "./zone";

/** One rule as GET /api/rules sends it (internal/api ruleJSON). */
export interface Rule {
  id: string;
  enabled: boolean;
  what: string; // country | app | category | all
  targets: string[];
  who: string; // all | device | group
  members: string[];
  group: string;
  days: string[]; // mon..sun
  from: string; // "HH:MM" or ""
  to: string;
  until: number | null; // unix seconds, null without an end
  activeNow: boolean;
  hits: number;
  applied: boolean;
  /** Why the router did not apply it ("" when it did). */
  problem: string;
  /** Why the model refused to read it ("" when it reads). */
  error: string;
  /** A note on a rule that is applied: its country ranges were kept from before (absent when none). */
  note?: string;
  /** The live allowances that lift it now (absent from an older router). */
  allows?: RuleAllow[];
  /** "block" or "limit" (absent from an older router: block). */
  action?: string;
  /** A limit's minutes a day, and today's use per device. */
  minutes?: number;
  used?: Record<string, number>;
}

/** A live allowance as GET /api/rules sends it (internal/api allowJSON). */
export interface RuleAllow {
  id: string;
  member: string;
  until: number; // unix seconds
  by: string; // agent | owner
}

/** The body of POST /api/rules and PUT /api/rules/{id}. */
export interface RuleBody {
  enabled?: boolean;
  what: string;
  targets?: string[];
  who: string;
  members?: string[];
  group?: string;
  days?: string[];
  from?: string;
  to?: string;
  until?: number;
  action?: string;
  minutes?: number;
}

/** What the person fills in. Targets are typed as words; the rest are choices. */
export interface RuleForm {
  enabled?: boolean;
  what: "country" | "app" | "category" | "all";
  targets: string;
  who: "all" | "device" | "group";
  members: string[];
  group: string;
  days: string[];
  from: string;
  to: string;
  /** The end of a pause, carried through an edit so it is not lost. */
  until?: number;
  /** Block, or a time limit of minutes a day. */
  action?: "block" | "limit";
  minutes?: string;
}

export interface Names {
  app?: (id: string) => string;
  device?: (mac: string) => string;
  group?: (id: string) => string;
}

export const DAYS: { id: string; short: string }[] = [
  { id: "mon", short: "Mon" },
  { id: "tue", short: "Tue" },
  { id: "wed", short: "Wed" },
  { id: "thu", short: "Thu" },
  { id: "fri", short: "Fri" },
  { id: "sat", short: "Sat" },
  { id: "sun", short: "Sun" },
];

/** "Mon-Fri", "Mon, Sat-Sun", "every day": runs of three days or more are written as a range. */
export function daysText(days: string[]): string {
  const at = DAYS.map((d) => d.id).filter((id) => days.includes(id));
  if (at.length === 0) return "";
  if (at.length === 7) return "every day";
  const idx = at.map((id) => DAYS.findIndex((d) => d.id === id));
  const out: string[] = [];
  for (let i = 0; i < idx.length; ) {
    let j = i;
    while (j + 1 < idx.length && idx[j + 1] === idx[j] + 1) j++;
    if (j - i >= 2) out.push(`${DAYS[idx[i]].short}-${DAYS[idx[j]].short}`);
    else for (let k = i; k <= j; k++) out.push(DAYS[idx[k]].short);
    i = j + 1;
  }
  return out.join(", ");
}

/** "21:00-07:00" etc.; the end of a pause is shown in the router zone like every time here. */
export function untilText(unix: number): string {
  const iso = new Date(unix * 1000).toISOString();
  return `${dayMonth(iso)} ${time(iso)}`;
}

function whatText(r: Rule, n: Names): string {
  switch (r.what) {
    case "country":
      return r.targets.map((c) => countryName(c)).join(", ");
    case "app":
      return r.targets.map((a) => (n.app ? n.app(a) : appName(a))).join(", ");
    case "category":
      return r.targets.map((c) => category(c)).join(", ");
    case "all":
      return "the whole internet";
    default:
      return r.what || "?";
  }
}

function whoText(r: Rule, n: Names): string {
  switch (r.who) {
    case "all":
      return "all devices";
    case "device": {
      const names = r.members.map((m) => (n.device ? n.device(m) : m));
      return names.length > 3 ? `${names.slice(0, 3).join(", ")} and ${names.length - 3} more` : names.join(", ");
    }
    case "group":
      return `group "${n.group ? n.group(r.group) : r.group}"`;
    default:
      return r.who || "?";
  }
}

function whenText(r: Rule): string {
  const parts: string[] = [];
  const d = daysText(r.days);
  if (d) parts.push(d);
  if (r.from && r.to) parts.push(`${r.from}-${r.to}`);
  if (r.until) parts.push(`until ${untilText(r.until)}`);
  return parts.length ? parts.join(" ") : "always";
}

/** "TikTok, YouTube · Alex's iPhone · Mon-Fri 21:00-07:00": what, who, when. */
export function describeRule(r: Rule, names: Names = {}): string {
  return [whatText(r, names), whoText(r, names), ruleWhen(r)].join(" · ");
}

/** A pause is the rule the Pause button writes: everything, for one device, with an end or without. */
export function isPause(r: Rule): boolean {
  return r.what === "all" && r.who === "device" && r.days.length === 0 && !r.from && !r.to;
}

/** The agent writes its rules in sections named agent_*. */
export function fromAgent(r: Rule): boolean {
  return r.id.startsWith("agent_");
}

export type RuleState = { kind: "active" | "waiting" | "off" | "failed"; text: string };

/** The state line: active now, waiting for its time, off: <reason>, not applied: <reason>. */
export function stateOf(r: Rule, nowMs: number): RuleState {
  if (r.error) return { kind: "off", text: `off: ${r.error}` };
  if (!r.enabled) return { kind: "off", text: "off: turned off" };
  if (r.until && r.until * 1000 <= nowMs) return { kind: "off", text: "off: its time has ended" };
  if (r.problem) return { kind: "failed", text: r.problem.startsWith("not applied") ? r.problem : `not applied: ${r.problem}` };
  const note = r.note ? ` · ${r.note}` : "";
  if (r.activeNow) return { kind: "active", text: `active now${note}` };
  return { kind: "waiting", text: `waiting for its time${note}` };
}

/** The rules form for a rule that exists, for editing. */
export function formFromRule(r: Rule): RuleForm {
  return {
    enabled: r.enabled,
    what: (["country", "app", "category", "all"].includes(r.what) ? r.what : "app") as RuleForm["what"],
    targets: r.targets.join(", "),
    who: (["all", "device", "group"].includes(r.who) ? r.who : "all") as RuleForm["who"],
    members: [...r.members],
    group: r.group,
    days: [...r.days],
    from: r.from,
    to: r.to,
    ...(r.until ? { until: r.until } : {}),
    ...(r.action === "limit" ? { action: "limit" as const, minutes: String(r.minutes ?? "") } : {}),
  };
}

/** The API's rule body for a form: countries in capitals, apps in lower case, only the fields the choices need. */
export function ruleFromForm(f: RuleForm): RuleBody {
  const b: RuleBody = { what: f.what, who: f.who };
  if (f.enabled !== undefined) b.enabled = f.enabled;
  if (f.what !== "all") {
    const words = f.targets.split(/[\s,;]+/).filter(Boolean);
    b.targets = words.map((w) => (f.what === "country" ? w.toUpperCase() : w.toLowerCase()));
  }
  if (f.who === "device") b.members = f.members.map((m) => m.toLowerCase());
  if (f.who === "group") b.group = f.group;
  if (f.days.length > 0) b.days = DAYS.map((d) => d.id).filter((id) => f.days.includes(id));
  if (f.from && f.to) {
    b.from = f.from;
    b.to = f.to;
  }
  if (f.until) b.until = f.until;
  if (f.action === "limit") {
    b.action = "limit";
    b.minutes = Number(f.minutes);
    delete b.from;
    delete b.to;
  }
  return b;
}

/** The first thing wrong with a form, in words, or "" when it can be sent. */
export function formProblem(f: RuleForm): string {
  if (f.action === "limit") {
    const n = Number(f.minutes);
    if (!Number.isInteger(n) || n < 1 || n > 1440) return "Time limit: 1 to 1440 minutes a day.";
    if (f.what !== "app" && f.what !== "category") return "A time limit is only for an app or a category.";
    if (f.from || f.to) return "A time limit counts the whole day: no hours are needed.";
  }
  if (f.what !== "all" && f.targets.split(/[\s,;]+/).filter(Boolean).length === 0) {
    return f.what === "country" ? "Name the countries, for example RU, CN." : "Say what to block.";
  }
  if (f.what === "country" && !f.targets.split(/[\s,;]+/).filter(Boolean).every((w) => /^[A-Za-z]{2}$/.test(w))) {
    return "Countries are written as two letters: RU, CN.";
  }
  if (f.who === "device" && f.members.length === 0) return "Pick a device.";
  if (f.who === "group" && !f.group) return "Pick a group.";
  if (!!f.from !== !!f.to) return "A time needs both a start and an end.";
  return "";
}

/** The pause lengths the device card offers. */
export type PauseKind = "1h" | "07:00" | "forever";

/**
 * The end of a pause as unix seconds, or undefined for none: an hour from now,
 * or the next 07:00 on the router's clock (today's if it is still ahead).
 */
export function pauseUntil(kind: PauseKind, now: Date, z: string = zone()): number | undefined {
  if (kind === "forever") return undefined;
  if (kind === "1h") return Math.floor(now.getTime() / 1000) + 3600;
  const w = wall(now.getTime(), z);
  let t = atWall(w.y, w.mo, w.d, 7, 0, z);
  if (t <= now.getTime()) t = atWall(w.y, w.mo, w.d + 1, 7, 0, z);
  return Math.floor(t / 1000);
}

/** The body of the rule the Pause button creates for one device. */
export function pauseBody(mac: string, kind: PauseKind, now: Date, z: string = zone()): RuleBody {
  const b: RuleBody = { what: "all", who: "device", members: [mac.toLowerCase()] };
  const u = pauseUntil(kind, now, z);
  if (u !== undefined) b.until = u;
  return b;
}

/**
 * Whether a rule reaches the device: for everyone, or naming its MAC. A group
 * rule's members are the router's expansion of the group in UCI, so a group
 * that only labels.json gives the device does not count.
 */
export function appliesTo(r: Rule, mac: string): boolean {
  if (r.who === "all") return true;
  return (r.who === "device" || r.who === "group") && r.members.includes(mac.toLowerCase());
}

/** The allowances of a rule that still run, for one device when mac is given. */
export function liveAllows(r: Rule, nowMs: number, mac?: string): RuleAllow[] {
  const m = mac?.toLowerCase();
  return (r.allows ?? []).filter((a) => a.until * 1000 > nowMs && (!m || a.member === m));
}

/** "allowed until 21:30", and ", by the agent" when the router agent gave it. */
export function allowText(a: RuleAllow): string {
  const t = time(new Date(a.until * 1000).toISOString());
  return `allowed until ${t}${a.by === "agent" ? ", by the agent" : ""}`;
}

/** The pause that holds the device now, if one does: the device card then offers to lift it, not another. */
export function activePause(rules: Rule[] | undefined, mac: string, nowMs: number): Rule | undefined {
  const m = mac.toLowerCase();
  return (rules ?? []).find((r) => isPause(r) && r.members.includes(m) && r.enabled && (!r.until || r.until * 1000 > nowMs));
}

/** The Blocked tile: the sum of the rules' counters, which run from the start of the rules, not per day. */
export function blockedTile(rules: Rule[] | undefined): { value: string; sub: string } {
  if (!rules) return { value: "-", sub: "no data" };
  if (rules.length === 0) return { value: "-", sub: "no rules" };
  const n = rules.reduce((a, r) => a + (r.hits || 0), 0);
  return { value: new Intl.NumberFormat("en-GB").format(n), sub: "since the rules started" };
}

/**
 * Where to return after a login: the screen a 401 interrupted. Only a hash of
 * this page is taken; anything else (a full URL, a script, a huge string)
 * returns to the overview.
 */
export function nextLogin(hash: string): string {
  return /^#\/[^\u0000-\u001f\u007f]{0,511}$/.test(hash) ? hash : "#/";
}

/** What the login says when the page came over plain http: the password then crosses the house network unencrypted. */
export function loginWarning(protocol: string): string {
  return protocol === "http:"
    ? "This page was opened over http: the password will cross the house network unencrypted. Open it over https instead."
    : "";
}

/** An app's display name from the icon manifest the page loads; the id when the manifest does not know it. */
export function manifestName(m: Record<string, { name?: string }> | null | undefined): (id: string) => string {
  return (id) => (m && Object.prototype.hasOwnProperty.call(m, id) && m[id].name) || id;
}

export interface AppChoice { id: string; name: string }

/**
 * The apps a new rule can name: the icon manifest's, plus every app id the
 * console already knows from traffic (a build without an icon pack has no
 * manifest names, and a rule must still be possible). The manifest's name wins;
 * unnamed ids show as themselves; catch-all ids are never offered.
 */
export function appChoices(manifest: Record<string, { name?: string }> | null | undefined, known: { id: string; name?: string }[]): AppChoice[] {
  const skip = new Set(["ads", "other", "unknown"]);
  const out = new Map<string, AppChoice>();
  for (const [id, m] of Object.entries(manifest ?? {})) if (!skip.has(id) && m.name) out.set(id, { id, name: m.name });
  for (const k of known) if (k.id && !skip.has(k.id) && !out.has(k.id)) out.set(k.id, { id: k.id, name: k.name || k.id });
  return [...out.values()].sort((a, b) => a.name.localeCompare(b.name));
}


/** One limit rule of a device today, as GET /api/usage sends it: the sum of
 * its apps' minutes, which is what blocks, and the apps that were used. */
export interface Usage {
  rule?: string;
  what?: string;
  targets?: string[];
  used: number;
  limit: number;
  spentUntil: number | null;
  apps?: { app: string; used: number }[];
}

/** "Social media (TikTok 40 min, Instagram 20 min)": what the rule limits, then the
 * apps that used its minutes when they are not just the rule's one app. */
export function usageLabel(u: Usage, appName: (id: string) => string): string {
  const targets = (u.targets ?? []).map((t) => (u.what === "category" ? category(t) : appName(t))).join(", ");
  const apps = u.apps ?? [];
  if (apps.length === 0 || (u.what === "app" && apps.length === 1 && (u.targets ?? []).length === 1)) return targets;
  return `${targets} (${apps.map((a) => `${appName(a.app)} ${a.used} min`).join(", ")})`;
}

/** "used 12 of 60 min", or "used up until 00:00" once the minutes are used up. */
export function usageText(u: Usage, z: string = zone()): string {
  if (u.spentUntil) {
    const w = wall(u.spentUntil * 1000, z);
    return `used up until ${String(w.h).padStart(2, "0")}:${String(w.mi).padStart(2, "0")}`;
  }
  return `used ${u.used} of ${u.limit} min`;
}

/** What a rule does, as its links on the Rules screen begin. */
export function ruleVerb(r: Rule): string {
  if (r.action === "limit") return `limits to ${r.minutes ?? 0} min a day`;
  return r.what === "all" ? "blocks the whole internet" : "blocks";
}

/** The tail of describeRule: when the rule holds, and a limit's minutes. */
export function ruleWhen(r: Rule): string {
  if (r.action === "limit") {
    const d = daysText(r.days);
    return [...(d ? [d] : []), `${r.minutes ?? 0} min a day`].join(" · ");
  }
  return whenText(r);
}
