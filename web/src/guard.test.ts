import { test } from "node:test";
import assert from "node:assert/strict";
import { once } from "./guard";

test("a second press before the first has finished is dropped, and the button works again after", async () => {
  const flag = { current: false };
  let calls = 0;
  let release: () => void = () => {};
  const first = once(flag, () => new Promise<void>((r) => { calls++; release = r; }));
  // Pressed again at once, before anything could redraw.
  assert.equal(await once(flag, async () => { calls++; }), false);
  assert.equal(calls, 1);
  release();
  assert.equal(await first, true);
  assert.equal(await once(flag, async () => { calls++; }), true);
  assert.equal(calls, 2);
});

test("a refused change frees the button too", async () => {
  const flag = { current: false };
  await assert.rejects(() => once(flag, async () => { throw new Error("refused"); }));
  assert.equal(flag.current, false);
});
