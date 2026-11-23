import { test } from "node:test";
import assert from "node:assert/strict";
import { interventionText, interventionsNote, type Intervention } from "./interventions";

const hm = (iso: string) => iso.slice(11, 16);
const iv = (p: Partial<Intervention>): Intervention => ({ at: "2026-11-20T18:40:00Z", kind: "blocked", ...p });

test("each intervention says who did what, which rule, and until when", () => {
  assert.equal(interventionText(iv({ kind: "allow", by: "agent", rule: "games", name: "roblox, minecraft", until: "2026-11-20T21:30:00Z" }), hm),
    "The agent (Hermes) let it through the rule on roblox, minecraft until 21:30.");
  assert.equal(interventionText(iv({ kind: "allow", by: "owner", rule: "", until: "2026-11-20T21:30:00Z", group: "kids" }), hm),
    "The owner let its group kids through every rule until 21:30.");
  assert.equal(interventionText(iv({ kind: "limit_spent", by: "router", rule: "gl", name: "roblox", until: "2026-11-21T00:00:00Z" }), hm),
    "Its time limit on roblox ran out: blocked until 00:00.");
  assert.equal(interventionText(iv({ kind: "blocked", by: "agent", rule: "agent_tt", name: "tiktok" }), hm), "The agent's rule on tiktok blocked a connection.");
  assert.equal(interventionText(iv({ kind: "blocked", by: "owner", rule: "cc", name: "CN, RU" }), hm), "The owner's rule on CN, RU blocked a connection.");
  assert.equal(interventionText(iv({ kind: "approved", by: "owner" }), hm), "The owner let it out of quarantine.");
  assert.equal(interventionText(iv({ kind: "isolated", by: "owner" }), hm), "The owner isolated it.");
  assert.equal(interventionText(iv({ kind: "released", by: "owner" }), hm), "The owner ended its isolation.");
  assert.equal(interventionText(iv({ kind: "cut", by: "owner", detail: "tcp 149.154.167.50:443 (telegram)" }), hm), "The owner cut a connection: tcp 149.154.167.50:443 (telegram).");
});

test("the journal says when it keeps nothing, since when it counts, and what it could not read", () => {
  const d = (iso: string) => iso.slice(0, 10);
  assert.equal(interventionsNote({ on: false, skipped: 0, entries: [] }, d), "This router keeps no journal of interventions.");
  assert.equal(interventionsNote({ on: true, since: "2026-11-18T12:00:00Z", skipped: 0, entries: [] }, d), "No intervention since 2026-11-18.");
  assert.equal(interventionsNote({ on: true, since: "2026-11-18T12:00:00Z", skipped: 0, entries: [iv({})] }, d), "Since 2026-11-18, newest first:");
  assert.equal(interventionsNote({ on: true, since: "2026-11-18T12:00:00Z", skipped: 2, entries: [iv({})] }, d),
    "Since 2026-11-18, newest first (2 damaged lines of the journal could not be read):");
});
