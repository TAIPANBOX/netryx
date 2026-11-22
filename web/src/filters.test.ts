import { test } from "node:test";
import assert from "node:assert/strict";
import { filtersBody, safeDnsText, type Filters } from "./filters";

test("filters body round-trips", () => {
  const f: Filters = { forceDns: true, blockDot: true, blockDoh: false, safeSearch: true, youtube: "strict", safeDns: { healthy: true, problem: "" } };
  assert.deepEqual(filtersBody(f), { forceDns: true, blockDot: true, blockDoh: false, safeSearch: true, youtube: "strict" });
  assert.deepEqual(filtersBody({ ...f, youtube: "loud" as Filters["youtube"] }).youtube, "off");
  assert.equal(safeDnsText(f), "DNS filter working");
  assert.equal(safeDnsText({ ...f, safeDns: { healthy: false, problem: "the safe resolver does not answer: refused" } }), "DNS filter not working: the safe resolver does not answer: refused");
  assert.equal(safeDnsText({ ...f, safeSearch: false, youtube: "off", safeDns: { healthy: false, problem: "" } }), "DNS filter off");
});
