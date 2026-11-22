import { test } from "node:test";
import assert from "node:assert/strict";
import { columnOfTime, crosshairRows, legendAfter, markersOn, peakColumn } from "./geometry";

const t = ["2026-11-22T09:00:00Z", "2026-11-22T09:10:00Z", "2026-11-22T09:20:00Z"];
const ms = (iso: string) => Date.parse(iso);

test("the crosshair names every visible layer's rate at its column, busiest first, and the upload", () => {
  const layers = [
    { label: "YouTube", color: "#f00", values: [100, 50, 0] },
    { label: "Netflix", color: "#0f0", values: [10, 400, 0] },
    { label: "other", color: "#999", values: [1, 0, 0] },
  ];
  const up = [5, 6, 7];
  assert.deepEqual(crosshairRows(1, layers, up, []), {
    down: 450,
    up: 6,
    rows: [
      { label: "Netflix", color: "#0f0", rate: 400 },
      { label: "YouTube", color: "#f00", rate: 50 },
    ],
  });
  // A hidden layer is not named, and not in the total.
  assert.deepEqual(crosshairRows(1, layers, up, ["Netflix"])!.rows.map((r) => r.label), ["YouTube"]);
  assert.equal(crosshairRows(1, layers, up, ["Netflix"])!.down, 50);
  // A quiet column says so with no rows.
  assert.deepEqual(crosshairRows(2, layers, up, []), { down: 0, up: 7, rows: [] });
  // Out of range is no column.
  assert.equal(crosshairRows(5, layers, up, []), null);
  assert.equal(crosshairRows(-1, layers, up, []), null);
});

test("a time shared from another chart lands on the column that holds it, or on none", () => {
  assert.equal(columnOfTime(t, 600, ms("2026-11-22T09:00:00Z")), 0);
  assert.equal(columnOfTime(t, 600, ms("2026-11-22T09:15:00Z")), 1);
  assert.equal(columnOfTime(t, 600, ms("2026-11-22T09:29:59Z")), 2);
  assert.equal(columnOfTime(t, 600, ms("2026-11-22T09:30:00Z")), -1, "past the last step");
  assert.equal(columnOfTime(t, 600, ms("2026-11-22T08:59:59Z")), -1, "before the first");
  assert.equal(columnOfTime([], 600, ms(t[0])), -1);
});

test("events are marked where they happened on the time axis, and only those inside it", () => {
  const ev = [
    { at: "2026-11-22T08:00:00Z", text: "too early", kind: "a" },
    { at: "2026-11-22T09:00:00Z", text: "first", kind: "a" },
    { at: "2026-11-22T09:15:00Z", text: "middle", kind: "b" },
    { at: "2026-11-22T09:30:00Z", text: "past the end", kind: "a" },
    { at: "bad", text: "no time", kind: "a" },
  ];
  // The axis runs over three steps of 10 minutes, 1000 units wide.
  assert.deepEqual(markersOn(t, 600, ev), [
    { x: 0, text: "first", kind: "a", at: "2026-11-22T09:00:00Z" },
    { x: 500, text: "middle", kind: "b", at: "2026-11-22T09:15:00Z" },
  ]);
  assert.deepEqual(markersOn([], 600, ev), []);
});

test("a legend click hides or shows one layer, a double click shows that layer alone or all again", () => {
  const all = ["a", "b", "c"];
  assert.deepEqual(legendAfter([], "b", "toggle", all), ["b"]);
  assert.deepEqual(legendAfter(["b"], "b", "toggle", all), []);
  assert.deepEqual(legendAfter([], "b", "isolate", all), ["a", "c"]);
  // Isolating the one already alone brings every layer back.
  assert.deepEqual(legendAfter(["a", "c"], "b", "isolate", all), []);
  // Hiding the last visible layer is refused: a chart never goes blank by a click.
  assert.deepEqual(legendAfter(["a", "c"], "b", "toggle", all), ["a", "c"]);
});

test("the spike is the busiest column, the first of equals, none on a quiet chart", () => {
  assert.equal(peakColumn([1, 9, 3, 9]), 1);
  assert.equal(peakColumn([0, 0]), -1);
  assert.equal(peakColumn([]), -1);
});
