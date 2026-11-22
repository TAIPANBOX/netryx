// The house-wide Apps and Destinations screens: which rules close an app or a
// country, how a row names its clients, the search and the links. No DOM here,
// so node tests cover it.

import { manifestName, stateOf, type Names, type Rule } from "./rules";
import type { ClientRef, HouseApp } from "./types";
import { appName, category, clientName, groupName, plural } from "./format";

/** The rules that name this app, or its category. */
export function rulesForApp(rules: Rule[] | undefined, app: { id: string; category: string }): Rule[] {
  return (rules ?? []).filter(
    (r) => (r.what === "app" && r.targets.includes(app.id)) || (r.what === "category" && !!app.category && r.targets.includes(app.category)),
  );
}

/** The country rules that name this country. */
export function rulesForCountry(rules: Rule[] | undefined, cc: string): Rule[] {
  const up = cc.toUpperCase();
  return (rules ?? []).filter((r) => r.what === "country" && r.targets.includes(up));
}

/** "Laptop, Phone and 3 more": the first clients of a row by name, then how many others. */
export function clientsLine(n: number, top: ClientRef[]): string {
  const named = top.filter((c) => !c.other).map((c) => clientName(c));
  if (n === 0 || named.length === 0) {
    // Traffic with no client behind it: the router's own, or from addresses
    // nobody could give a hardware address to.
    const whose = [];
    if (top.some((c) => c.mac === "router")) whose.push("the router itself");
    if (top.some((c) => c.other && c.mac !== "router" && c.mac !== "other")) whose.push("devices without a hardware address");
    return whose.length ? whose.join(" and ") : "no clients";
  }
  const rest = n - named.length;
  return named.join(", ") + (rest > 0 ? ` and ${rest} more` : "");
}

/** The search on the Apps screen: every word in the name, the category id or its name. */
export function matchApp(a: HouseApp, q: string): boolean {
  const hay = [appName(a.app.name), a.app.id, a.app.category, category(a.app.category)].join(" ").toLowerCase();
  return q.toLowerCase().split(/\s+/).filter(Boolean).every((w) => hay.includes(w));
}

/** The categories present in a list, the one that moved the most bytes first. */
export function categoriesIn(list: HouseApp[]): string[] {
  const sum = new Map<string, number>();
  for (const a of list) {
    if (a.other || !a.app.category) continue;
    sum.set(a.app.category, (sum.get(a.app.category) ?? 0) + a.down + a.up);
  }
  return [...sum.entries()].sort((x, y) => y[1] - x[1] || (x[0] < y[0] ? -1 : 1)).map(([c]) => c);
}

/** Where an app row leads: its own screen, or nowhere for the folded rest. */
export function appHref(a: HouseApp): string | undefined {
  return a.other ? undefined : `#/app/${encodeURIComponent(a.app.id)}`;
}

/** Where a country leads: its own screen, or nowhere for the unknown rest. */
export function countryHref(cc: string): string | undefined {
  return /^[A-Z]{2}$/.test(cc) ? `#/destination/${cc}` : undefined;
}

/** A domain row's name: the folded rest, a connection nobody named, or the name. */
export function domainText(d: { name: string; other?: boolean }): string {
  if (d.other) return "other names";
  return d.name || "no name";
}

/** Whether a rule is in force for the whole house now: turned on, not ended, for everyone. */
function forHouse(r: Rule, nowMs: number): boolean {
  return r.enabled && !r.error && r.who === "all" && !(r.until && r.until * 1000 <= nowMs);
}

/** Whether these rules already close the app or country for everyone: then no block button. */
export function closesHouse(rules: Rule[], nowMs: number): boolean {
  return rules.some((r) => forHouse(r, nowMs));
}

/** A rule's tag: what it does, for whom when not everyone, and its state when not in force. */
export function ruleTagText(r: Rule, nowMs: number): string {
  let word = r.action === "limit" ? "time limit" : "blocked";
  if (r.who === "device") word += ` for ${plural(r.members.length, "device", "devices")}`;
  else if (r.who === "group") word += " for a group";
  const st = stateOf(r, nowMs);
  return st.kind === "active" ? word : `${word}, ${st.text}`;
}

/** The Apps toolbar's count: a list cut at its bound is the busiest of a longer one. */
export function appsSummary(n: number, max: number): string {
  return n >= max ? `the ${max} busiest apps` : plural(n, "app", "apps");
}

/** The names a rule's tag uses: a device by the name the console shows, a group and an app by theirs. */
export function ruleNames(
  devices: { mac: string; name: string; label: string; hostname: string }[],
  groups: { id: string; name: string }[],
  manifest: Record<string, { name?: string }> | null | undefined,
): Names {
  const dev = new Map(devices.map((d) => [d.mac.toLowerCase(), d.label || d.name || d.hostname]));
  return {
    device: (m) => dev.get(m.toLowerCase()) || m,
    group: (id) => groupName(id, groups),
    app: manifestName(manifest),
  };
}
