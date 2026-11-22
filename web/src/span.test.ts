import { test } from "node:test";
import assert from "node:assert/strict";
import { brushSpan, parseSpan, periodQuery, periodStep, withSpan } from "./span";

const A = "2026-11-22T09:00:00Z";
const B = "2026-11-22T10:00:00Z";

test("a stretch is read from the address, and nothing else is one", () => {
  assert.deepEqual(parseSpan(`#/?span=${A}~${B}`), { from: A, to: B });
  assert.deepEqual(parseSpan(`#/client/aa:bb:cc:dd:ee:ff?from=clients&span=${A}~${B}`), { from: A, to: B });
  assert.equal(parseSpan("#/"), null);
  assert.equal(parseSpan(`#/?span=${A}`), null);
  assert.equal(parseSpan(`#/?span=${B}~${A}`), null, "backwards");
  assert.equal(parseSpan(`#/?span=${A}~${A}`), null, "empty");
  assert.equal(parseSpan("#/?span=yesterday~today"), null);
  // Only whole RFC 3339 times in UTC: nothing else reaches the API.
  assert.equal(parseSpan("#/?span=2026-11-22~2026-11-23"), null);
});

test("a stretch is written into the address beside what is there, and taken out again", () => {
  assert.equal(withSpan("#/", { from: A, to: B }), `#/?span=${A}~${B}`);
  assert.equal(withSpan("#/asn/13335?from=destinations", { from: A, to: B }), `#/asn/13335?from=destinations&span=${A}~${B}`);
  assert.equal(withSpan(`#/asn/13335?from=destinations&span=${A}~${B}`, null), "#/asn/13335?from=destinations");
  assert.equal(withSpan(`#/?span=${A}~${B}`, null), "#/");
  assert.equal(withSpan("", { from: A, to: B }), `#/?span=${A}~${B}`);
});

test("the API is asked for the stretch when there is one, otherwise the period", () => {
  assert.equal(periodQuery("24h", null), "range=24h");
  assert.equal(periodQuery("24h", { from: A, to: B }), `range=24h&from=${encodeURIComponent(A)}&to=${encodeURIComponent(B)}`);
});

test("a stretch is drawn at the step of the shortest period that holds it, as the router reads it", () => {
  const span = (h: number) => ({ from: A, to: new Date(Date.parse(A) + h * 3600_000).toISOString() });
  assert.equal(periodStep("24h", null), 600);
  assert.equal(periodStep("7d", null), 3600);
  assert.equal(periodStep("24h", span(0.5)), 60);
  assert.equal(periodStep("24h", span(1)), 60);
  assert.equal(periodStep("1h", span(5)), 600);
  assert.equal(periodStep("1h", span(24)), 600);
  assert.equal(periodStep("1h", span(72)), 3600);
  assert.equal(periodStep("1h", span(480)), 86400);
});

test("a brushed stretch runs from the first column it covers to the end of the last", () => {
  const t = ["2026-11-22T09:00:00Z", "2026-11-22T09:10:00Z", "2026-11-22T09:20:00Z", "2026-11-22T09:30:00.000Z"];
  assert.deepEqual(brushSpan(t, 600, 1, 2), { from: "2026-11-22T09:10:00Z", to: "2026-11-22T09:30:00Z" });
  // Dragged right to left is the same stretch.
  assert.deepEqual(brushSpan(t, 600, 2, 1), { from: "2026-11-22T09:10:00Z", to: "2026-11-22T09:30:00Z" });
  // One column is one step: what "What made this spike?" asks for.
  assert.deepEqual(brushSpan(t, 600, 3, 3), { from: "2026-11-22T09:30:00Z", to: "2026-11-22T09:40:00Z" });
  // Out of range columns are held to the chart.
  assert.deepEqual(brushSpan(t, 600, -4, 99), { from: "2026-11-22T09:00:00Z", to: "2026-11-22T09:40:00Z" });
  assert.equal(brushSpan([], 600, 0, 1), null);
  assert.equal(brushSpan(["not a time"], 600, 0, 0), null);
});
