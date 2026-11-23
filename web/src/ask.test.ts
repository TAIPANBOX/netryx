import { test } from "node:test";
import assert from "node:assert/strict";
import { askFooter, askRetry } from "./ask";

test("an answer says it is the model's, what it cost and what is left", () => {
  assert.equal(askFooter({ ok: true, answer: "x", costUsd: 0.0031, asksLeft: 9, budgetLeft: 0.4969 }),
    "Written by the model behind the router's agent · cost $0.0031 · 9 questions left this hour · $0.50 left today");
  // What the agent does not say is left out, never shown as 0.
  assert.equal(askFooter({ ok: true, answer: "x" }), "Written by the model behind the router's agent");
  assert.equal(askFooter({ ok: true, answer: "x", asksLeft: 1 }), "Written by the model behind the router's agent · 1 question left this hour");
});

test("a refusal says when to try again, in minutes past a minute", () => {
  assert.equal(askRetry(0), "");
  assert.equal(askRetry(45), "Try again in 45 s.");
  assert.equal(askRetry(60), "Try again in 1 min.");
  assert.equal(askRetry(701), "Try again in 12 min.");
});
