// A device's interventions, for the client's full view: who let it through,
// which limit ran out, who let it out of quarantine or isolated it, which
// rule blocked it and who cut its connection. Loaded with that view only.

export interface Intervention { at: string; kind: string; rule?: string; name?: string; by?: string; until?: string; detail?: string; group?: string }
export interface Interventions { on: boolean; since?: string; skipped: number; entries: Intervention[] }

const WHO: Record<string, string> = { owner: "The owner", agent: "The agent (Hermes)", router: "The router" };
const whose = (by?: string) => (by === "agent" ? "The agent's" : by === "router" ? "The router's" : "The owner's");

/** One intervention as a sentence; clock formats a time of day. */
export function interventionText(e: Intervention, clock: (iso: string) => string): string {
  const who = WHO[e.by ?? ""] ?? "Someone";
  const on = e.name ? `the rule on ${e.name}` : "a rule";
  switch (e.kind) {
    case "allow": {
      const whom = e.group ? `its group ${e.group}` : "it";
      const what = e.rule ? on : "every rule";
      return `${who} let ${whom} through ${what}${e.until ? ` until ${clock(e.until)}` : ""}.`;
    }
    case "limit_spent":
      return `Its time limit${e.name ? ` on ${e.name}` : ""} ran out${e.until ? `: blocked until ${clock(e.until)}` : ""}.`;
    case "blocked":
      return `${whose(e.by)} rule${e.name ? ` on ${e.name}` : ""} blocked a connection.`;
    case "approved":
      return `${who} let it out of quarantine.`;
    case "isolated":
      return `${who} isolated it.`;
    case "released":
      return `${who} ended its isolation.`;
    case "cut":
      return `${who} cut a connection${e.detail ? `: ${e.detail}` : ""}.`;
  }
  return `${who}: ${e.kind}.`;
}

/** The line above the list: whether a journal is kept, since when, and how
 *  many of its lines could not be read. */
export function interventionsNote(j: Interventions, day: (iso: string) => string): string {
  if (!j.on) return "This router keeps no journal of interventions.";
  const since = j.since ? day(j.since) : "the journal began";
  if (j.entries.length === 0) return `No intervention since ${since}.`;
  const bad = j.skipped > 0 ? ` (${j.skipped} damaged line${j.skipped === 1 ? "" : "s"} of the journal could not be read)` : "";
  return `Since ${since}, newest first${bad}:`;
}
