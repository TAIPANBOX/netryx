import { test } from "node:test";
import assert from "node:assert/strict";
import { invalidAllowsNote } from "./allows";

test("allowances that open nothing are named with why, never trusted", () => {
  const dev = (m: string) => (m === "02:4e:58:00:00:03" ? "Sam's phone" : m.toUpperCase());
  assert.deepEqual(invalidAllowsNote([], dev), []);
  assert.deepEqual(invalidAllowsNote([{ id: "a_old", member: "02:4e:58:00:00:03", why: "it names no rule: write rule '*' for every rule, or a rule's name" }], dev), [
    "1 allowance in the config opens nothing:",
    "a_old for Sam's phone: it names no rule: write rule '*' for every rule, or a rule's name.",
  ]);
  assert.equal(invalidAllowsNote([{ id: "a", member: "", why: "x" }, { id: "b", member: "kids", why: "its member is not a hardware address" }], dev)[0],
    "2 allowances in the config open nothing:");
  assert.equal(invalidAllowsNote([{ id: "b", member: "kids", why: "its member is not a hardware address" }], dev)[1],
    "b for KIDS: its member is not a hardware address.");
  assert.equal(invalidAllowsNote([{ id: "a", member: "", why: "x" }], dev)[1], "a: x.");
});
