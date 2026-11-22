import { test } from "node:test";
import assert from "node:assert/strict";
import { parseRoute, parseFlowQuery, flowsPath, RANGES } from "./query";

test("hash routes name the screens", () => {
  assert.deepEqual(parseRoute(""), { name: "overview" });
  assert.deepEqual(parseRoute("#/"), { name: "overview" });
  assert.deepEqual(parseRoute("#/clients"), { name: "clients" });
  assert.deepEqual(parseRoute("#/client/aa:bb:cc:dd:ee:01"), { name: "client", mac: "aa:bb:cc:dd:ee:01" });
  assert.deepEqual(parseRoute("#/client/AA%3ABB%3ACC%3ADD%3AEE%3A01"), { name: "client", mac: "aa:bb:cc:dd:ee:01" });
  assert.deepEqual(parseRoute("#/flows"), { name: "flows", q: "" });
  assert.deepEqual(parseRoute("#/flows?q=mac%3Aaa%3Abb%3Acc%3Add%3Aee%3A01"), { name: "flows", q: "mac:aa:bb:cc:dd:ee:01" });
  assert.deepEqual(parseRoute("#/nowhere"), { name: "overview" });
  assert.deepEqual(parseRoute("#/rules"), { name: "rules" });
  assert.deepEqual(parseRoute("#/client/"), { name: "clients" });
});

test("#/rules is the rules screen", () => {
  assert.deepEqual(parseRoute("#/rules"), { name: "rules" });
  assert.deepEqual(parseRoute("#/rules?x=1"), { name: "rules" });
});

test("the period pills map to the API's ranges", () => {
  assert.deepEqual(RANGES.map((r) => [r.label, r.id, r.step]), [
    ["1 h", "1h", 60], ["24 h", "24h", 600], ["7 days", "7d", 3600], ["30 days", "30d", 86400],
  ]);
});

test("a flow search splits into the API's filters and free text", () => {
  assert.deepEqual(parseFlowQuery("hello"), { q: "hello", mac: "", app: "", cc: "", dir: "" });
  assert.deepEqual(parseFlowQuery('app:netflix cc:de dir:out youtube video'), { q: "youtube video", mac: "", app: "netflix", cc: "DE", dir: "out" });
  assert.deepEqual(parseFlowQuery("mac:AA:BB:CC:DD:EE:01"), { q: "", mac: "aa:bb:cc:dd:ee:01", app: "", cc: "", dir: "" });
  // A client is named in quotes and resolved to its MAC.
  const devices = [{ mac: "aa:bb:cc:dd:ee:07", name: "Yard camera" }];
  assert.deepEqual(parseFlowQuery('client:"yard camera" dir:out', devices), { q: "", mac: "aa:bb:cc:dd:ee:07", app: "", cc: "", dir: "out" });
  // A client nobody is called stays as text rather than being dropped.
  assert.deepEqual(parseFlowQuery('client:"Nobody"', devices), { q: "Nobody", mac: "", app: "", cc: "", dir: "" });
  // A direction the API refuses is not sent.
  assert.deepEqual(parseFlowQuery("dir:sideways"), { q: "dir:sideways", mac: "", app: "", cc: "", dir: "" });
});

test("a flows request carries only the filters that are set, encoded", () => {
  assert.equal(flowsPath({ q: "", mac: "", app: "", cc: "", dir: "" }, { live: true, limit: 200 }), "/api/flows?live=1&limit=200");
  assert.equal(
    flowsPath({ q: "a&b c", mac: "aa:bb:cc:dd:ee:01", app: "", cc: "DE", dir: "in" }, { live: false, limit: 50, cursor: "c2" }),
    "/api/flows?q=a%26b+c&mac=aa%3Abb%3Acc%3Add%3Aee%3A01&cc=DE&dir=in&limit=50&cursor=c2",
  );
});

test("#/rules/filters is the filters screen", () => {
  assert.deepEqual(parseRoute("#/rules/filters"), { name: "filters" });
  assert.deepEqual(parseRoute("#/rules"), { name: "rules" });
});

test("the live flows are asked by the window, the journal never", () => {
  const f = { q: "", mac: "", app: "", cc: "", dir: "" };
  assert.equal(flowsPath(f, { live: true, limit: 100, window: "3m" }), "/api/flows?live=1&window=3m&limit=100");
  assert.equal(flowsPath(f, { live: false, limit: 100, window: "3m" }), "/api/flows?limit=100");
});
