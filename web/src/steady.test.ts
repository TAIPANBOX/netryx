import { test } from "node:test";
import assert from "node:assert/strict";
import { steadyOrder, sortRows, nextSort, loadSort, saveSort, type Steady } from "./steady";
import { isPaused, setPaused, onPause } from "./pause";
import { WINDOWS, windowLabel } from "./query";

const rows = (pairs: [string, number][]) => pairs.map(([key, value]) => ({ key, value }));
const T = 1_000_000;

test("the first order is the sorted one", () => {
  const s = steadyOrder(null, rows([["a", 9], ["b", 5]]), T, "now:desc");
  assert.deepEqual(s.order, ["a", "b"]);
});

test("rows do not swap within 30 s, however much the figures move", () => {
  let s: Steady = steadyOrder(null, rows([["a", 100], ["b", 50]]), T, "now:desc");
  s = steadyOrder(s, rows([["b", 500], ["a", 100]]), T + 20_000, "now:desc");
  assert.deepEqual(s.order, ["a", "b"], "20 s after the last move");
  s = steadyOrder(s, rows([["b", 500], ["a", 100]]), T + 31_000, "now:desc");
  assert.deepEqual(s.order, ["b", "a"], "31 s after: the clear leader moves up");
});

test("a difference under the margin moves nothing, even after 30 s", () => {
  let s: Steady = steadyOrder(null, rows([["a", 100], ["b", 95]]), T, "now:desc");
  s = steadyOrder(s, rows([["b", 108], ["a", 100]]), T + 60_000, "now:desc");
  assert.deepEqual(s.order, ["a", "b"], "8% apart is not worth a jump");
  s = steadyOrder(s, rows([["b", 130], ["a", 100]]), T + 60_000, "now:desc");
  assert.deepEqual(s.order, ["b", "a"], "30% apart is");
});

test("a new row takes its place at once, a gone row leaves, the rest hold still", () => {
  let s: Steady = steadyOrder(null, rows([["a", 100], ["b", 50], ["c", 10]]), T, "now:desc");
  s = steadyOrder(s, rows([["a", 100], ["d", 70], ["c", 12]]), T + 1000, "now:desc");
  assert.deepEqual(s.order, ["a", "d", "c"]);
});

test("picking another sort reorders at once", () => {
  let s: Steady = steadyOrder(null, rows([["a", 100], ["b", 50]]), T, "now:desc");
  s = steadyOrder(s, rows([["b", 1], ["a", 2]]), T + 1000, "name:asc");
  assert.deepEqual(s.order, ["b", "a"]);
});

test("rows sort by any column, either way, ties by key", () => {
  const data = [{ k: "x", n: "Zed", v: 3 }, { k: "y", n: "amy", v: 9 }, { k: "z", n: "Bob", v: 3 }];
  const get = { name: (r: (typeof data)[0]) => r.n, now: (r: (typeof data)[0]) => r.v };
  assert.deepEqual(sortRows(data, (r) => r.k, get, { col: "now", dir: "desc" }).map((r) => r.k), ["y", "x", "z"]);
  assert.deepEqual(sortRows(data, (r) => r.k, get, { col: "name", dir: "asc" }).map((r) => r.k), ["y", "z", "x"], "names any case");
  assert.deepEqual(sortRows(data, (r) => r.k, get, { col: "nope", dir: "asc" }).map((r) => r.k), ["x", "y", "z"], "an unknown column keeps the order");
});

test("a click on the sorted column turns it round, on another starts at its own way", () => {
  assert.deepEqual(nextSort({ col: "now", dir: "desc" }, "now", "desc"), { col: "now", dir: "asc" });
  assert.deepEqual(nextSort({ col: "now", dir: "asc" }, "name", "asc"), { col: "name", dir: "asc" });
  assert.deepEqual(nextSort({ col: "name", dir: "asc" }, "now", "desc"), { col: "now", dir: "desc" });
});

test("the sort is remembered, and a broken or missing storage is not an error", () => {
  const mem = new Map<string, string>();
  (globalThis as { localStorage?: unknown }).localStorage = {
    getItem: (k: string) => mem.get(k) ?? null,
    setItem: (k: string, v: string) => void mem.set(k, v),
  };
  saveSort("apps", { col: "now", dir: "asc" });
  assert.deepEqual(loadSort("apps", { col: "bytes", dir: "desc" }, ["now", "bytes"]), { col: "now", dir: "asc" });
  mem.set("netryx.sort.apps", "{not json");
  assert.deepEqual(loadSort("apps", { col: "bytes", dir: "desc" }, ["now", "bytes"]), { col: "bytes", dir: "desc" });
  mem.set("netryx.sort.apps", JSON.stringify({ col: "evil", dir: "asc" }));
  assert.deepEqual(loadSort("apps", { col: "bytes", dir: "desc" }, ["now", "bytes"]), { col: "bytes", dir: "desc" }, "only a known column");
  (globalThis as { localStorage?: unknown }).localStorage = { getItem: () => { throw new Error("blocked"); }, setItem: () => { throw new Error("blocked"); } };
  assert.deepEqual(loadSort("apps", { col: "bytes", dir: "desc" }, ["bytes"]), { col: "bytes", dir: "desc" });
  saveSort("apps", { col: "now", dir: "asc" });
});

test("Pause freezes every poll until it is lifted, and says so to whoever listens", () => {
  const seen: boolean[] = [];
  const off = onPause((p) => seen.push(p));
  assert.equal(isPaused(), false);
  setPaused(true);
  assert.equal(isPaused(), true);
  setPaused(false);
  off();
  setPaused(true);
  setPaused(false);
  assert.deepEqual(seen, [true, false]);
});

test("the window pills are one, three and ten minutes", () => {
  assert.deepEqual(WINDOWS.map((w) => [w.id, w.label]), [["1m", "1 min"], ["3m", "3 min"], ["10m", "10 min"]]);
  assert.equal(windowLabel("10m"), "10 min");
});
